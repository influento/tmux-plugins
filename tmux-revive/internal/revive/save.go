package revive

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/influento/tmux-plugins/internal/lockfile"
	"github.com/influento/tmux-plugins/tmux-revive/internal/snapshot"
)

// SaveResult is what a save did.
type SaveResult int

const (
	// Coalesced: another save was already waiting, and it reads tmux after
	// this change, so this one did nothing.
	Coalesced SaveResult = iota
	// NoServer: no server, or a server with no sessions besides the
	// skipped ones, as during shutdown and kill-server. Nothing is written.
	NoServer
	// NotReady: the server has not been restored yet; nothing is written.
	NotReady
	// Unchanged: the newest snapshot already holds this state.
	Unchanged
	// Stopping: the save would lose panes while the machine or the login
	// session is shutting down; nothing is written.
	Stopping
	// Written: a new snapshot was added.
	Written
	// Replaced: only details changed, and the newest snapshot was replaced.
	Replaced
)

func (s SaveResult) String() string {
	switch s {
	case Coalesced:
		return "coalesced"
	case NoServer:
		return "no server"
	case NotReady:
		return "not ready"
	case Unchanged:
		return "unchanged"
	case Stopping:
		return "stopping"
	case Written:
		return "written"
	case Replaced:
		return "replaced"
	}
	return "unknown"
}

// Save writes a snapshot of the server when its state changed.
//
// Saves run from tmux hooks, in the background, and come in bursts. One
// lock covers reading tmux and writing the file, so an older save cannot
// finish last. A second lock lets one save wait for the first: a save that
// finds one already waiting returns at once, since the waiting one reads
// tmux after this change. settle is a wait before reading tmux, during
// which further changes join this save.
func (r *Reviver) Save(ctx context.Context, settle time.Duration) (SaveResult, error) {
	unqueue, ok, err := lockfile.TryLock(r.lockPath("queue"))
	if err != nil {
		return 0, err
	}
	if !ok {
		return Coalesced, nil
	}
	if err := sleep(ctx, settle); err != nil {
		unqueue()
		return 0, err
	}
	unlock, err := lockfile.Lock(ctx, r.lockPath("save"))
	unqueue() // the next change may now wait behind this save
	if err != nil {
		return 0, err
	}
	defer unlock()
	return r.saveLocked(ctx)
}

// WaitIdle waits until no save runs or waits to run, under the locks in
// lockDir. A save that starts later finds what it finds then.
func WaitIdle(ctx context.Context, lockDir string) error {
	for _, name := range []string{"queue", "save"} {
		unlock, err := lockfile.Lock(ctx, filepath.Join(lockDir, name+".lock"))
		if err != nil {
			return err
		}
		unlock()
	}
	return nil
}

// saveLocked saves; the caller holds the save lock.
func (r *Reviver) saveLocked(ctx context.Context) (SaveResult, error) {
	panes, err := r.readPanes(ctx)
	if errors.Is(err, errNoServer) || (err == nil && len(panes) == 0) {
		return NoServer, nil
	}
	if err != nil {
		return 0, err
	}
	if !panes[0].ready {
		return NotReady, nil
	}
	// Sessions in @skip_sessions (tmux-overlay's) are a cache that their
	// owner rebuilds; restoring them would start programs nobody asked for.
	// A server left with only those is treated as one with none: kill-server
	// closes sessions one by one, and the real ones may go first.
	skip := r.skipper(ctx)
	kept := panes[:0]
	for _, p := range panes {
		if !skip(p.session) {
			kept = append(kept, p)
		}
	}
	if panes = kept; len(panes) == 0 {
		return NoServer, nil
	}
	programs, err := r.programs(ctx)
	if errors.Is(err, errNoServer) {
		return NoServer, nil
	}
	if err != nil {
		return 0, err
	}
	if err := r.assignIDs(ctx, panes); err != nil {
		if errors.Is(err, errNoServer) {
			return NoServer, nil
		}
		return 0, err
	}
	snap := build(panes, programs)
	snap.Time = r.now()
	return r.commit(ctx, snap)
}

// commit writes snap unless it holds nothing new, or would lose panes
// during shutdown.
func (r *Reviver) commit(ctx context.Context, snap *snapshot.Snapshot) (SaveResult, error) {
	prev, e, ok, err := r.Store.Latest()
	if err != nil {
		return 0, err
	}
	replace, res := "", Written
	if ok {
		if snapshot.Equal(prev, snap) {
			return Unchanged, nil
		}
		// At shutdown panes and their programs die before tmux does: a save
		// then must not drop what a restore would bring back.
		lost, cmds := snapshot.Lost(prev, snap), snapshot.LostCommands(prev, snap)
		if len(lost)+len(cmds) > 0 && r.stopping(ctx) {
			r.logf("save skipped: shutting down, and it would lose %d of %d panes and %d commands", len(lost), prev.Panes(), len(cmds))
			return Stopping, nil
		}
		if snapshot.SameShape(prev, snap) {
			replace, res = e.Path, Replaced
		}
	}
	r.Store.Keep = r.intOption(ctx, OptSnapshots, snapshot.DefaultKeep)
	if _, err := r.Store.Write(snap, replace); err != nil {
		return 0, err
	}
	return res, nil
}

// Forget marks a session so that no save keeps it, then saves, so that the
// session is not restored even when killing it ends the server. prefix X
// runs it before kill-session.
func (r *Reviver) Forget(ctx context.Context, session string) error {
	if _, err := r.Tmux.Run(ctx, "set-option", "-t", "="+session+":", OptForgotten, "1"); err != nil {
		return err
	}
	unlock, err := lockfile.Lock(ctx, r.lockPath("save"))
	if err != nil {
		return err
	}
	defer unlock()
	res, err := r.saveLocked(ctx)
	if err != nil || res != NotReady {
		return err
	}
	// A server that was never restored does not save; drop the session from
	// the snapshot a restore would use.
	prev, _, ok, err := r.Store.Latest()
	if err != nil || !ok {
		return err
	}
	next, had := prev.Without(session)
	if !had {
		return nil
	}
	next.Time = r.now()
	_, err = r.Store.Write(next, "")
	return err
}
