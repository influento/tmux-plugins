package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
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
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Log receives errors that must not reach the agent; none when nil.
	Log func(format string, args ...any)
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Env) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
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
	unlock, err := store.Lock(ctx, e.LockPath)
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
	optNamed      = "@agent_named"      // the window title this pane's agent set
	optRun        = "@agent_run"        // the launcher's pid
	optOneShot    = "@agent_oneshot"    // "1" for a one-turn run
	optResume     = "@agent_resume"     // the session the run resumes
	optStarted    = "@agent_started"    // when the run started, unix seconds
	// OptTitle is the window option that holds the title an agent gave
	// the window.
	OptTitle = "@agent_title"
	// OptRevive is the pane option tmux-revive types into the pane's shell
	// on restore.
	OptRevive = "@revive_cmd"
)

// live is a pane's live record and the facts about its window.
type live struct {
	agent, session, transcript, cwd, state, reason, wait, named string
	run, oneShot, resume, revive                                string
	flag                                                        attention.State
	autoRename                                                  bool
	windowName, title                                           string
}

var liveFormat = strings.Join([]string{
	"#{" + optAgent + "}", "#{" + optSession + "}", "#{" + optTranscript + "}", "#{" + optCwd + "}",
	"#{" + optState + "}", "#{" + optReason + "}", "#{" + optWait + "}", "#{" + optNamed + "}",
	"#{" + optRun + "}", "#{" + optOneShot + "}", "#{" + optResume + "}", "#{" + OptRevive + "}",
	"#{@attention}", "#{automatic-rename}", "#{window_name}", "#{" + OptTitle + "}",
}, "\x1f")

func (e *Env) readLive(ctx context.Context) (live, error) {
	out, err := e.Tmux.Output(ctx, "display-message", "-p", "-t", e.Pane, liveFormat)
	if err != nil {
		return live{}, err
	}
	f := strings.SplitN(out, "\x1f", 16)
	for len(f) < 16 {
		f = append(f, "")
	}
	return live{
		agent: f[0], session: f[1], transcript: f[2], cwd: f[3],
		state: f[4], reason: f[5], wait: f[6], named: f[7],
		run: f[8], oneShot: f[9], resume: f[10], revive: f[11],
		flag: attention.State(f[12]), autoRename: f[13] == "1", windowName: f[14], title: f[15],
	}, nil
}

