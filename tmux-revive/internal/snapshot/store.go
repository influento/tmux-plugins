package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// DefaultKeep is how many snapshots a Store keeps when Keep is zero.
const DefaultKeep = 20

// logLimit is the size at which the log moves to log.old.
const logLimit = 256 << 10

// nameLayout names a snapshot file after its time, so that names sort by
// time.
const nameLayout = "20060102T150405.000000000Z"

// Store is tmux-revive's folder: the snapshots, one scrollback file per
// pane, and a log of restores and skipped saves. Scrollback can hold
// tokens and keys, so the folder is 0700 and every file 0600.
type Store struct {
	Dir  string
	Keep int
}

// Entry is one snapshot file.
type Entry struct {
	Path string
	Time time.Time
}

func (s *Store) snapDir() string   { return filepath.Join(s.Dir, "snapshots") }
func (s *Store) scrollDir() string { return filepath.Join(s.Dir, "scrollback") }

func (s *Store) keep() int {
	if s.Keep > 0 {
		return s.Keep
	}
	return DefaultKeep
}

// Entries lists the snapshot files, oldest first.
func (s *Store) Entries() ([]Entry, error) {
	des, err := os.ReadDir(s.snapDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, de := range des {
		name, ok := strings.CutSuffix(de.Name(), ".json")
		if !ok || !de.Type().IsRegular() {
			continue
		}
		t, err := time.Parse(nameLayout, name)
		if err != nil {
			continue
		}
		es = append(es, Entry{Path: filepath.Join(s.snapDir(), de.Name()), Time: t})
	}
	slices.SortFunc(es, func(a, b Entry) int { return a.Time.Compare(b.Time) })
	return es, nil
}

// Load reads one snapshot file.
func (s *Store) Load(path string) (*Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if snap.Version != Version {
		return nil, fmt.Errorf("%s: format %d, want %d", filepath.Base(path), snap.Version, Version)
	}
	return &snap, nil
}

// Latest is the newest snapshot that can be read. ok is false when there
// is none.
func (s *Store) Latest() (snap *Snapshot, e Entry, ok bool, err error) {
	return s.newest(func(Entry) bool { return true })
}

// At is the newest snapshot taken at or before t.
func (s *Store) At(t time.Time) (snap *Snapshot, e Entry, ok bool, err error) {
	return s.newest(func(e Entry) bool { return !e.Time.After(t) })
}

// newest returns the newest readable snapshot whose entry passes keep. A
// file that cannot be read, such as one cut short by a crash, is passed
// over.
func (s *Store) newest(keep func(Entry) bool) (*Snapshot, Entry, bool, error) {
	es, err := s.Entries()
	if err != nil {
		return nil, Entry{}, false, err
	}
	for i := len(es) - 1; i >= 0; i-- {
		if !keep(es[i]) {
			continue
		}
		if snap, err := s.Load(es[i].Path); err == nil {
			return snap, es[i], true, nil
		}
	}
	return nil, Entry{}, false, nil
}

// Write saves snap as a new snapshot file, removes the file replace names
// (if not ""), and keeps only the newest Keep files. The file appears
// whole or not at all.
func (s *Store) Write(snap *Snapshot, replace string) (Entry, error) {
	dir := s.snapDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Entry{}, err
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return Entry{}, err
	}
	t := snap.Time.UTC()
	path := filepath.Join(dir, t.Format(nameLayout)+".json")
	for exists(path) && path != replace {
		t = t.Add(time.Nanosecond)
		path = filepath.Join(dir, t.Format(nameLayout)+".json")
	}
	if err := writeAtomic(dir, path, append(b, '\n')); err != nil {
		return Entry{}, err
	}
	if replace != "" && replace != path {
		if err := os.Remove(replace); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Entry{}, err
		}
	}
	return Entry{Path: path, Time: t}, s.prune()
}

func (s *Store) prune() error {
	es, err := s.Entries()
	if err != nil {
		return err
	}
	errs := []error{removeStaleTemps(s.snapDir())}
	for len(es) > s.keep() {
		if err := os.Remove(es[0].Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		es = es[1:]
	}
	return errors.Join(errs...)
}

// validID is a pane id safe as a file name.
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ScrollbackPath is the scrollback file of the pane with this id.
func (s *Store) ScrollbackPath(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("invalid pane id %q", id)
	}
	return filepath.Join(s.scrollDir(), id+".txt"), nil
}

// WriteScrollback replaces the pane's scrollback file with data, unless it
// already holds exactly that.
func (s *Store) WriteScrollback(id string, data []byte) (changed bool, err error) {
	path, err := s.ScrollbackPath(id)
	if err != nil {
		return false, err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(s.scrollDir(), 0o700); err != nil {
		return false, err
	}
	return true, writeAtomic(s.scrollDir(), path, data)
}

// PruneScrollback removes the scrollback files of panes that are neither
// live nor in a kept snapshot.
func (s *Store) PruneScrollback(live map[string]bool) error {
	keep, err := s.referenced()
	if err != nil {
		return err
	}
	des, err := os.ReadDir(s.scrollDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	errs := []error{removeStaleTemps(s.scrollDir())}
	for _, de := range des {
		id, ok := strings.CutSuffix(de.Name(), ".txt")
		if !ok || keep[id] || live[id] {
			continue
		}
		if err := os.Remove(filepath.Join(s.scrollDir(), de.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// referenced returns the pane ids in every snapshot kept.
func (s *Store) referenced() (map[string]bool, error) {
	es, err := s.Entries()
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, e := range es {
		snap, err := s.Load(e.Path)
		if err != nil {
			continue
		}
		for _, id := range paneIDs(snap) {
			ids[id] = true
		}
	}
	return ids, nil
}

// Logf appends a line to the log: restores, skipped saves, commands that
// could not be typed. It is where to look when something did not come
// back. Logging is best effort: a failure here never fails the caller.
func (s *Store) Logf(now time.Time, format string, args ...any) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(s.Dir, "log")
	if fi, err := os.Lstat(path); err == nil && fi.Size() > logLimit {
		_ = os.Rename(path, path+".old") // best effort, as above
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", now.Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// staleTemp is the age at which a temporary file is one a crash left
// behind: a write keeps its own for well under a second.
const staleTemp = time.Hour

// removeStaleTemps removes the temporary files that writes cut short by a
// crash left in dir.
func removeStaleTemps(dir string) error {
	des, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, de := range des {
		if !strings.HasPrefix(de.Name(), ".tmp-") {
			continue
		}
		fi, err := de.Info()
		if err != nil || time.Since(fi.ModTime()) < staleTemp {
			continue // gone already, or a write in progress
		}
		if err := os.Remove(filepath.Join(dir, de.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// writeAtomic writes data to a temporary file in dir and renames it to
// path, so that a reader or a crash never sees half a file.
func writeAtomic(dir, path string, data []byte) error {
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // gone after the rename; cleans up on failure
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
