package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A run an agent starts (claude -p from a script the agent ran) knows no
// parent; only the process tree tells, and only while both run. So every
// hook leaves a note: its agent process (pid and start time, which tell it
// from a later process with the same pid) runs this session. A hook of a
// run reads the note of the agent process above its own, and records that
// session as the run's parent in the run's token tally. A run of a run
// finds its parent the same way.

// procLineage is the nearest agent process above the calling process and
// the nearest agent process above that one, each as pid-start; "" for
// none.
func procLineage(names []string) (self, parent string) {
	return lineageFrom(os.Getppid(), names)
}

// lineageFrom is procLineage from process pid up.
func lineageFrom(pid int, names []string) (self, parent string) {
	pid = nearestAgentPid(pid, names)
	if pid <= 0 {
		return "", ""
	}
	p, err := readProc(pid)
	if err != nil {
		return "", ""
	}
	self = procKey(p)
	if up := nearestAgentPid(p.ppid, names); up > 0 {
		if q, err := readProc(up); err == nil {
			parent = procKey(q)
		}
	}
	return self, parent
}

// procKey names a process for its note.
func procKey(p proc) string {
	if p.start == "" {
		return ""
	}
	return strconv.Itoa(p.pid) + "-" + p.start
}

func (e *Env) lineage(names []string) (self, parent string) {
	if e.NotesDir == "" || len(names) == 0 {
		return "", ""
	}
	if e.Lineage != nil {
		return e.Lineage(names)
	}
	return procLineage(names)
}

// noteSession notes that agent process self runs session.
func (e *Env) noteSession(a Agent, session, self string) {
	if self == "" || session == "" || strings.ContainsAny(session, "/\n ") {
		return
	}
	p := filepath.Join(e.NotesDir, self)
	note := a.Name() + " " + session
	if data, err := os.ReadFile(p); err == nil && string(data) == note {
		return
	}
	if err := os.MkdirAll(e.NotesDir, 0o700); err != nil {
		e.logf("note %s: %v", self, err)
		return
	}
	f, err := os.CreateTemp(e.NotesDir, self+".tmp-*")
	if err != nil {
		e.logf("note %s: %v", self, err)
		return
	}
	_, werr := f.WriteString(note)
	if err := errors.Join(werr, f.Close()); err != nil {
		os.Remove(f.Name())
		e.logf("note %s: %v", self, err)
		return
	}
	if err := os.Rename(f.Name(), p); err != nil {
		os.Remove(f.Name())
		e.logf("note %s: %v", self, err)
	}
}

// noteParent records, in the token tally of a run's session, the session
// that agent process parent runs, unless the tally has a parent already.
func (e *Env) noteParent(ctx context.Context, a Agent, session, parent string) {
	if e.Store == nil || session == "" || parent == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(e.NotesDir, parent))
	if err != nil {
		return // the parent's agent left no note: no hook of it ran yet
	}
	pAgent, pSession, ok := strings.Cut(string(data), " ")
	if !ok || pSession == session {
		return
	}
	u, _, err := e.Store.LoadUsage(a.Name(), session)
	if err != nil && !isDamaged(err) {
		e.logf("parent of %s: %v", session, err)
		return
	}
	if u.Parent != "" {
		return
	}
	defer e.lock(ctx)()
	if u, err = e.loadUsage(a, session); err != nil {
		e.logf("parent of %s: %v", session, err)
		return
	}
	if u.Parent != "" {
		return
	}
	u.Parent, u.ParentAgent = pSession, pAgent
	if err := e.Store.SaveUsage(u); err != nil {
		e.logf("parent of %s: %v", session, err)
	}
}

// PruneNotes removes the notes of agent processes that no longer run.
func PruneNotes(dir string) error {
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, de := range des {
		key, _, _ := strings.Cut(de.Name(), ".tmp")
		pid, start, _ := strings.Cut(key, "-")
		if n, err := strconv.Atoi(pid); err == nil {
			if p, err := readProc(n); err == nil && p.start == start {
				continue // it runs: its note, or a write in flight
			}
		}
		if err := os.Remove(filepath.Join(dir, de.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