// paneOpts writes pane options in one call: a value of "" unsets.
func (e *Env) paneOpts(ctx context.Context, opts map[string]string) error {
	var args []string
	for name, v := range opts {
		if len(args) > 0 {
			args = append(args, ";")
		}
		if v == "" {
			args = append(args, "set-option", "-pu", "-t", e.Pane, name)
		} else {
			args = append(args, "set-option", "-p", "-t", e.Pane, name, v)
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
	if err != nil || !ok {
		return err
	}
	nested := e.Nested
	if nested == nil {
		nested = NestedHook
	}
	if nested(e.Registry.ProcessNames(), e.Run) {
		return nil
	}
	defer e.lock(ctx)()
	return e.Apply(ctx, a, ev)
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
	if e.ownRun(l) && l.oneShot == "1" {
		return e.oneShot(ctx, a, ev, l)
	}
	switch ev.Kind {
	case SessionStart:
		return e.start(ctx, a, ev, l)
	case SessionEnd:
		return e.end(ctx, a, ev, l)
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
	switch ev.Kind {
	case Working:
		return e.working(ctx, a, ev, l, session)
	case NeedsInput:
		return e.needsInput(ctx, a, ev, session)
	case TurnEnd:
		return e.turnEnd(ctx, a, ev, l, session)
	}
	return nil
}

// adopt makes the event's session the pane's.
func (e *Env) adopt(ctx context.Context, a Agent, ev Event) error {
	opts := map[string]string{optAgent: a.Name(), optSession: ev.SessionID}
	if ev.Transcript != "" {
		opts[optTranscript] = ev.Transcript
	}
	if cmd, ok := ReviveCommand(a, ev.SessionID); ok {
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
// as done; and its window title. Anything else starts clean.
func (e *Env) start(ctx context.Context, a Agent, ev Event, l live) error {
	state, reason, title := attention.Idle, "", ""
	restoring := ev.Resume && ev.SessionID != "" && l.resume == ev.SessionID
	var rec store.Session
	if restoring && e.Store != nil && store.ValidName(ev.SessionID) {
		r, ok, err := e.Store.Load(a.Name(), ev.SessionID)
		if err != nil {
			e.logf("record %s: %v", ev.SessionID, err)
		}
		if ok && r.End != string(EndExited) {
			rec = r
			state, reason, title = attention.State(r.State), r.Reason, r.Title
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
		optState: string(state), optReason: reason, optWait: "", optResume: "",
	}
	if title != "" {
		if err := e.applyTitle(ctx, title); err != nil {
			return err
		}
		opts[optNamed] = title
	}
	opts[OptRevive] = ""
	if cmd, ok := ReviveCommand(a, ev.SessionID); ok {
		opts[OptRevive] = cmd
	}
	if err := e.paneOpts(ctx, opts); err != nil {
		return err
	}
	now := e.now().Unix()
	e.remember(a, ev.SessionID, func(s *store.Session) {
		s.Transcript, s.Cwd, s.State, s.Reason, s.Pane = ev.Transcript, ev.Cwd, string(state), reason, e.Pane
		if s.Started == 0 {
			s.Started = now
		}
		if restoring {
			s.Title = rec.Title
		}
		s.End, s.Ended, s.Exit = "", 0, ""
	})
	return nil
}

// working: a turn or a tool started, or a tool finished. A decision that is
// pending on one tool stays when another tool, run beside it, finishes.
func (e *Env) working(ctx context.Context, a Agent, ev Event, l live, session string) error {
	if ev.ToolEnd && l.flag == attention.NeedsYou && l.wait != "" && ev.Tool != l.wait {
		return nil
	}
	changed, err := e.flag(ctx, a, attention.Working, "")
	if err != nil || !changed {
		return err
	}
	if err := e.paneOpts(ctx, map[string]string{optState: string(attention.Working), optReason: "", optWait: ""}); err != nil {
		return err
	}
	e.remember(a, session, func(s *store.Session) { s.State, s.Reason = string(attention.Working), "" })
	return nil
}

func (e *Env) needsInput(ctx context.Context, a Agent, ev Event, session string) error {
	if _, err := e.flag(ctx, a, attention.NeedsYou, ev.Reason); err != nil {
		return err
	}
	if err := e.paneOpts(ctx, map[string]string{optState: string(attention.NeedsYou), optReason: ev.Reason, optWait: ev.Tool}); err != nil {
		return err
	}
	e.remember(a, session, func(s *store.Session) { s.State, s.Reason = string(attention.NeedsYou), ev.Reason })
	return nil
}

// turnEnd flags done, and names the window from the first prompt while tmux
// still names it: once named, automatic-rename is off and it stays.
func (e *Env) turnEnd(ctx context.Context, a Agent, ev Event, l live, session string) error {
	const reason = "turn ended"
	changed, err := e.flag(ctx, a, attention.Done, reason)
	if err != nil {
		return err
	}
	if changed {
		if err := e.paneOpts(ctx, map[string]string{optState: string(attention.Done), optReason: reason, optWait: ""}); err != nil {
			return err
		}
		e.remember(a, session, func(s *store.Session) { s.State, s.Reason = string(attention.Done), reason })
	}
	t, ok := a.(Titler)
	if !ok || !l.autoRename {
		return nil
	}
	title := t.Title(or(ev.Transcript, l.transcript))
	if title == "" {
		return nil
	}
	if err := e.applyTitle(ctx, title); err != nil {
		return err
	}
	if err := e.paneOpts(ctx, map[string]string{optNamed: title}); err != nil {
		return err
	}
	e.remember(a, session, func(s *store.Session) { s.Title = title })
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
// down every session before the agent hears of it.
func (e *Env) end(ctx context.Context, a Agent, ev Event, l live) error {
	if err := e.Core.Clear(ctx, e.Pane); err != nil {
		return err
	}
	if l.named != "" && l.title == l.named {
		args := []string{"set-option", "-wu", "-t", e.Pane, OptTitle}
		if l.windowName == l.named {
			args = append(args, ";", "set-option", "-w", "-t", e.Pane, "automatic-rename", "on")
		}
		if _, err := e.Tmux.Run(ctx, args...); err != nil {
			return err
		}
	}
	opts := map[string]string{optNamed: ""}
	if ev.End == EndExited {
		for _, o := range []string{optAgent, optSession, optTranscript, optCwd, optState, optReason, optWait, OptRevive} {
			opts[o] = ""
		}
	}
	if err := e.paneOpts(ctx, opts); err != nil {
		return err
	}
	e.markEnded(a, or(ev.SessionID, l.session), ev.End, "")
	return nil
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

// runEnv is the environment for the agent: the caller's, with the run set.
func runEnv(pid int) []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TMUX_AGENT_RUN=") {
			out = append(out, kv)
		}
	}
	return append(out, fmt.Sprintf("TMUX_AGENT_RUN=%d", pid))
}
