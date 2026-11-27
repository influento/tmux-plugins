package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// A watcher keeps what a pane shows of its agent live while the agent
// runs, from what the agent sends no hook for: a turn the user cuts short,
// a subagent the user stops, the context used by each model call, the
// title, a branch switched from elsewhere, the runs the agent starts
// outside its subagents, and a turn that ended with no word in the
// transcript, from the file the agent keeps for its process (Idler). It follows the agent's files with inotify, the
// runs with pidfds, and hooks wake it through a socket; it never polls.
// The launcher runs one beside its agent; an agent started without the
// launcher gets one of its own (tmux-agent watch).

// watchDelay gathers a burst of changes into one look: the agent writes
// its transcript in pieces.
const watchDelay = 100 * time.Millisecond

// Watch follows the pane's agent until ctx ends, the pane goes, or, when
// agentPid is not 0, that process exits: its session then ends as one gone
// without a word. It returns ErrWatched, at once, when the pane has a live
// watcher already.
func (e *Env) Watch(ctx context.Context, a Agent, agentPid int) error {
	return e.watch(ctx, a, agentPid, nil)
}

// watch is Watch for a launcher, which starts its watcher before its
// agent: started gives the agent's pid once it runs.
func (e *Env) watch(ctx context.Context, a Agent, agentPid int, started <-chan int) error {
	path := watchSocket(e.getenv, e.Pane)
	if path == "" {
		return errors.New("watch: no tmux server or pane")
	}
	conn, release, err := listenWakes(path)
	if err != nil {
		return err
	}
	defer release()
	wakes := make(chan string, 8)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return // closed
			}
			select {
			case wakes <- string(buf[:n]):
			default:
			}
		}
	}()
	files, err := newFileWatch()
	if err != nil {
		return err
	}
	defer files.close()
	exits := newExitWatch()
	defer exits.close()

	again := time.NewTimer(time.Hour)
	again.Stop()
	defer again.Stop()
	w := &watchState{a: a, agentPid: agentPid, process: agentPid, files: files, exits: exits, start: startFolder(agentPid), again: again}
	exits.set(w.pids(nil))
	if err := e.look(ctx, w, true, false); err != nil {
		if isGone(err) || ctx.Err() != nil {
			return nil
		}
		e.logf("watch %s: %v", e.Pane, err)
	}
	timer := time.NewTimer(watchDelay)
	timer.Stop()
	pending, scan, git := false, false, false
	for {
		select {
		case <-ctx.Done():
			return nil
		case pid := <-started:
			started = nil
			w.process = pid
		case word := <-wakes:
			if word == wakePing {
				continue
			}
			scan = scan || word != wakeLook
		case <-again.C:
		case p := <-files.changes:
			if w.gitDir != "" && p == w.gitDir {
				git = true
			}
		case pid := <-exits.exited:
			if pid == agentPid {
				if err := e.SweepAgents(ctx); err != nil && !isGone(err) {
					return err
				}
				return nil
			}
			scan = true
		case <-timer.C:
			pending = false
			err := e.look(ctx, w, scan, git)
			scan, git = false, false
			switch {
			case err == nil:
			case ctx.Err() != nil, isGone(err):
				return nil
			default:
				e.logf("watch %s: %v", e.Pane, err) // the next change tries again
			}
			continue
		}
		if !pending {
			pending = true
			timer.Reset(watchDelay)
		}
	}
}

// watchState is what a watcher follows now.
type watchState struct {
	a          Agent
	agentPid   int
	process    int // the agent's process, once known: its status file
	files      *fileWatch
	exits      *exitWatch
	transcript string // the session's transcript the files follow
	cwd        string // the folder the git folder was read for
	gitDir     string
	runs       []int       // the nested runs' pids
	start      string      // the folder the agent process started in
	again      *time.Timer // a look asked for another (typeName)
}

// pids are the processes to follow: the agent, when the watch ends with
// it, and the runs.
func (w *watchState) pids(runs []int) []int {
	w.runs = runs
	if w.agentPid > 0 {
		return append(slices.Clone(runs), w.agentPid)
	}
	return runs
}

// look brings the pane up to date with its agent's files: a turn cut
// short, the context share, subagents that ended, and a new name to type
// in; with
// scan, the runs below the agent; with git, the branch. It follows the
// files of the session it finds before it reads them, so a write during
// the read wakes the next look, and also when a step then fails: the
// next change is the next try. A pane it cannot read keeps what it
// followed.
func (e *Env) look(ctx context.Context, w *watchState, scan, git bool) error {
	unlock := e.lock(ctx)
	l, err := e.readLive(ctx)
	if err != nil {
		unlock()
		w.follow(e, ctx, w.transcript, w.cwd) // a file put in place of one is watched again
		return err
	}
	if l.agent != w.a.Name() || l.transcript == "" {
		unlock()
		w.follow(e, ctx, "", "")
		return nil // no session of this agent yet, or no more
	}
	w.follow(e, ctx, l.transcript, l.cwd)
	err = errors.Join(
		e.cutShort(ctx, e.Pane, w.a, l.session, l.transcript),
		e.wentIdle(ctx, w, l),
		e.typeName(ctx, w, l),
		e.setShare(ctx, w.a, l.transcript, l.cwd, w.start, l),
		e.endedSubagents(ctx, w.a, l),
	)
	if git && l.cwd != "" {
		err = errors.Join(err, e.Core.Report(ctx, e.Pane, e.gitLabelOf(ctx, l.cwd)))
	}
	unlock()
	if err != nil {
		return err
	}
	if scan {
		runs, err := e.nestedFor(ctx, e.Pane, l)
		if err != nil {
			return err
		}
		w.exits.set(w.pids(runs))
	}
	return nil
}

