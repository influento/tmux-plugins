package agent

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// A task is a unit of work a tool runs agents for (workbench's item),
// in windows `tmux-agent start --task` opens and binds to it
// (attention.OptTask). tmux-agent keeps an open task (store.Task) only
// to bind a resumed session's pane to it again after a restart: each
// session notes the task of its pane in its record. The roles come from
// each agent's own process (Roler), in a window or as a nested run.

// roleOf is the role of the calling hook's agent process, or "".
func (e *Env) roleOf(a Agent) string {
	r, ok := a.(Roler)
	if !ok {
		return ""
	}
	args := e.AgentArgs
	if args == nil {
		args = func(names []string, run int) ([]string, bool) {
			return nearestAgentArgs(os.Getppid(), names, run, e.server())
		}
	}
	got, ok := args(e.Registry.ProcessNames(), e.Run)
	if !ok {
		return ""
	}
	return r.Role(got)
}

// openTask is the open task a binding names; ok is false when it names
// none, or the task ended.
func (e *Env) openTask(key string) (store.Task, bool) {
	source, id, ok := store.ParseTaskKey(key)
	if !ok || e.Store == nil {
		return store.Task{}, false
	}
	t, ok, err := e.Store.LoadTask(source, id)
	if err != nil {
		e.logf("task %s: %v", key, err)
	}
	return t, ok
}

// BindTask binds panes to the task, and notes it in the record of each
// pane's agent session, so a resume after a restart binds it again.
func (e *Env) BindTask(ctx context.Context, t store.Task, panes []string) error {
	defer e.lock(ctx)()
	for _, pane := range panes {
		if err := e.optsOf(ctx, pane, map[string]string{attention.OptTask: t.Key(), attention.OptTaskTitle: t.Title}); err != nil {
			return err
		}
		l, err := e.liveOf(ctx, pane)
		if err != nil {
			return err
		}
		if a, ok := e.Registry.Lookup(l.agent); ok && l.session != "" {
			e.remember(a, l.session, func(s *store.Session) { s.Task = t.Key() })
		}
	}
	e.changed(ctx)
	return nil
}

// UnbindTask takes the panes bound to the task out of it.
func (e *Env) UnbindTask(ctx context.Context, key string, panes []string) error {
	defer e.lock(ctx)()
	for _, pane := range panes {
		l, err := e.liveOf(ctx, pane)
		if err != nil {
			if isGone(err) {
				continue
			}
			return err
		}
		if l.task != key {
			continue
		}
		if err := e.optsOf(ctx, pane, map[string]string{attention.OptTask: "", attention.OptTaskTitle: ""}); err != nil {
			return err
		}
		if a, ok := e.Registry.Lookup(l.agent); ok && l.session != "" {
			e.remember(a, l.session, func(s *store.Session) { s.Task = "" })
		}
	}
	e.changed(ctx)
	return nil
}

// runsOf is what a pane shows of its nested runs: their names, sorted, and
// the roles of those that run as one (attention.OptNestedRoles).
func (e *Env) runsOf(procs []proc) (names []string, roles string) {
	pnames := e.Registry.ProcessNames()
	var rs []string
	for _, p := range procs {
		name := agentName(p, pnames)
		names = append(names, name)
		a, ok := e.Registry.ByProcess(name)
		if !ok {
			continue
		}
		if r, ok := a.(Roler); ok {
			if role := r.Role(agentArgs(p, pnames)); role != "" {
				rs = append(rs, role+"@"+strconv.FormatInt(startUnix(p), 10))
			}
		}
	}
	slices.Sort(names)
	slices.Sort(rs)
	return names, strings.Join(rs, " ")
}

// bootTime is when the machine started, in unix seconds; 0 when unknown.
var bootTime = sync.OnceValue(func() int64 {
	data, err := os.ReadFile(procRoot + "/stat")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64) // 0 when garbled
			return n
		}
	}
	return 0
})

// clockTicks is USER_HZ, the unit of a process's start time: 100 on Linux.
const clockTicks = 100

// startUnix is when process p started, in unix seconds; 0 when unknown.
func startUnix(p proc) int64 {
	ticks, err := strconv.ParseInt(p.start, 10, 64)
	if err != nil || bootTime() == 0 {
		return 0
	}
	return bootTime() + ticks/clockTicks
}
