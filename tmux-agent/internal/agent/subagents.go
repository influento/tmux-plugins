package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// A pane keeps the ids of the subagents running beside its agent in
// attention.OptSubagents, for the panel's count. A subagent start adds
// one, its end takes it out, and a turn end sets the list to what the
// agent says still runs: a subagent the user stops sends no end. Each
// subagent gets a record next to its session's; the hooks give its start,
// type and prompt, and after its end a read of the agent's files adds its
// result, its tokens and how it ended.

// subagentStart: a subagent began beside the pane's agent.
func (e *Env) subagentStart(ctx context.Context, a Agent, ev Event, l live, session string) error {
	sub := ev.Subagent
	if sub == nil || sub.ID == "" {
		return nil
	}
	e.rememberSubagent(a, session, Subagent{ID: sub.ID, Type: sub.Type, Started: e.now()})
	if slices.Contains(l.subagents, sub.ID) {
		return nil
	}
	return e.setRunning(ctx, append(slices.Clone(l.subagents), sub.ID))
}

// subagentEnd: a subagent finished. Its files say how only a moment later,
// so their read starts in the background.
func (e *Env) subagentEnd(ctx context.Context, a Agent, ev Event, l live, session string) error {
	sub := ev.Subagent
	if sub == nil || sub.ID == "" {
		return nil
	}
	e.rememberSubagent(a, session, Subagent{ID: sub.ID, Type: sub.Type, Transcript: sub.Transcript, Ended: e.now()})
	e.fill(a, session, or(ev.Transcript, l.transcript), []string{sub.ID})
	if !slices.Contains(l.subagents, sub.ID) {
		return nil
	}
	return e.setRunning(ctx, slices.DeleteFunc(slices.Clone(l.subagents), func(id string) bool { return id == sub.ID }))
}

// syncSubagents sets the pane's running subagents to those a turn end
// lists. One gone from the list without an end of its own was stopped.
func (e *Env) syncSubagents(ctx context.Context, a Agent, ev Event, l live, session string) error {
	var gone []string
	for _, id := range l.subagents {
		if !slices.Contains(ev.Subagents, id) {
			gone = append(gone, id)
		}
	}
	for _, id := range gone {
		e.rememberSubagent(a, session, Subagent{ID: id, Ended: e.now()})
	}
	if len(gone) > 0 {
		e.fill(a, session, or(ev.Transcript, l.transcript), gone)
	}
	if slices.Equal(l.subagents, ev.Subagents) {
		return nil
	}
	return e.setRunning(ctx, ev.Subagents)
}

// setRunning writes the pane's running subagents and tells the panels,
// which show how many.
func (e *Env) setRunning(ctx context.Context, ids []string) error {
	if err := e.paneOpts(ctx, map[string]string{attention.OptSubagents: strings.Join(ids, " ")}); err != nil {
		return err
	}
	e.changed(ctx)
	return nil
}

// fill starts the read of the agent's files for the subagents ids of the
// session, when the agent has files to read.
func (e *Env) fill(a Agent, session, transcript string, ids []string) {
	if _, ok := a.(SubagentReader); !ok || e.Fill == nil || session == "" || transcript == "" {
		return
	}
	e.Fill(a, SessionFiles{Session: session, Transcript: transcript}, ids)
}

// rememberSubagent merges what an event tells about a subagent into its
// record. A value an event does not give leaves the record's; a start
// time already recorded stays, an end time already recorded too.
func (e *Env) rememberSubagent(a Agent, session string, sub Subagent) {
	if e.Store == nil || session == "" || !store.ValidName(session) || !store.ValidName(sub.ID) {
		return
	}
	err := e.Store.UpdateSubagent(a.Name(), session, sub.ID, func(r *store.Subagent) {
		setStr(&r.Type, sub.Type)
		setStr(&r.Description, sub.Description)
		setStr(&r.Prompt, sub.Prompt)
		setStr(&r.Model, sub.Model)
		setStr(&r.Transcript, sub.Transcript)
		if r.Started == 0 && !sub.Started.IsZero() {
			r.Started = sub.Started.Unix()
		}
		if r.Ended == 0 && !sub.Ended.IsZero() {
			r.Ended = sub.Ended.Unix()
		}
	})
	if err != nil {
		e.logf("subagent record %s: %v", sub.ID, err)
	}
}