// follow points the file watch at the session's transcript and the
// agent's other files for it, and at the git folder of cwd. A transcript
// not there yet (an agent can write it first at the first prompt) is
// watched through its folder, for its name alone, until a look finds it.
func (w *watchState) follow(e *Env, ctx context.Context, transcript, cwd string) {
	if cwd != w.cwd {
		w.cwd, w.gitDir = cwd, ""
		if cwd != "" {
			if h, err := e.headOf(ctx, cwd); err == nil {
				w.gitDir = h.Dir
			}
		}
	}
	w.transcript = transcript
	paths := map[string]func(string) bool{}
	if transcript != "" {
		paths[transcript] = nil
		if _, err := os.Lstat(transcript); err != nil {
			name := filepath.Base(transcript)
			paths[filepath.Dir(transcript)] = func(n string) bool { return n == name }
		}
		if fw, ok := w.a.(FileWatcher); ok {
			for _, p := range fw.WatchedFiles(transcript) {
				paths[p] = nil
			}
		}
		if id, ok := w.a.(Idler); ok && w.process > 0 {
			paths[id.StatusFile(w.process, e.getenv)] = nil
		}
	}
	if w.gitDir != "" {
		paths[w.gitDir] = func(name string) bool { return name == "HEAD" }
	}
	w.files.set(paths)
}

// wentIdle ends the pane's working turn when the agent's process went idle
// after the flag was set, with no word in the transcript: Esc before the
// first token. A turn held by the agent's turn-end hooks ends as done. A
// turn held by nested runs stays, and so does one while subagents run:
// the process is idle while they work, after its own turn ended. The
// caller holds the lock.
func (e *Env) wentIdle(ctx context.Context, w *watchState, l live) error {
	id, ok := w.a.(Idler)
	if !ok || w.process <= 0 || l.flag != attention.Working || len(l.subagents) > 0 {
		return nil
	}
	idle := func() (time.Time, bool) { return id.IdleSince(w.process, l.session, e.getenv) }
	switch l.heldBy {
	case "":
		return e.endTurnAt(ctx, e.Pane, w.a, l.session, idle, attention.Working)
	case heldHooks:
		return e.endHooksHeld(ctx, e.Pane, w.a, l, idle)
	}
	return nil
}

// gitLabelOf is the git label of folder dir: empty outside a checkout.
func (e *Env) gitLabelOf(ctx context.Context, dir string) attention.Label {
	if h, err := e.headOf(ctx, dir); err == nil {
		return gitLabel(h)
	}
	return attention.Label{Source: GitSource}
}

// endedSubagents takes out of the pane's running subagents those whose
// files say they ended, as one the user stopped, and has their records
// filled. The caller holds the lock.
func (e *Env) endedSubagents(ctx context.Context, a Agent, l live) error {
	se, ok := a.(SubagentEnder)
	if !ok || len(l.subagents) == 0 {
		return nil
	}
	var running, ended []string
	for _, id := range l.subagents {
		if se.SubagentEnded(l.transcript, id) != "" {
			ended = append(ended, id)
		} else {
			running = append(running, id)
		}
	}
	if len(ended) == 0 {
		return nil
	}
	for _, id := range ended {
		e.rememberSubagent(a, l.session, Subagent{ID: id, Ended: e.now()})
	}
	e.fill(a, l.session, l.transcript, ended)
	return e.setRunning(ctx, running)
}

// nestedFor brings the pane's list of nested runs up to date, ends its
// held turn when they all ended, and returns their pids.
func (e *Env) nestedFor(ctx context.Context, pane string, l live) ([]int, error) {
	tree := processTree()
	if tree == nil {
		return nil, nil
	}
	procs := nestedProcs(l.panePid, e.Registry.ProcessNames(), tree)
	var pids []int
	for _, p := range procs {
		pids = append(pids, p.pid)
	}
	runs, roles := e.runsOf(procs)
	if !slices.Equal(runs, l.nested) {
		if err := e.optsOf(ctx, pane, map[string]string{attention.OptNested: strings.Join(runs, " "), attention.OptNestedRoles: roles}); err != nil {
			return nil, err
		}
		e.changed(ctx)
	}
	if l.heldBy == heldRuns && len(runs) == 0 {
		if err := e.endHeld(ctx, pane); err != nil {
			return nil, err
		}
	}
	return pids, nil
}

// WatchAll starts a watcher for each agent pane that has none: agents
// started before tmux-agent had watchers, or whose watcher died. A
// one-turn run gets none.
func (e *Env) WatchAll(ctx context.Context) error {
	if e.Watcher == nil {
		return nil
	}
	format := strings.Join([]string{"#{pane_id}", "#{pane_pid}", "#{" + optAgent + "}", "#{" + optOneShot + "}"}, "\x1f")
	out, err := e.Tmux.Output(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return err
	}
	var tree map[int][]proc
	names := e.Registry.ProcessNames()
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\x1f", 4)
		if len(f) < 4 || f[2] == "" || f[3] == "1" {
			continue
		}
		a, ok := e.Registry.Lookup(f[2])
		if !ok || e.wake(f[0], wakePing) {
			continue
		}
		panePid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		if tree == nil {
			if tree = processTree(); tree == nil {
				return nil // no process table to find the agents in
			}
		}
		if pid := firstAgentBelow(panePid, names, tree); pid > 0 {
			e.Watcher(a, f[0], pid)
		}
	}
	return nil
}

// startFolder is the folder of process pid, or of this process for 0: the
// launcher starts its agent in its own.
func startFolder(pid int) string {
	if pid == 0 {
		dir, _ := os.Getwd() // unknown: the session's folder stands in
		return dir
	}
	dir, _ := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "cwd")) // the same
	return dir
}
