package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Task is an open task: a unit of work a tool (its source) runs agents
// for, such as workbench's item. Its windows are the panes bound to it
// (the pane option the agent layer sets); the record keeps only what a
// restart needs to put the task back on them. It is deleted at the task's
// end: tmux-agent keeps no history of tasks.
type Task struct {
	Source  string `json:"source"`
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Started int64  `json:"started,omitempty"` // unix seconds
}

// Key is the task's name in a pane's binding: source/id.
func (t Task) Key() string { return t.Source + "/" + t.ID }

// ParseTaskKey splits a binding into its source and id; ok is false for
// one that names no valid task.
func ParseTaskKey(key string) (source, id string, ok bool) {
	source, id, ok = strings.Cut(key, "/")
	return source, id, ok && ValidName(source) && ValidName(id)
}

func (s *Store) taskPath(source, id string) (string, error) {
	if !ValidName(source) || !ValidName(id) {
		return "", errors.New("store: invalid task name " + source + "/" + id)
	}
	return filepath.Join(s.Dir, "tasks", source, id+".json"), nil
}

// LoadTask reads an open task; ok is false when there is none.
func (s *Store) LoadTask(source, id string) (t Task, ok bool, err error) {
	p, err := s.taskPath(source, id)
	if err != nil {
		return Task{}, false, err
	}
	ok, err = load(p, &t)
	if err != nil || !ok {
		return Task{}, false, err
	}
	return t, true, nil
}

// SaveTask writes a task whole.
func (s *Store) SaveTask(t Task) error {
	p, err := s.taskPath(t.Source, t.ID)
	if err != nil {
		return err
	}
	return writeJSON(p, t)
}

// DeleteTask removes a task; ok is false when there was none.
func (s *Store) DeleteTask(source, id string) (ok bool, err error) {
	p, err := s.taskPath(source, id)
	if err != nil {
		return false, err
	}
	err = os.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Tasks lists the open tasks by source, then id. A record it cannot read
// is left out.
func (s *Store) Tasks() ([]Task, error) {
	root := filepath.Join(s.Dir, "tasks")
	sources, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Task
	for _, src := range sources {
		if !src.IsDir() || !ValidName(src.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, src.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			id, ok := strings.CutSuffix(f.Name(), ".json")
			if !ok || !ValidName(id) {
				continue
			}
			if t, ok, err := s.LoadTask(src.Name(), id); err == nil && ok {
				out = append(out, t)
			}
		}
	}
	slices.SortFunc(out, func(a, b Task) int { return strings.Compare(a.Key(), b.Key()) })
	return out, nil
}
