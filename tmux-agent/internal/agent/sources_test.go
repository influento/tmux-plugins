package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// changed waits for the file watch to report path; got reports another.
func changed(t *testing.T, w *fileWatch, path string) {
	t.Helper()
	select {
	case got := <-w.changes:
		if got != path {
			t.Fatalf("change of %q, want %q", got, path)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no change of %q", path)
	}
}

// drain takes what the file watch reports until it is quiet: one write
// can fire a watch more than once.
func drain(w *fileWatch) {
	for {
		select {
		case <-w.changes:
		case <-time.After(100 * time.Millisecond):
			return
		}
	}
}

// quiet checks the file watch reports nothing for a moment.
func quiet(t *testing.T, w *fileWatch) {
	t.Helper()
	select {
	case got := <-w.changes:
		t.Fatalf("change of %q, want none", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A file watch reports writes to a file, entries of a folder that pass its
// filter, a file that comes after a set and a file put in place of one.
func TestFileWatch(t *testing.T) {
	dir := t.TempDir()
	file, folder := filepath.Join(dir, "t.jsonl"), filepath.Join(dir, "git")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := newFileWatch()
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	set := func() {
		w.set(map[string]func(string) bool{file: nil, folder: func(name string) bool { return name == "HEAD" }})
	}
	set()
	quiet(t, w)

	write(t, file, "one") // not there at the set: none
	quiet(t, w)
	set()
	write(t, file, "two")
	changed(t, w, file)
	drain(w)

	write(t, filepath.Join(folder, "index"), "x")
	quiet(t, w)
	write(t, filepath.Join(folder, "HEAD"), "ref: refs/heads/b")
	changed(t, w, folder)
	drain(w)

	// A rename into place, as git and editors write.
	write(t, file+".new", "three")
	if err := os.Rename(file+".new", file); err != nil {
		t.Fatal(err)
	}
	changed(t, w, file) // the old file's watch ends
	drain(w)
	set()
	write(t, file, "four")
	changed(t, w, file)

	w.set(nil)
	drain(w)
	write(t, file, "five")
	quiet(t, w)
}

// An exit watch reports a process that ends, and one gone already; one
// taken out of the watch is not reported.
func TestExitWatch(t *testing.T) {
	start := func() *exec.Cmd {
		t.Helper()
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	exited := func(x *exitWatch, pid int) {
		t.Helper()
		select {
		case got := <-x.exited:
			if got != pid {
				t.Fatalf("exit of %d, want %d", got, pid)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no exit of %d", pid)
		}
	}
	x := newExitWatch()
	defer x.close()

	a, b := start(), start()
	x.set([]int{a.Process.Pid, b.Process.Pid})
	_ = a.Process.Kill()
	_ = a.Wait()
	exited(x, a.Process.Pid)

	x.set(nil)
	_ = b.Process.Kill()
	_ = b.Wait()
	select {
	case got := <-x.exited:
		t.Fatalf("exit of %d after it left the watch", got)
	case <-time.After(100 * time.Millisecond):
	}

	x.set([]int{b.Process.Pid}) // reaped: no such process
	exited(x, b.Process.Pid)
}

// A pane has one watcher: a second gets ErrWatched while the first lives,
// and the socket after it goes. Wakes reach the watcher.
func TestListenWakes(t *testing.T) {
	dir, err := os.MkdirTemp("", "tw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	getenv := func(k string) string {
		return map[string]string{"TMUX": "/s,4242,0", "XDG_RUNTIME_DIR": dir}[k]
	}
	e := &Env{Getenv: getenv, Pane: "%7"}
	path := watchSocket(getenv, "%7")
	if path != filepath.Join(dir, "tmux-agent", "watch-4242-7.sock") {
		t.Fatalf("socket %q", path)
	}
	if e.wake("%7", wakePing) {
		t.Fatal("a wake with no watcher was taken")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, path, "") // left by a watcher that crashed

	conn, release, err := listenWakes(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenWakes(path); !errors.Is(err, ErrWatched) {
		t.Fatalf("second watcher: %v, want ErrWatched", err)
	}
	if !e.wake("%7", wakeScan) {
		t.Fatal("the wake was not taken")
	}
	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != wakeScan {
		t.Fatalf("read %q, %v; want %q", buf[:n], err, wakeScan)
	}

	release()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket after release: %v", err)
	}
	if e.wake("%7", wakePing) {
		t.Error("a wake was taken after the watcher went")
	}
	_, release, err = listenWakes(path)
	if err != nil {
		t.Fatalf("watcher after the first went: %v", err)
	}
	release()
}
