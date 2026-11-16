package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/influento/tmux-plugins/tmux-revive/internal/logind"
	"github.com/influento/tmux-plugins/tmux-revive/internal/revive"
)

// lockWhy is what `systemd-inhibit --list` shows for the guard's lock.
const lockWhy = "Saving the scrollback of tmux panes"

// shutdownGuard closes the gap of up to one scrollback interval that a
// reboot, a poweroff or a logout would lose: it saves and captures as the
// shutdown starts, under a logind delay lock, and as the user's last login
// session ends (the user's service manager, which runs tmux, stops some
// seconds later). One guard runs per server, for as long as the server
// lives; without systemd's tools it does nothing.
func (a *app) shutdownGuard() error {
	if a.r.Self == "" || !logind.Available() {
		return nil
	}
	unlock, alive, ok, err := a.perServer("shutdown")
	if err != nil || !ok {
		return err
	}
	defer unlock()
	// tmux sends its run-shell jobs SIGTERM as it exits: the guard stops
	// what it started, or the lock stays held for no server.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	var wg sync.WaitGroup
	wg.Go(func() { a.holdShutdownLock(ctx) })
	wg.Go(func() { a.watchLogout(ctx) })
	for alive() && sleepCtx(ctx, time.Second) {
	}
	stop()
	wg.Wait()
	return nil
}

// holdShutdownLock keeps a delay lock on shutdown, with shutdown-wait
// running under it. When a shutdown starts, shutdown-wait saves and
// captures, then ends, which lets the shutdown go on; a new lock waits
// until a shutdown is no longer under way (it was cancelled).
func (a *app) holdShutdownLock(ctx context.Context) {
	var b backoff
	for {
		started := time.Now()
		out, err := logind.Inhibit(ctx, lockWhy, a.r.Self, "shutdown-wait").CombinedOutput()
		if ctx.Err() != nil {
			return
		}
		for {
			preparing, err := logind.PreparingForShutdown(ctx)
			if err != nil || !preparing || !sleepCtx(ctx, time.Second) {
				break
			}
		}
		// A lock that ends at once is no lock: systemd-inhibit or busctl
		// failed (busctl before systemd 257 has no wait).
		if err != nil {
			a.logFailure(&b, "shutdown lock: %v: %s", err, bytes.TrimSpace(out))
		} else if time.Since(started) < time.Second {
			a.logFailure(&b, "shutdown lock: ended at once: %s", bytes.TrimSpace(out))
		} else {
			b.reset()
		}
		if !sleepCtx(ctx, b.delay) {
			return
		}
	}
}

// watchLogout saves and captures when a login session ends and the user
// has none left. At a shutdown the lock has done it already.
func (a *app) watchLogout(ctx context.Context) {
	var b backoff
	for {
		if err := logind.WaitSessionRemoved(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			a.logFailure(&b, "logout watch: %v", err)
			if !sleepCtx(ctx, b.delay) {
				return
			}
			continue
		}
		b.reset()
		state, err := logind.UserState(ctx, os.Getuid())
		if err != nil {
			a.logFailure(&b, "logout watch: %v", err)
			continue
		}
		if state == logind.UserClosing && !revive.SystemdStopping(ctx) {
			a.store.Logf(time.Now(), "logout: saving the scrollback")
			a.saveAndCapture(ctx, "logout")
			a.store.Logf(time.Now(), "logout: scrollback saved")
		}
	}
}

// shutdownWait runs under the guard's delay lock: it waits for logind to
// say a shutdown starts, then saves and captures, and ends, which releases
// the lock. A cancelled shutdown ends it with nothing done.
func (a *app) shutdownWait() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	starting, err := logind.WaitShutdown(ctx)
	if err != nil || !starting {
		return err
	}
	a.store.Logf(time.Now(), "shutdown: saving the scrollback")
	a.saveAndCapture(ctx, "shutdown")
	a.store.Logf(time.Now(), "shutdown: scrollback saved")
	return nil
}

// backoff is the wait before a failed step runs again: 1 s, doubling to
// 10 min. It logs a failure only when its text changes, so a step that
// keeps failing writes one line.
type backoff struct {
	delay time.Duration
	last  string
}

func (b *backoff) reset() { b.delay, b.last = 0, "" }

func (a *app) logFailure(b *backoff, format string, args ...any) {
	b.delay = min(max(2*b.delay, time.Second), 10*time.Minute)
	if line := fmt.Sprintf(format, args...); line != b.last {
		b.last = line
		a.store.Logf(time.Now(), "%s", line)
	}
}

// sleepCtx waits d, and reports false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
