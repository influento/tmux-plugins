package revive

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/internal/lockfile"
)

// fallbackSession is where a terminal lands on a server with no work in
// it.
const fallbackSession = "main"

// afterRestore is the time the steps after a restore get, whatever time
// the restore itself used.
const afterRestore = 30 * time.Second

// Attach is the terminal's entry point, run before a client attaches. It
// restores the newest snapshot once per server, before any shell exists,
// and returns the session the terminal should attach to.
//
// The rules are those of dotfiles' tmux-attach, which it replaces:
//   - a server is restored once: the first terminal marks it ready, and
//     later ones never restore again;
//   - a server holding live work is never restored onto; only one with no
//     sessions, or a single one-window main, counts as bare;
//   - terminals that open together restore once (a lock);
//   - the terminal lands on the session used last, never on one matching
//     @skip_sessions, and on main when there is nothing else.
func (r *Reviver) Attach(ctx context.Context) (string, error) {
	unlock, err := lockfile.Lock(ctx, r.lockPath("attach"))
	if err != nil {
		return "", err
	}
	defer unlock()

	var errs []error
	sessions, err := r.sessions(ctx)
	if err != nil {
		return "", err
	}
	target := ""
	if !r.isReady(ctx, sessions) {
		if bare(sessions) {
			rep, err := r.restoreLatest(ctx)
			errs = append(errs, err)
			target = rep.Target
			// Typing may have used up ctx. What is left must still run, or
			// the terminal lands on a new main beside the restored sessions.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), afterRestore)
			defer cancel()
		}
		if err := r.markReady(ctx); err != nil && !errors.Is(err, errNoServer) {
			errs = append(errs, err)
		}
		if sessions, err = r.sessions(ctx); err != nil {
			return "", errors.Join(append(errs, err)...)
		}
	}
	if target == "" {
		target = lastUsed(sessions, r.skipper(ctx))
	}
	if target != "" {
		return target, errors.Join(errs...)
	}
	if !hasSession(sessions, fallbackSession) {
		if _, err := r.Tmux.Run(ctx, "new-session", "-d", "-s", fallbackSession); err != nil {
			return "", errors.Join(append(errs, err)...)
		}
		if err := r.markReady(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return fallbackSession, errors.Join(errs...)
}

// restoreLatest restores the newest snapshot, if there is one with
// something in it.
func (r *Reviver) restoreLatest(ctx context.Context) (Report, error) {
	snap, e, ok, err := r.Store.Latest()
	if err != nil || !ok || len(snap.Sessions) == 0 {
		return Report{}, err
	}
	return r.Restore(ctx, snap, e)
}

// liveSession is one line of list-sessions.
type liveSession struct {
	name         string
	windows      int
	lastAttached int64
	activity     int64
}

// sessions lists the server's sessions; none when no server runs.
func (r *Reviver) sessions(ctx context.Context) ([]liveSession, error) {
	// session_last_attached is empty for a session never attached, every
	// session right after a restore, so the format puts 0 in its place.
	out, err := r.Tmux.Output(ctx, "list-sessions", "-F", strings.Join([]string{
		"#{session_name}", "#{session_windows}",
		"#{?session_last_attached,#{session_last_attached},0}",
		"#{?session_activity,#{session_activity},0}",
	}, sep))
	if err := noServer(err); errors.Is(err, errNoServer) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var ss []liveSession
	for _, line := range outLines(out) {
		f := strings.Split(line, sep)
		if len(f) != 4 {
			continue
		}
		ss = append(ss, liveSession{name: f[0], windows: atoi(f[1]), lastAttached: atoi64(f[2]), activity: atoi64(f[3])})
	}
	return ss, nil
}

// isReady reports whether the server was already restored, or judged
// live: then no terminal restores onto it again.
func (r *Reviver) isReady(ctx context.Context, sessions []liveSession) bool {
	if len(sessions) == 0 {
		return false
	}
	v, err := r.option(ctx, OptReady, "")
	return err == nil && v == "1"
}

// bare reports whether a restore may fill the server: it has no sessions,
// or only a one-window main that something started before the terminal.
// Anything else is live work.
func bare(sessions []liveSession) bool {
	for _, s := range sessions {
		if s.name != fallbackSession || s.windows > 1 {
			return false
		}
	}
	return true
}

func hasSession(sessions []liveSession, name string) bool {
	for _, s := range sessions {
		if s.name == name {
			return true
		}
	}
	return false
}

// lastUsed is the session attached to most recently, then the one active
// most recently, among those skip lets through.
func lastUsed(sessions []liveSession, skip func(string) bool) string {
	var best *liveSession
	for i := range sessions {
		s := &sessions[i]
		if skip(s.name) {
			continue
		}
		if best == nil || s.lastAttached > best.lastAttached ||
			(s.lastAttached == best.lastAttached && s.activity > best.activity) {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	return best.name
}
