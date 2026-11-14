package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// UsageReader reads what the model calls of an agent's sessions used, from
// its transcripts. Each session keeps a tally in the store, which a pass
// over the transcripts brings up to date (ScanUsage): it reads only what
// was written since the last pass.
type UsageReader interface {
	// Transcripts lists every session's transcript in the agent's files.
	Transcripts(getenv func(string) string) ([]SessionFiles, error)
	// ReadUsage reads the main agent's model calls in a transcript from
	// byte offset from up to its last whole line. recent are the ids of
	// the calls last read: a call whose id is among them was counted.
	ReadUsage(transcript string, from int64, recent []string) (UsageRead, error)
}

// UsageRead is what one read of a transcript found.
type UsageRead struct {
	Calls  []Call
	To     int64    // the offset after the last whole line read
	Recent []string // the ids of the last calls read, for the next read
	Cwd    string   // the first folder a line names
	// OneShot is true for a session that ran one prompt (claude -p), as
	// the line that names the folder tells.
	OneShot bool
}

// RunOwner names the session a run belongs to from the run's folder: one
// the agent made for a session of its own (Claude Code's scratchpad), in
// which that session started the run. "" when the folder says none.
type RunOwner interface {
	RunOwner(cwd string) string
}

// Call is one model call.
type Call struct {
	At     time.Time
	Model  string
	Tokens Tokens
}

// ScanUsage brings the tally of every session in the agent's files up to
// date. It returns how many tallies changed.
func (e *Env) ScanUsage(ctx context.Context, a Agent, getenv func(string) string) (int, error) {
	ur, ok := a.(UsageReader)
	if !ok || e.Store == nil {
		return 0, nil
	}
	sessions, err := ur.Transcripts(getenv)
	if err != nil {
		return 0, err
	}
	changed := 0
	var errs []error
	for _, sf := range sessions {
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		ok, err := e.scanUsageOf(ctx, a, ur, sf)
		if err != nil {
			errs = append(errs, err)
		}
		if ok {
			changed++
		}
	}
	return changed, errors.Join(errs...)
}

// scanUsageOf reads what a session's transcript gained since its tally's
// last read. The read runs without the lock, since a first one can take
// long; the tally is written under the lock, and only when no other pass
// moved it meanwhile.
func (e *Env) scanUsageOf(ctx context.Context, a Agent, ur UsageReader, sf SessionFiles) (bool, error) {
	if !store.ValidName(sf.Session) {
		return false, nil
	}
	fi, err := os.Stat(sf.Transcript)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // the tally keeps what it read
	}
	if err != nil {
		return false, err
	}
	u, err := e.loadUsage(a, sf.Session)
	if err != nil {
		return false, err
	}
	if fi.Size() == u.Offset {
		return false, nil
	}
	from := u.Offset
	if fi.Size() < from {
		from = 0 // a transcript written anew is read anew
	}
	recent := u.Recent
	if from == 0 {
		recent = nil
	}
	read, err := ur.ReadUsage(sf.Transcript, from, recent)
	if err != nil {
		return false, err
	}
	if read.To == from {
		return false, nil // only a line still being written: the next pass reads it
	}
	title, sure := u.Title, u.TitleSure
	if t, ok := a.(Titler); ok && !sure {
		if tt, s := t.Title(sf.Transcript); tt != "" {
			title, sure = tt, s
		}
	}
	unlock := e.lock(ctx)
	defer unlock()
	now, err := e.loadUsage(a, sf.Session)
	if err != nil {
		return false, err
	}
	if now.Offset != u.Offset {
		return false, nil // another pass moved it: its read stands
	}
	if from == 0 {
		now.Hours = nil
	}
	for _, c := range read.Calls {
		now.Add(c.At, c.Model, c.Tokens)
	}
	now.Transcript, now.Offset, now.Recent = sf.Transcript, read.To, read.Recent
	now.Title, now.TitleSure = title, sure
	if now.Cwd == "" || from == 0 {
		now.Cwd, now.OneShot = read.Cwd, read.OneShot
	}
	return true, e.Store.SaveUsage(now)
}

// isDamaged reports whether err is a record that is no JSON.
func isDamaged(err error) bool {
	_, bad := errors.AsType[*json.SyntaxError](err)
	return bad
}

// loadUsage reads a session's tally; a damaged one starts again.
func (e *Env) loadUsage(a Agent, session string) (store.Usage, error) {
	u, _, err := e.Store.LoadUsage(a.Name(), session)
	if isDamaged(err) {
		u, err = store.Usage{}, nil
	}
	if err != nil {
		return store.Usage{}, err
	}
	u.Agent, u.Session = a.Name(), session
	return u, nil
}
