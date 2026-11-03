// Package store keeps what tmux-agent must remember across a tmux restart:
// one record per agent session, keyed by the agent and its session id, so a
// resumed conversation finds its flag and its window title again.
//
// Live state stays in tmux options and goes with the server. The records
// live in <state>/sessions/<agent>/<id>.json, mode 0600 in 0700 folders. A
// record is never deleted: an ended session is marked with how it ended.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

// Session is what is kept about one agent session.
type Session struct {
	Agent      string `json:"agent"`
	ID         string `json:"session_id"`
	Transcript string `json:"transcript,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	State      string `json:"state,omitempty"`  // the last flag; empty for idle
	Reason     string `json:"reason,omitempty"` // the flag's reason
	Title      string `json:"title,omitempty"`  // the window title it set
	Pane       string `json:"pane,omitempty"`   // the pane it last ran in
	Started    int64  `json:"started,omitempty"`
	Updated    int64  `json:"updated,omitempty"`
	Ended      int64  `json:"ended,omitempty"`
	End        string `json:"end,omitempty"`  // how it ended: exited, killed, unknown
	Exit       string `json:"exit,omitempty"` // exit code, or the signal that killed it
}

// Store is the folder that holds the records.
type Store struct {
	Dir string
}

// StateDir is tmux-agent's folder under $XDG_STATE_HOME.
func StateDir(getenv func(string) string) string {
	if d := getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "tmux-agent")
	}
	return filepath.Join(getenv("HOME"), ".local", "state", "tmux-agent")
}

// RuntimeDir is tmux-agent's folder for locks: under $XDG_RUNTIME_DIR, else
// a per-user folder in the temp directory.
func RuntimeDir(getenv func(string) string) string {
	if d := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(d) {
		return filepath.Join(d, "tmux-agent")
	}
	return filepath.Join(os.TempDir(), "tmux-agent-"+strconv.Itoa(os.Getuid()))
}

// validName is an agent name or a session id safe as a file name.
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidName reports whether s can name a record.
func ValidName(s string) bool { return validName.MatchString(s) }

func (s *Store) path(agent, id string) (string, error) {
	if !ValidName(agent) || !ValidName(id) {
		return "", fmt.Errorf("store: invalid record name %q/%q", agent, id)
	}
	return filepath.Join(s.Dir, "sessions", agent, id+".json"), nil
}

// Load reads a record. ok is false when there is none.
func (s *Store) Load(agent, id string) (sess Session, ok bool, err error) {
	p, err := s.path(agent, id)
	if err != nil {
		return Session{}, false, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	if err := json.Unmarshal(data, &sess); err != nil {
		return Session{}, false, fmt.Errorf("store: %s: %w", p, err)
	}
	return sess, true, nil
}

// Update reads a record, or starts one, applies fn and writes it back. The
// caller holds the lock, so two updates never interleave.
func (s *Store) Update(agent, id string, fn func(*Session)) error {
	sess, _, err := s.Load(agent, id)
	if err != nil {
		if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
			return err
		}
		sess = Session{} // a damaged record is replaced
	}
	sess.Agent, sess.ID = agent, id
	fn(&sess)
	sess.Updated = time.Now().Unix()
	return s.Save(sess)
}

// Save writes a record whole. The write is atomic: a reader sees the old
// record or the new one.
func (s *Store) Save(sess Session) error {
	p, err := s.path(sess.Agent, sess.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// Lock takes the exclusive lock in file path, waiting until ctx ends. The
// lock goes with the process, so a crashed holder never blocks the next.
func Lock(ctx context.Context, path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, ctx.Err())
		case <-tick.C:
		}
	}
}
