package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateMergesAndKeepsMode(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	const id = "11111111-2222-4333-8444-555555555555"
	if err := s.Update("agent-a", id, func(r *Session) { r.State, r.Cwd = "working", "/src/proj-a" }); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("agent-a", id, func(r *Session) { r.Title = "fix the tests" }); err != nil {
		t.Fatal(err)
	}
	r, ok, err := s.Load("agent-a", id)
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if r.State != "working" || r.Cwd != "/src/proj-a" || r.Title != "fix the tests" || r.Agent != "agent-a" || r.ID != id || r.Updated == 0 {
		t.Errorf("record = %+v", r)
	}
	for _, p := range []string{filepath.Join(s.Dir, "sessions"), filepath.Join(s.Dir, "sessions", "agent-a")} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v (%v), want 0700", p, fi.Mode().Perm(), err)
		}
	}
	fi, err := os.Stat(filepath.Join(s.Dir, "sessions", "agent-a", id+".json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode %v (%v), want 0600", fi.Mode().Perm(), err)
	}
}

func TestLoadMissing(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if _, ok, err := s.Load("agent-a", "none"); ok || err != nil {
		t.Errorf("Load of a missing record: ok=%v err=%v", ok, err)
	}
}

// A session id from a hook payload names a file: anything that could leave
// the folder is refused.
func TestNamesAreChecked(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	for _, bad := range []string{"", "..", "../x", "a/b", ".hidden", "a b"} {
		if err := s.Update("agent-a", bad, func(*Session) {}); err == nil {
			t.Errorf("Update(%q) succeeded", bad)
		}
		if err := s.Update(bad, "id", func(*Session) {}); err == nil {
			t.Errorf("Update with agent %q succeeded", bad)
		}
	}
}

// A damaged record is replaced, not a reason to stop recording.
func TestUpdateReplacesDamagedRecord(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	p := filepath.Join(s.Dir, "sessions", "agent-a", "id.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("agent-a", "id", func(r *Session) { r.State = "done" }); err != nil {
		t.Fatal(err)
	}
	if r, _, err := s.Load("agent-a", "id"); err != nil || r.State != "done" {
		t.Errorf("record = %+v (%v)", r, err)
	}
}

func TestLockIsExclusiveAndTimesOut(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run", "lock")
	unlock, err := Lock(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	// flock locks belong to the open file, so a second open in the same
	// process waits like another process would.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock: %v, want a deadline error", err)
	}
	unlock()
	unlock2, err := Lock(context.Background(), p)
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	unlock2()
}

func TestDirs(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := StateDir(env(map[string]string{"XDG_STATE_HOME": "/st", "HOME": "/h"})); got != "/st/tmux-agent" {
		t.Errorf("StateDir = %q", got)
	}
	if got := StateDir(env(map[string]string{"XDG_STATE_HOME": "rel", "HOME": "/h"})); got != "/h/.local/state/tmux-agent" {
		t.Errorf("StateDir with a relative XDG_STATE_HOME = %q", got)
	}
	if got := RuntimeDir(env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1"})); got != "/run/user/1/tmux-agent" {
		t.Errorf("RuntimeDir = %q", got)
	}
}
