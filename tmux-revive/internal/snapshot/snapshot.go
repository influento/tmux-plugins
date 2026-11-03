// Package snapshot is tmux-revive's saved state: what a snapshot holds,
// how two snapshots compare, and the files they live in.
package snapshot

import (
	"encoding/json"
	"slices"
	"time"
)

// Version is the snapshot format. A snapshot of another version is not
// read.
const Version = 1

// Snapshot is every session of one tmux server at one moment.
type Snapshot struct {
	Version  int       `json:"version"`
	Time     time.Time `json:"time"`
	Sessions []Session `json:"sessions"`
}

// Session is one tmux session.
type Session struct {
	Name string `json:"name"`
	// LastAttached is when a client last attached, unix seconds; 0 when
	// none ever did. attach lands on the session used last.
	LastAttached int64    `json:"last_attached,omitempty"`
	Windows      []Window `json:"windows"`
}

// Window is one window of a session.
type Window struct {
	Index int    `json:"index"`
	ID    string `json:"id"` // @revive_id, kept across restarts
	// Name is kept only when automatic-rename is off: an automatic name
	// changes with every command, and tmux sets it again by itself.
	Name       string `json:"name,omitempty"`
	AutoRename bool   `json:"auto_rename"`
	Layout     string `json:"layout"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Active     bool   `json:"active,omitempty"`
	Zoomed     bool   `json:"zoomed,omitempty"`
	Panes      []Pane `json:"panes"`
}

// Pane is one pane of a window.
type Pane struct {
	Index  int    `json:"index"`
	ID     string `json:"id"` // @revive_pane, names its scrollback file
	Cwd    string `json:"cwd"`
	Active bool   `json:"active,omitempty"`
	// Program is the command line of a program restore starts again, such
	// as nvim, when the pane ran one.
	Program []string `json:"program,omitempty"`
	// Command is the pane's @revive_cmd: what restore types into the
	// pane's shell. It wins over Program.
	Command string `json:"command,omitempty"`
}

// Panes counts the panes in the snapshot.
func (s *Snapshot) Panes() int {
	n := 0
	for _, sess := range s.Sessions {
		for _, w := range sess.Windows {
			n += len(w.Panes)
		}
	}
	return n
}

// Windows counts the windows in the snapshot.
func (s *Snapshot) Windows() int {
	n := 0
	for _, sess := range s.Sessions {
		n += len(sess.Windows)
	}
	return n
}

// Equal reports whether a and b hold the same state, whenever they were
// taken.
func Equal(a, b *Snapshot) bool {
	ja, errA := json.Marshal(a.Sessions)
	jb, errB := json.Marshal(b.Sessions)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// SameShape reports whether a and b hold the same sessions, windows and
// panes, by name and id, so that they differ only in details: layouts,
// folders, programs, names, the active pane. A save that changes only
// details replaces the newest snapshot instead of adding one, so that a
// drag-resize or a run of cd commands cannot push older snapshots out.
func SameShape(a, b *Snapshot) bool {
	return slices.Equal(shape(a), shape(b))
}

func shape(s *Snapshot) []string {
	var keys []string
	for _, sess := range s.Sessions {
		for _, w := range sess.Windows {
			for _, p := range w.Panes {
				keys = append(keys, sess.Name+"\x00"+w.ID+"\x00"+p.ID)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

// Lost returns the ids of the panes in prev that next no longer has.
func Lost(prev, next *Snapshot) []string {
	have := map[string]bool{}
	for _, id := range paneIDs(next) {
		have[id] = true
	}
	var lost []string
	for _, id := range paneIDs(prev) {
		if !have[id] {
			lost = append(lost, id)
		}
	}
	return lost
}

func paneIDs(s *Snapshot) []string {
	var ids []string
	for _, sess := range s.Sessions {
		for _, w := range sess.Windows {
			for _, p := range w.Panes {
				ids = append(ids, p.ID)
			}
		}
	}
	return ids
}

// Without returns a copy of s without the named session, and whether s had
// it.
func (s *Snapshot) Without(name string) (*Snapshot, bool) {
	out := *s
	out.Sessions = slices.DeleteFunc(slices.Clone(s.Sessions), func(sess Session) bool { return sess.Name == name })
	return &out, len(out.Sessions) != len(s.Sessions)
}

// LastUsed is the session a client attached to last, among those keep
// accepts; "" when none was ever attached.
func (s *Snapshot) LastUsed(keep func(name string) bool) string {
	best, bestAt := "", int64(0)
	for _, sess := range s.Sessions {
		if sess.LastAttached > bestAt && keep(sess.Name) {
			best, bestAt = sess.Name, sess.LastAttached
		}
	}
	return best
}
