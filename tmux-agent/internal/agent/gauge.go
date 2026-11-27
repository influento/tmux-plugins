package agent

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// optWindow is the pane option that holds the context window the pane's
// agent gave its status line command, as "SESSION TOKENS": it counts for
// that session only.
const optWindow = "@agent_window"

func (e *Env) getenv(key string) string {
	if e.Getenv != nil {
		return e.Getenv(key)
	}
	return os.Getenv(key)
}

// gauge keeps the pane's context share current at the events that move
// it while no watcher runs: a session start, a compaction and the main
// agent's turn end. The caller holds the lock.
// session is the pane's session after the event.
func (e *Env) gauge(ctx context.Context, a Agent, ev Event, l live, session string) error {
	if ev.By != "" || (ev.Kind != SessionStart && ev.Kind != TurnEnd && ev.Kind != Compacted) {
		return nil
	}
	l.session = session
	return e.setShare(ctx, a, or(ev.Transcript, l.transcript), or(ev.Cwd, l.cwd), "", l)
}

// Status takes the input agent a gave its status line command: the context
// window of a session, which that session's share counts against. The
// agent runs the command without waiting for its session-start hook, so a
// window of a session the pane does not hold yet is kept for its start.
func (e *Env) Status(ctx context.Context, a Agent, input []byte) error {
	sr, ok := a.(StatusReader)
	if !ok || e.Pane == "" {
		return nil
	}
	s, ok := sr.ReadStatus(input)
	if !ok {
		return nil
	}
	told := s.Session + " " + strconv.FormatInt(s.Window, 10)
	// Most runs tell what the pane holds: those take no lock.
	if l, err := e.readLive(ctx); err != nil || l.toldBy(a, told) {
		return err
	}
	defer e.lock(ctx)()
	l, err := e.readLive(ctx)
	if err != nil || l.toldBy(a, told) {
		return err
	}
	if err := e.paneOpts(ctx, map[string]string{optWindow: told}); err != nil {
		return err
	}
	e.debugf("window %s: %s -> %s", e.Pane, or(l.window, "none"), told)
	l.window = told
	if l.session != s.Session {
		return nil // its session's start sets the share
	}
	return e.setShare(ctx, a, l.transcript, l.cwd, s.Project, l)
}

// toldBy says whether a status of agent a that tells told changes nothing:
// the pane holds it, runs another agent, or is gone (tmux answers for a
// pane that is gone with empty fields).
func (l live) toldBy(a Agent, told string) bool {
	return l.window == told || l.agent != "" && l.agent != a.Name() || l.panePid == 0
}

// toldWindow is the window the pane's agent told for the pane's session, 0
// when it told none.
func (l live) toldWindow() int64 {
	session, tokens, _ := strings.Cut(l.window, " ")
	if session != l.session {
		return 0
	}
	window, _ := strconv.ParseInt(tokens, 10, 64)
	return window
}

// setShare sets the pane's context share (attention.OptContext) from its
// agent's transcript; l holds the share the pane shows and the window its
// agent told. A transcript with no model call yet clears it. Only a change
// writes, and tells the panels. The caller holds the lock.
func (e *Env) setShare(ctx context.Context, a Agent, transcript, cwd, start string, l live) error {
	g, ok := a.(ContextGauge)
	if !ok {
		return nil
	}
	was, share := l.context, ""
	if used, limit, ok := g.Context(transcript, cwd, start, l.toldWindow(), e.getenv); ok && limit > 0 {
		share = strconv.FormatInt(max((used*100+limit/2)/limit, 1), 10)
	}
	if share == was {
		return nil
	}
	if err := e.paneOpts(ctx, map[string]string{attention.OptContext: share}); err != nil {
		return err
	}
	e.debugf("share %s: %s -> %s, %s", e.Pane, or(was, "none"), or(share, "none"), transcript)
	e.changed(ctx)
	return nil
}
