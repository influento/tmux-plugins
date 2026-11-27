package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/internal/lockfile"
	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/git"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// Env is what the agent layer works on: one tmux server, one pane, the
// records, and the lock that keeps two events from interleaving.
type Env struct {
	Tmux     attention.Tmux
	Core     *attention.Core
	Store    *store.Store
	Registry *Registry
	// Pane is the pane the agent runs in ($TMUX_PANE); empty outside tmux.
	Pane string
	// Run is the launcher's pid from $TMUX_AGENT_RUN; 0 without one.
	Run int
	// LockPath is the lock file; no locking when empty.
	LockPath string
	// LockWait bounds the wait for the lock. Past it the event goes ahead
	// unlocked: a late event is worse than a rare race.
	LockWait time.Duration
	// Nested reports whether the calling hook belongs to an agent started
	// inside another one. NestedHook when nil.
	Nested func(names []string, run int) bool
	// AgentArgs reads the arguments of the calling hook's agent process;
	// the nearest agent process above the hook when nil.
	AgentArgs func(names []string, run int) ([]string, bool)
	// OtherHooks reports whether hooks the agent started with the calling
	// one, for the same event, still run, in the hook's pane of server.
	// OtherHooksRun when nil.
	OtherHooks func(names []string, server int) bool
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Git reads a folder's checkout; git.HeadOf when nil.
	Git func(ctx context.Context, dir string) (git.Head, error)
	// Getenv reads the agent's environment, for its settings; os.Getenv
	// when nil.
	Getenv func(string) string
	// Log receives errors that must not reach the agent; none when nil.
	Log func(format string, args ...any)
	// Debug receives what helps explain a flag or a share after the fact:
	// each hook event, each change of the share; none when nil.
	Debug func(format string, args ...any)
	// Fill starts, in the background, the read of the agent's files for
	// the subagents of the session that ended (ScanSubagents); none when
	// nil. The files can be large, so the hook never reads them itself.
	Fill func(a Agent, sf SessionFiles, ids []string)
	// Watcher starts, in the background, a watcher (Watch) for the agent
	// in pane, process pid, which runs without one; none when nil.
	Watcher func(a Agent, pane string, pid int)
	// NotesDir holds a note per agent process: its session (see
	// lineage.go); none when empty.
	NotesDir string
	// Lineage names the agent process of the calling hook and the agent
	// process above that one; procLineage when nil.
	Lineage func(names []string) (self, parent string)
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// revive is the resume command for the session the calling hook names,
// or "" for none: a one-turn run never runs again.
func (e *Env) revive(a Agent, session string) string {
	cmd, ok := ReviveCommand(a, session)
	if !ok || OneShotHook(a, e.Registry.ProcessNames(), e.Run, e.server()) {
		return ""
	}
	return cmd
}

func (e *Env) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

func (e *Env) debugf(format string, args ...any) {
	if e.Debug != nil {
		e.Debug(format, args...)
	}
}

// lock takes the lock, or gives up waiting after LockWait and goes on.
func (e *Env) lock(ctx context.Context) func() {
	if e.LockPath == "" {
		return func() {}
	}
	wait := e.LockWait
	if wait == 0 {
		wait = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	unlock, err := lockfile.Lock(ctx, e.LockPath)
	if err != nil {
		e.logf("going on without the lock: %v", err)
		return func() {}
	}
	return unlock
}

// The pane options that make up a pane's live record. They go with the
// server; the store keeps a copy per session for after a restart.
const (
	optAgent      = "@agent"            // the agent running in the pane
	optSession    = "@agent_session"    // its session id
	optTranscript = "@agent_transcript" // its transcript
	optCwd        = "@agent_cwd"        // its folder
	optState      = "@agent_state"      // its last state, kept when a flag is seen
	optReason     = "@agent_reason"     // the state's reason
	optWait       = "@agent_wait"       // the tool a needs-you waits on
	optWaitBy     = "@agent_wait_by"    // the subagent it waits on; unset for the agent
	optNamed      = "@agent_named"      // the window name this pane's agent gave
	optRun        = "@agent_run"        // the launcher's pid
	optOneShot    = "@agent_oneshot"    // "1" for a one-turn run
	optResume     = "@agent_resume"     // the session the run resumes
	optStarted    = "@agent_started"    // when the run started, unix seconds
	optHeld       = "@agent_held"       // the turn ended, but heldRuns or heldHooks still run
	optPromptGot  = "@agent_prompt_got" // the last prompt the agent took: unix ms, then its PromptKey
	optName       = "@agent_name"       // the name the agent runs under, or will once it is typed
	optNameGiven  = "@agent_name_given" // the name the agent got: at its start, or typed in last
	// OptTitle is the window option that holds the title an agent gave
	// the window.
	OptTitle = "@agent_title"
	// OptRevive is the pane option tmux-revive types into the pane's shell
	// on restore.
	OptRevive = "@revive_cmd"
)

// live is a pane's live record and the facts about its window.
type live struct {
	agent, session, transcript, cwd, state, reason, wait, waitBy, named string
	subagents, nested                                                   []string
	held                                                                bool   // the turn ended, held by heldBy
	heldBy                                                              string // heldRuns or heldHooks
	panePid                                                             int
	run, oneShot, resume, revive, context                               string
	starting                                                            string // the channel of a start that waits
	task, role                                                          string // the pane's task binding, its agent's role
	flag                                                                attention.State
	autoRename, inMode                                                  bool
	windowName, title                                                   string
	name, nameGiven                                                     string
}

var liveFormat = strings.Join([]string{
	"#{" + optAgent + "}", "#{" + optSession + "}", "#{" + optTranscript + "}", "#{" + optCwd + "}",
	"#{" + optState + "}", "#{" + optReason + "}", "#{" + optWait + "}", "#{" + optNamed + "}",
	"#{" + optRun + "}", "#{" + optOneShot + "}", "#{" + optResume + "}", "#{" + OptRevive + "}",
	"#{@attention}", "#{automatic-rename}", "#{window_name}", "#{" + OptTitle + "}",
	"#{" + optWaitBy + "}", "#{" + attention.OptSubagents + "}",
	"#{" + attention.OptNested + "}", "#{" + optHeld + "}", "#{pane_pid}",
	"#{" + attention.OptContext + "}", "#{" + OptStarting + "}",
	"#{" + attention.OptTask + "}", "#{" + attention.OptRole + "}",
	"#{" + optName + "}", "#{" + optNameGiven + "}", "#{pane_in_mode}",
}, "\x1f")

const liveFields = 28

func (e *Env) readLive(ctx context.Context) (live, error) { return e.liveOf(ctx, e.Pane) }

func (e *Env) liveOf(ctx context.Context, pane string) (live, error) {
	out, err := e.Tmux.Output(ctx, "display-message", "-p", "-t", pane, liveFormat)
	if err != nil {
		return live{}, err
	}
	f := strings.SplitN(out, "\x1f", liveFields)
	for len(f) < liveFields {
		f = append(f, "")
	}
	return live{
		agent: f[0], session: f[1], transcript: f[2], cwd: f[3],
		state: f[4], reason: f[5], wait: f[6], named: f[7],
		run: f[8], oneShot: f[9], resume: f[10], revive: f[11],
		flag: attention.State(f[12]), autoRename: f[13] == "1", windowName: f[14], title: f[15],
		waitBy: f[16], subagents: strings.Fields(f[17]),
		nested: strings.Fields(f[18]), held: f[19] != "", heldBy: f[19], panePid: atoi(f[20]), context: f[21],
		starting: f[22], task: f[23], role: f[24],
		name: f[25], nameGiven: f[26], inMode: f[27] == "1",
	}, nil
}

// paneOpts writes options of the hook's pane in one call: a value of ""
// unsets.
func (e *Env) paneOpts(ctx context.Context, opts map[string]string) error {
	return e.optsOf(ctx, e.Pane, opts)
}

// optsOf writes options of pane in one call: a value of "" unsets.
func (e *Env) optsOf(ctx context.Context, pane string, opts map[string]string) error {
	var args []string
	for name, v := range opts {
		if len(args) > 0 {
			args = append(args, ";")
		}
		if v == "" {
			args = append(args, "set-option", "-pu", "-t", pane, name)
		} else {
			args = append(args, "set-option", "-p", "-t", pane, name, v)
		}
	}
	if len(args) == 0 {
		return nil
	}
	_, err := e.Tmux.Run(ctx, args...)
	return err
}

// ownRun reports whether the pane's run is the launcher this hook runs
// under.
func (e *Env) ownRun(l live) bool {
	return e.Run != 0 && l.run == strconv.Itoa(e.Run)
}

// remember updates the session's record, when the session is known.
func (e *Env) remember(a Agent, session string, fn func(*store.Session)) {
	if e.Store == nil || session == "" || !store.ValidName(session) {
		return
	}
	if err := e.Store.Update(a.Name(), session, fn); err != nil {
		e.logf("record %s: %v", session, err)
	}
}

// flag sets the pane's flag as owned by the agent's command, so the flag
// goes when the agent dies without saying so.
func (e *Env) flag(ctx context.Context, a Agent, s attention.State, reason string) (bool, error) {
	_, changed, err := e.Core.Set(ctx, e.Pane, attention.Flag{State: s, Reason: reason, Owner: CommandName(a)})
	return changed, err
}

// Hook applies one hook payload of agent a to the pane. Outside tmux, for
// an agent without hooks, or for a hook of an agent nested in another, it
// does nothing.
func (e *Env) Hook(ctx context.Context, a Agent, payload []byte) error {
	hr, ok := a.(HookReader)
	if !ok || e.Pane == "" {
		return nil
	}
	ev, ok, err := hr.ReadHook(payload)
	if err != nil {
		return err
	}
	if !ok {
		e.debugf("hook %s %s %s: dropped", e.Pane, a.Name(), ev.Name)
		return nil
	}
	nested := e.Nested
	if nested == nil {
		nested = func(names []string, run int) bool { return NestedHook(names, run, e.server()) }
	}
	names := e.Registry.ProcessNames()
	self, parent := e.lineage(names)
	e.noteSession(a, ev.SessionID, self)
	if nested(names, e.Run) {
		// A run the agent started: its watcher counts the runs again.
		e.debugf("hook %s %s %s: %s session %s, nested", e.Pane, a.Name(), ev.Name, ev.Kind, ev.SessionID)
		e.noteParent(ctx, a, ev.SessionID, parent)
		e.wake(e.Pane, wakeScan)
		return nil
	}
	e.debugf("hook %s %s %s: %s session %s%s", e.Pane, a.Name(), ev.Name, ev.Kind, ev.SessionID, by(ev.By))
	err = e.applyLocked(ctx, a, ev)
	e.keepWatched(a, ev)
	return err
}

func (e *Env) applyLocked(ctx context.Context, a Agent, ev Event) error {
	defer e.lock(ctx)()
	return e.Apply(ctx, a, ev)
}

// keepWatched tells the pane's watcher about an event that may change what
// it follows (a new session's transcript, a subagent's files, runs started
// during a turn), or starts a watcher when the pane has none: an agent run
// without the launcher, or one started before tmux-agent had watchers. A
// one-turn run gets none.
func (e *Env) keepWatched(a Agent, ev Event) {
	word := wakePing
	switch ev.Kind {
	case SessionEnd:
		return
	case SessionStart, SubagentStart, TurnEnd, Compacted:
		word = wakeScan
	}
	if e.wake(e.Pane, word) || e.Watcher == nil {
		return
	}
	names := e.Registry.ProcessNames()
	if OneShotHook(a, names, e.Run, e.server()) {
		return
	}
	if pid := nearestAgentPid(os.Getppid(), names, e.server()); pid > 0 {
		e.Watcher(a, e.Pane, pid)
	}
}

// Apply does what a neutral event means for the pane. The caller holds the
// lock.
func (e *Env) Apply(ctx context.Context, a Agent, ev Event) error {
	l, err := e.readLive(ctx)
	if err != nil {
		if isGone(err) {
			// The pane closed before its agent's last word.
			if ev.Kind == SessionEnd {
				e.markEnded(a, ev.SessionID, ev.End, "")
			}
			return nil
		}
		return err
	}
	if ev.Kind == SessionStart && l.starting != "" {
		// The agent is ready: the start that waits on it reads the pane
		// once this event is applied.
		defer e.signalStart(ctx, l.starting)
	}
	if e.ownRun(l) && l.oneShot == "1" {
		return e.oneShot(ctx, a, ev, l)
	}
	switch ev.Kind {
	case SessionStart:
		if err := e.start(ctx, a, ev, l); err != nil {
			return err
		}
		return e.refresh(ctx, a, ev, l, ev.SessionID)
	case SessionEnd:
		return e.end(ctx, a, ev, l)
	case Compacted:
		return e.refresh(ctx, a, ev, l, l.session)
	}
	session := l.session
	if ev.SessionID != "" && ev.SessionID != l.session {
		// A session whose start was missed, such as one already running
		// when tmux-agent was installed.
		session = ev.SessionID
		if err := e.adopt(ctx, a, ev); err != nil {
			return err
		}
	}
	if l.held && ev.By == "" && (ev.Kind == Working || ev.Kind == NeedsInput) {
		// The agent took a turn again: what holds it now is the turn.
		if err := e.paneOpts(ctx, map[string]string{optHeld: ""}); err != nil {
			return err
		}
	}
	switch ev.Kind {
	case Working:
		err = e.working(ctx, a, ev, l, session)
	case NeedsInput:
		err = e.needsInput(ctx, a, ev, session)
	case TurnEnd:
		err = e.turnEnd(ctx, a, ev, l, session)
	case SubagentStart:
		err = e.subagentStart(ctx, a, ev, l, session)
	case SubagentEnd:
		err = e.subagentEnd(ctx, a, ev, l, session)
	}
	if err != nil {
		return err
	}
	return e.refresh(ctx, a, ev, l, session)
}

// adopt makes the event's session the pane's.
func (e *Env) adopt(ctx context.Context, a Agent, ev Event) error {
	opts := map[string]string{optAgent: a.Name(), optSession: ev.SessionID}
	if ev.Transcript != "" {
		opts[optTranscript] = ev.Transcript
	}
	if cmd := e.revive(a, ev.SessionID); cmd != "" {
		opts[OptRevive] = cmd
	}
	return e.paneOpts(ctx, opts)
}

// oneShot: a one-shot run is one turn from launch to exit, and the launcher
// flags it, so its hooks only name the session. It never runs again, so it
// leaves no resume command.
func (e *Env) oneShot(ctx context.Context, a Agent, ev Event, l live) error {
	if ev.Kind == SessionEnd {
		e.markEnded(a, or(ev.SessionID, l.session), ev.End, "")
		return nil
	}
	if ev.SessionID == "" || ev.SessionID == l.session {
		return nil
	}
	opts := map[string]string{optAgent: a.Name(), optSession: ev.SessionID}
	if ev.Transcript != "" {
		opts[optTranscript] = ev.Transcript
	}
	if ev.Cwd != "" {
		opts[optCwd] = ev.Cwd
	}
	if err := e.paneOpts(ctx, opts); err != nil {
		return err
	}
	now := e.now().Unix()
	e.remember(a, ev.SessionID, func(s *store.Session) {
		s.Transcript, s.Cwd, s.Pane = or(ev.Transcript, s.Transcript), or(ev.Cwd, s.Cwd), e.Pane
		if s.Started == 0 {
			s.Started = now
		}
	})
	return nil
}

// start: a new session, or one resumed. A resume that a restore started
// (the launcher's --resume names this session) brings back what the
// session was waiting on, as information: a pending decision as "before
// restart", a turn cut mid-work as "interrupted mid-turn", an unread done
// as done. Anything else starts clean. The record keeps the name the
// pane's agent gave its window, which a restore marks as such again.
func (e *Env) start(ctx context.Context, a Agent, ev Event, l live) error {
	state, reason := attention.Idle, ""
	restoring := ev.Resume && ev.SessionID != "" && l.resume == ev.SessionID
	var rec store.Session
	if restoring && e.Store != nil && store.ValidName(ev.SessionID) {
		r, ok, err := e.Store.Load(a.Name(), ev.SessionID)
		if err != nil {
			e.logf("record %s: %v", ev.SessionID, err)
		}
		if ok && r.End != string(EndExited) {
			rec = r
			state, reason = attention.State(r.State), r.Reason
		}
	}
	var err error
	switch state {
	case attention.NeedsYou:
		_, err = e.flag(ctx, a, attention.NeedsYou, "before restart: "+or(reason, "waiting on you"))
	case attention.Working:
		state, reason = attention.NeedsYou, "interrupted mid-turn"
		_, err = e.flag(ctx, a, attention.NeedsYou, reason)
	case attention.Done:
		_, err = e.flag(ctx, a, attention.Done, reason)
	default:
		state, reason = attention.Idle, ""
		err = e.Core.Clear(ctx, e.Pane)
	}
	if err != nil {
		return err
	}
	opts := map[string]string{
		optAgent: a.Name(), optSession: ev.SessionID, optTranscript: ev.Transcript, optCwd: ev.Cwd,
		optState: string(state), optReason: reason, optWait: "", optWaitBy: "", optResume: "",
		attention.OptSubagents: "", attention.OptNested: "", attention.OptNestedRoles: "", optHeld: "",
	}
	opts[OptRevive] = e.revive(a, ev.SessionID)
	opts[attention.OptRole] = e.roleOf(a)
	task := l.task
	if task == "" && restoring {
		// After a restart: the pane the session resumes in is bound to
		// its task again, while the task is open.
		if t, ok := e.openTask(rec.Task); ok {
			task = t.Key()
			opts[attention.OptTask], opts[attention.OptTaskTitle] = task, t.Title
		}
	}
	if err := e.paneOpts(ctx, opts); err != nil {
		return err
	}
	now := e.now().Unix()
	e.remember(a, ev.SessionID, func(s *store.Session) {
		s.Transcript, s.Cwd, s.State, s.Reason, s.Pane = ev.Transcript, ev.Cwd, string(state), reason, e.Pane
		s.Task = task
		if s.Started == 0 {
			s.Started = now
		}
		s.Title = l.named
		s.End, s.Ended, s.Exit = "", 0, ""
	})
	return nil
}

// working: a turn or a tool started, or a tool finished. A pending decision
// stays unless the event ends it.
func (e *Env) working(ctx context.Context, a Agent, ev Event, l live, session string) error {
	if ev.Submitted && ev.By == "" {
		// The receipt that prompt waits for.
		if err := e.paneOpts(ctx, map[string]string{optPromptGot: strconv.FormatInt(e.now().UnixMilli(), 10) + " " + PromptKey(ev.Prompt)}); err != nil {
			return err
		}
		if err := e.Core.Report(ctx, e.Pane, attention.Label{Source: PromptSource}); err != nil {
			return err
		}
	}
	if ev.Subagent != nil {
		e.rememberSubagent(a, session, *ev.Subagent)
	}
	if l.flag == attention.NeedsYou && !endsWait(ev, l) {
		return nil
	}
	return e.markWorking(ctx, a, session)
}

// endsWait reports whether ev ends the decision a needs-you flag waits on.
// The agent and its subagents run side by side, so only the event of the
// one that waits counts; and a tool's end counts only for the tool waited
// on, not for another tool run beside it.
func endsWait(ev Event, l live) bool {
	if ev.By != l.waitBy {
		return false
	}
	return !ev.ToolEnd || l.wait == "" || ev.Tool == l.wait
}

// markWorking flags the pane working.
func (e *Env) markWorking(ctx context.Context, a Agent, session string) error {
	changed, err := e.flag(ctx, a, attention.Working, "")
	if err != nil || !changed {
		return err
	}
	if err := e.paneOpts(ctx, map[string]string{optState: string(attention.Working), optReason: "", optWait: "", optWaitBy: ""}); err != nil {
		return err
	}
	e.remember(a, session, func(s *store.Session) { s.State, s.Reason = string(attention.Working), "" })
	return nil
}

func (e *Env) needsInput(ctx context.Context, a Agent, ev Event, session string) error {
	if _, err := e.flag(ctx, a, attention.NeedsYou, ev.Reason); err != nil {
		return err
	}
	if err := e.paneOpts(ctx, map[string]string{optState: string(attention.NeedsYou), optReason: ev.Reason, optWait: ev.Tool, optWaitBy: ev.By}); err != nil {
		return err
	}
	e.remember(a, session, func(s *store.Session) { s.State, s.Reason = string(attention.NeedsYou), ev.Reason })
	return nil
}

// turnEnd flags done.
//
// A turn that ends while subagents run is not done: the pane stays working
// until the turn their reports start ends, and the pending decision of a
// subagent that still runs stays.
func (e *Env) turnEnd(ctx context.Context, a Agent, ev Event, l live, session string) error {
	if err := e.syncSubagents(ctx, a, ev, l, session); err != nil {
		return err
	}
	if len(ev.Subagents) == 0 && e.otherHooks(a) {
		// The agent's other turn-end hooks still run (a review that
		// runs an agent of its own, say), and one can take the turn on:
		// the turn ends when the agent goes idle (wentIdle).
		if err := e.markWorking(ctx, a, session); err != nil {
			return err
		}
		return e.paneOpts(ctx, map[string]string{optHeld: heldHooks})
	}
	return e.markTurnEnd(ctx, a, ev, l, session)
}

// What holds a turn that ended (optHeld).
const (
	heldRuns  = "1"    // agents the turn started run on
	heldHooks = "hook" // the agent's other turn-end hooks run on
)

// otherHooks reports whether hooks of agent a for the calling hook's event
// still run beside it. Only an agent that tells when it went idle can
// be held by them: nothing else would tell when they ended the turn.
func (e *Env) otherHooks(a Agent) bool {
	if _, ok := a.(Idler); !ok {
		return false
	}
	other := e.OtherHooks
	if other == nil {
		other = OtherHooksRun
	}
	return other(e.Registry.ProcessNames(), e.server())
}

// markTurnEnd sets the flag a turn end leaves.
func (e *Env) markTurnEnd(ctx context.Context, a Agent, ev Event, l live, session string) error {
	if len(ev.Subagents) > 0 {
		if l.flag == attention.NeedsYou && l.waitBy != "" && slices.Contains(ev.Subagents, l.waitBy) {
			return nil
		}
		return e.markWorking(ctx, a, session)
	}
	if runs := e.nestedRuns(l.panePid); len(runs) > 0 {
		if err := e.markWorking(ctx, a, session); err != nil {
			return err
		}
		return e.holdFor(ctx, e.Pane, l, runs)
	}
	if l.held || len(l.nested) > 0 {
		if err := e.paneOpts(ctx, map[string]string{optHeld: "", attention.OptNested: "", attention.OptNestedRoles: ""}); err != nil {
			return err
		}
	}
	const reason = "turn ended"
	changed, err := e.flag(ctx, a, attention.Done, reason)
	if err != nil || !changed {
		return err
	}
	if err := e.paneOpts(ctx, map[string]string{optState: string(attention.Done), optReason: reason, optWait: "", optWaitBy: ""}); err != nil {
		return err
	}
	e.remember(a, session, func(s *store.Session) { s.State, s.Reason = string(attention.Done), reason })
	return nil
}

// applyTitle names the pane's window and marks the name as an agent's.
func (e *Env) applyTitle(ctx context.Context, title string) error {
	_, err := e.Tmux.Run(ctx, "rename-window", "-t", e.Pane, title, ";",
		"set-option", "-w", "-t", e.Pane, OptTitle, title)
	return err
}

// end: the flag goes, the window name goes back to tmux if it is still the
// one this pane's agent set, and a session the user ended lets its resume
// go. A session that may have been killed keeps it: at shutdown tmux tears
// down every session before the agent hears of it. A run the launcher
// named keeps its name: its agent can go on to another session (/clear),
// and the launcher gives the window back when the run ends.
func (e *Env) end(ctx context.Context, a Agent, ev Event, l live) error {
	if err := e.Core.Clear(ctx, e.Pane); err != nil {
		return err
	}
	opts := map[string]string{attention.OptSubagents: "", attention.OptNested: "", attention.OptNestedRoles: "", optHeld: "", attention.OptContext: ""}
	if l.name == "" {
		if err := e.releaseTitle(ctx, e.Pane, l); err != nil {
			return err
		}
		opts[optNamed] = ""
	}
	if ev.End == EndExited {
		for _, o := range sessionOpts {
			opts[o] = ""
		}
	}
	if err := e.paneOpts(ctx, opts); err != nil {
		return err
	}
	if err := e.dropGit(ctx, e.Pane); err != nil {
		return err
	}
	e.markEnded(a, or(ev.SessionID, l.session), ev.End, "")
	return nil
}

// sessionOpts are the pane options that name the pane's session; they go
// when the session ends for good.
var sessionOpts = []string{optAgent, optSession, optTranscript, optCwd, optState, optReason, optWait, optWaitBy, OptRevive, attention.OptSubagents, attention.OptNested, attention.OptNestedRoles, optHeld, attention.OptContext, attention.OptRole}

// releaseTitle gives the window of pane back to tmux when its name is
// still the title the pane's agent set.
func (e *Env) releaseTitle(ctx context.Context, pane string, l live) error {
	if l.named == "" || l.title != l.named {
		return nil
	}
	args := []string{"set-option", "-wu", "-t", pane, OptTitle}
	if l.windowName == l.named {
		args = append(args, ";", "set-option", "-w", "-t", pane, "automatic-rename", "on")
	}
	_, err := e.Tmux.Run(ctx, args...)
	return err
}

// markEnded records how a session ended. A later, surer word wins: the
// launcher's killed or exited over a hook's unknown.
func (e *Env) markEnded(a Agent, session string, end End, exit string) {
	now := e.now().Unix()
	e.remember(a, session, func(s *store.Session) {
		if end == EndUnknown && s.End != "" {
			return
		}
		s.End, s.Ended = string(end), now
		if exit != "" {
			s.Exit = exit
		}
	})
}

// isGone reports whether a tmux error says only that the pane is gone.
func isGone(err error) bool {
	te, ok := errors.AsType[*tmux.Error](err)
	return ok && (strings.Contains(te.Stderr, "can't find") || strings.Contains(te.Stderr, "no such"))
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// RunFromEnv reads $TMUX_AGENT_RUN: the launcher's pid, or 0.
func RunFromEnv(getenv func(string) string) int {
	n, err := strconv.Atoi(getenv("TMUX_AGENT_RUN"))
	if err != nil || n <= 1 {
		return 0
	}
	return n
}

// runEnv is the environment for the agent: the caller's, with the run set
// and a start's channel left out, which only the launcher answers.
func runEnv(pid int) []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TMUX_AGENT_RUN=") && !strings.HasPrefix(kv, StartEnv+"=") {
			out = append(out, kv)
		}
	}
	return append(out, fmt.Sprintf("TMUX_AGENT_RUN=%d", pid))
}

// by names the subagent that sent an event, for the log.
func by(id string) string {
	if id == "" {
		return ""
	}
	return " by " + id
}
