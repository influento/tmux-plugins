package agent

import (
	"context"
	"os"
	"strconv"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

func (e *Env) getenv(key string) string {
	if e.Getenv != nil {
		return e.Getenv(key)
	}
	return os.Getenv(key)
}

// gauge keeps the pane's context share current at the events that move
// it while no watcher runs: a session start, a compaction and the main
// agent's turn end. The caller holds the lock.
func (e *Env) gauge(ctx context.Context, a Agent, ev Event, l live) error {
	if ev.By != "" || (ev.Kind != SessionStart && ev.Kind != TurnEnd && ev.Kind != Compacted) {
		return nil
	}
	return e.setShare(ctx, a, or(ev.Transcript, l.transcript), or(ev.Cwd, l.cwd), "", l.context)
}

// setShare sets the pane's context share (attention.OptContext) from its
// agent's transcript; was is the share the pane shows. A transcript with
// no model call yet clears it. Only a change writes, and tells the panels.
// The caller holds the lock.
func (e *Env) setShare(ctx context.Context, a Agent, transcript, cwd, start, was string) error {
	g, ok := a.(ContextGauge)
	if !ok {
		return nil
	}
	share := ""
	if used, limit, ok := g.Context(transcript, cwd, start, e.getenv); ok && limit > 0 {
		share = strconv.FormatInt(max((used*100+limit/2)/limit, 1), 10)
	}
	if share == was {
		return nil
	}
	if err := e.paneOpts(ctx, map[string]string{attention.OptContext: share}); err != nil {
		return err
	}
	e.changed(ctx)
	return nil
}
