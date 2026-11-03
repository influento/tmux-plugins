package lockfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "a.lock")
	unlock, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file: %v, %v; want mode 600", fi, err)
	}
	if _, ok, err := TryLock(path); ok || err != nil {
		t.Errorf("TryLock while held: ok=%v err=%v, want false, nil", ok, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock while held: %v, want the deadline", err)
	}

	got := make(chan error, 1)
	go func() {
		u, err := Lock(context.Background(), path)
		if err == nil {
			u()
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	unlock()
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("Lock after unlock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Lock never got the lock after unlock")
	}
	u, ok, err := TryLock(path)
	if !ok || err != nil {
		t.Fatalf("TryLock when free: ok=%v err=%v", ok, err)
	}
	u()
}

func TestLockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.lock")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := TryLock(path); err == nil {
		t.Error("TryLock followed a symlink")
	}
}
