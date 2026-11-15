package store

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// An open task is kept until its end, private to the user, and listed by
// source and id; its end deletes it.
func TestTasks(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	for _, tk := range []Task{
		{Source: "tool-b", ID: "item-1", Title: "one"},
		{Source: "tool-a", ID: "item-2", Title: "two", Started: 42},
		{Source: "tool-a", ID: "item-1"},
	} {
		if err := s.SaveTask(tk); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(filepath.Join(s.Dir, "tasks", "tool-a", "item-2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	list, err := s.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, tk := range list {
		keys = append(keys, tk.Key())
	}
	if want := []string{"tool-a/item-1", "tool-a/item-2", "tool-b/item-1"}; !slices.Equal(keys, want) {
		t.Errorf("tasks %v, want %v", keys, want)
	}
	if tk, ok, err := s.LoadTask("tool-a", "item-2"); err != nil || !ok || tk.Title != "two" || tk.Started != 42 {
		t.Errorf("LoadTask = %+v, %v, %v", tk, ok, err)
	}
	if ok, err := s.DeleteTask("tool-a", "item-2"); err != nil || !ok {
		t.Errorf("DeleteTask = %v, %v; want true", ok, err)
	}
	if ok, err := s.DeleteTask("tool-a", "item-2"); err != nil || ok {
		t.Errorf("DeleteTask of a gone task = %v, %v; want false, nil", ok, err)
	}
	if _, _, err := s.LoadTask("../x", "item-1"); err == nil {
		t.Error("LoadTask took a source that leaves the folder")
	}
}

func TestParseTaskKey(t *testing.T) {
	for key, want := range map[string]bool{
		"tool-a/item-1": true, "tool-a": false, "/item-1": false, "tool-a/../x": false, "": false,
	} {
		if _, _, ok := ParseTaskKey(key); ok != want {
			t.Errorf("ParseTaskKey(%q) ok = %v, want %v", key, ok, want)
		}
	}
}
