package agent

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// SweepTitles keeps window titles tied to running agents. A window that
// carries an agent's title but runs no agent gets its automatic name back:
// a pane killed with the agent inside says nothing on its way out. The
// other way round, an agent running under tmux's automatic name gets its
// session's recorded title back.
func (e *Env) SweepTitles(ctx context.Context) error {
	format := strings.Join([]string{
		"#{window_id}", "#{window_name}", "#{" + OptTitle + "}", "#{automatic-rename}",
		"#{pane_id}", "#{pane_current_command}", "#{" + optAgent + "}", "#{" + optSession + "}",
	}, "\x1f")
	out, err := e.Tmux.Output(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return err
	}
	type pane struct{ id, agent, session string }
	type window struct {
		id, name, title string
		auto            bool
		agents          []pane // panes running an agent
	}
	var order []string
	windows := map[string]*window{}
	commands := e.Registry.CommandNames()
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\x1f", 8)
		if len(f) < 8 {
			continue
		}
		w, ok := windows[f[0]]
		if !ok {
			w = &window{id: f[0], name: f[1], title: f[2], auto: f[3] == "1"}
			windows[f[0]] = w
			order = append(order, f[0])
		}
		cmd, launched := f[5], f[6]
		if cmd != "" && (slices.Contains(commands, cmd) || (launched != "" && cmd == CommandName(e.Registry.Get(launched)))) {
			w.agents = append(w.agents, pane{id: f[4], agent: launched, session: f[7]})
		}
	}
	for _, id := range order {
		w := windows[id]
		switch {
		case w.title != "" && len(w.agents) == 0:
			args := []string{"set-option", "-wu", "-t", w.id, OptTitle}
			if w.title == w.name {
				args = append(args, ";", "set-option", "-w", "-t", w.id, "automatic-rename", "on")
			}
			if _, err := e.Tmux.Run(ctx, args...); err != nil && !isGone(err) {
				return err
			}
		case w.auto && len(w.agents) > 0 && e.Store != nil:
			for _, p := range w.agents {
				title := e.recordedTitle(p.agent, p.session)
				if title == "" {
					continue
				}
				_, err := e.Tmux.Run(ctx, "rename-window", "-t", w.id, title, ";",
					"set-option", "-w", "-t", w.id, OptTitle, title, ";",
					"set-option", "-p", "-t", p.id, optNamed, title)
				if err != nil && !isGone(err) {
					return err
				}
				break
			}
		}
	}
	return nil
}

func (e *Env) recordedTitle(agent, session string) string {
	if agent == "" || session == "" {
		return ""
	}
	r, ok, err := e.Store.Load(agent, session)
	if err != nil || !ok {
		return ""
	}
	return r.Title
}

// SweepAgents lets go of the session of each pane whose agent is gone
// without a word, as one killed or crashed outside the launcher: the resume
// command goes, so a restore does not bring the session back, and so do the
// session's pane options and the window title its agent set. The launcher
// ends its own runs: a pane whose launcher still runs, or whose run was
// killed (the session's record says so), keeps its session. A stopped agent
// is not gone. Flags are left to the core's sweep.
func (e *Env) SweepAgents(ctx context.Context) error {
	format := strings.Join([]string{
		"#{pane_id}", "#{pane_pid}", "#{" + optAgent + "}", "#{" + optRun + "}", "#{" + optSession + "}",
	}, "\x1f")
	out, err := e.Tmux.Output(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return err
	}
	type pane struct {
		id, agent, session string
		pid                int
	}
	var maybe []pane
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\x1f", 5)
		if len(f) < 5 || f[2] == "" || f[3] != "" {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		maybe = append(maybe, pane{id: f[0], pid: pid, agent: f[2], session: f[4]})
	}
	if len(maybe) == 0 {
		return nil
	}
	// The processes are read under the lock: an agent that starts in the
	// pane now is there before its first hook can write.
	defer e.lock(ctx)()
	tree := processTree()
	if tree == nil {
		return nil // no process table to tell a gone agent by
	}
	names := e.Registry.ProcessNames()
	for _, p := range maybe {
		if hasAgentBelow(p.pid, names, tree) || e.wasKilled(p.agent, p.session) {
			continue
		}
		l, err := e.liveOf(ctx, p.id)
		if err != nil {
			if isGone(err) {
				continue
			}
			return err
		}
		if l.agent == "" || l.run != "" {
			continue // a run started meanwhile
		}
		if err := e.releaseTitle(ctx, p.id, l); err != nil && !isGone(err) {
			return err
		}
		opts := map[string]string{optNamed: ""}
		for _, o := range sessionOpts {
			opts[o] = ""
		}
		if err := e.optsOf(ctx, p.id, opts); err != nil && !isGone(err) {
			return err
		}
		e.markEnded(e.Registry.Get(p.agent), p.session, EndUnknown, "")
	}
	return nil
}

// wasKilled reports whether the session's record says its run was killed,
// which keeps it for a resume.
func (e *Env) wasKilled(agent, session string) bool {
	if e.Store == nil || session == "" || !store.ValidName(session) {
		return false
	}
	r, ok, err := e.Store.Load(agent, session)
	return err == nil && ok && r.End == string(EndKilled)
}