func setStr(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// fillFrom puts what the agent's files tell about a subagent into its
// record. The files are surer than the hooks, so their values win; the
// record is complete once the files say how the subagent ended.
func fillFrom(r *store.Subagent, f Subagent) {
	setStr(&r.Type, f.Type)
	setStr(&r.Description, f.Description)
	setStr(&r.Prompt, f.Prompt)
	setStr(&r.Result, f.Result)
	setStr(&r.Model, f.Model)
	setStr(&r.Transcript, f.Transcript)
	if f.Depth != 0 {
		r.Depth = f.Depth
	}
	if !f.Started.IsZero() {
		r.Started = f.Started.Unix()
	}
	if !f.Ended.IsZero() {
		r.Ended = f.Ended.Unix()
	}
	if f.Tokens != nil {
		t := *f.Tokens
		r.Tokens = &t
	}
	r.Outcome = string(f.Outcome)
	r.Complete = f.Outcome != ""
}

// ScanSubagents fills the records of a session's subagents from the
// agent's files: each whose record is not complete and whose files say it
// ended. ids, when given, limits the read to those; for them it waits up
// to wait for files that still show them running, since a hook comes a
// moment before the files say how it ended. It returns how many records
// it completed.
func (e *Env) ScanSubagents(ctx context.Context, a Agent, sf SessionFiles, ids []string, wait time.Duration) (int, error) {
	sr, ok := a.(SubagentReader)
	if !ok || e.Store == nil || !store.ValidName(sf.Session) {
		return 0, nil
	}
	if len(ids) == 0 {
		var err error
		if ids, err = sr.SubagentIDs(sf.Transcript); err != nil {
			return 0, err
		}
	}
	deadline := e.now().Add(wait)
	filled := 0
	var errs []error
	for _, id := range ids {
		if !store.ValidName(id) {
			continue
		}
		rec, ok, err := e.Store.LoadSubagent(a.Name(), sf.Session, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok && rec.Complete {
			continue // the same subagent is never read twice
		}
		f, err := readEnded(ctx, sr, sf.Transcript, id, deadline, e.now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if f.Outcome == "" {
			continue // it still runs: a later pass reads it
		}
		unlock := e.lock(ctx)
		err = e.Store.UpdateSubagent(a.Name(), sf.Session, id, func(r *store.Subagent) { fillFrom(r, f) })
		unlock()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		filled++
	}
	return filled, errors.Join(errs...)
}

// readEnded reads a subagent from the agent's files, again every so often
// until they say it ended or the deadline passes.
func readEnded(ctx context.Context, sr SubagentReader, transcript, id string, deadline time.Time, now func() time.Time) (Subagent, error) {
	for {
		f, err := sr.ReadSubagent(transcript, id)
		if err != nil || f.Outcome != "" || !now().Before(deadline) {
			return f, err
		}
		select {
		case <-ctx.Done():
			return f, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// ScanAllSubagents is the pass over every session in the agent's files:
// it fills the records of their ended subagents that are not complete,
// those from before tmux-agent kept records among them.
func (e *Env) ScanAllSubagents(ctx context.Context, a Agent, getenv func(string) string) (int, error) {
	sr, ok := a.(SubagentReader)
	if !ok {
		return 0, nil
	}
	sessions, err := sr.SubagentSessions(getenv)
	if err != nil {
		return 0, err
	}
	filled := 0
	var errs []error
	for _, sf := range sessions {
		if err := ctx.Err(); err != nil {
			return filled, err
		}
		n, err := e.ScanSubagents(ctx, a, sf, nil, 0)
		filled += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	return filled, errors.Join(errs...)
}
