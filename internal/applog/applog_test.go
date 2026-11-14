package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Lines append; past the limit the file moves to .old and a new one
// starts; a symlink in the file's place is not written through.
func TestAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "log")
	Append(path, 10, "one")
	Append(path, 10, "two\n")
	if got := read(t, path); got != "one\ntwo\n" {
		t.Fatalf("log = %q", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v, %v; want 0600", fi.Mode(), err)
	}
	Append(path, 4, "three")
	if got, old := read(t, path), read(t, path+".old"); got != "three\n" || old != "one\ntwo\n" {
		t.Fatalf("log = %q, old = %q", got, old)
	}

	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	Append(link, 1<<20, "leak")
	if got := read(t, target); strings.Contains(got, "leak") {
		t.Error("wrote through a symlink")
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
