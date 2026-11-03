// Package lockfile takes exclusive locks on files with flock(2). A lock
// goes with the process, so a holder that crashed never blocks the next.
package lockfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// pollEvery is how often Lock tries again while another process holds the
// lock.
const pollEvery = 5 * time.Millisecond

// Lock takes the lock in the file at path, waiting until ctx ends.
func Lock(ctx context.Context, path string) (unlock func(), err error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		ok, err := try(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if ok {
			return func() { f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, ctx.Err())
		case <-tick.C:
		}
	}
}

// TryLock takes the lock in the file at path if it is free. ok is false
// when another process holds it.
func TryLock(path string) (unlock func(), ok bool, err error) {
	f, err := open(path)
	if err != nil {
		return nil, false, err
	}
	ok, err = try(f)
	if err != nil || !ok {
		f.Close()
		if err != nil {
			err = fmt.Errorf("lock %s: %w", path, err)
		}
		return nil, false, err
	}
	return func() { f.Close() }, true, nil
}

// try takes the lock on f without waiting. ok is false when another
// process holds it.
func try(f *os.File) (ok bool, err error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		}
		return false, err
	}
}

// open opens a lock file, 0600 in a 0700 folder, and never through a
// symlink. Go opens it close-on-exec, so a process started while the lock
// is held, such as a tmux server, never inherits it and holds it for good.
func open(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
}
