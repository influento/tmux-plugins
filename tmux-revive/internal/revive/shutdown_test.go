package revive_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// stubLogind stands in for logind's tools; each notes its pid, and the
// command systemd-inhibit runs its own, in $HOME/pids. systemd-inhibit runs its
// command, then copies the scrollback folder as it is when the lock goes
// to $HOME/released-<pid>. busctl's waits end when the test writes the
// signal's value to $HOME/shutdown or $HOME/session-removed, and each
// value ends one wait; PreparingForShutdown reads $HOME/preparing.
func stubLogind(t *testing.T, e *env) {
	t.Helper()
	e.Stub(t, "systemd-inhibit", "#!/bin/sh\necho $$ >> \"$HOME/pids\"\n"+tmuxtest.RecordCall+`
while [ $# -gt 0 ]; do case $1 in --*) shift ;; *) break ;; esac; done
"$@" & child=$!
echo $child >> "$HOME/pids"
wait $child; rc=$?
if cp -r "$XDG_STATE_HOME/tmux-revive/scrollback" "$HOME/release.tmp.$$" 2>/dev/null; then
  mv "$HOME/release.tmp.$$" "$HOME/released-$$"
fi
exit $rc
`)
	e.Stub(t, "busctl", "#!/bin/sh\necho $$ >> \"$HOME/pids\"\n"+tmuxtest.RecordCall+`
take() {
  while ! mv "$HOME/$1" "$HOME/$1.$$" 2>/dev/null; do
    [ -d "$TMUXTEST_CALLS" ] || exit 1
    sleep 0.05
  done
  cat "$HOME/$1.$$"; rm -f "$HOME/$1.$$"
}
case "$*" in
  *" wait "*PrepareForShutdown) take shutdown ;;
  *" wait "*SessionRemoved) take session-removed ;;
  *PreparingForShutdown) cat "$HOME/preparing" 2>/dev/null || echo "b false" ;;
  *) exit 1 ;;
esac
`)
}

// signal writes a stub's input file in one step.
func (e *env) signal(t *testing.T, name, value string) {
	t.Helper()
	tmp := filepath.Join(e.Home, name+".tmp")
	if err := os.WriteFile(tmp, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(e.Home, name)); err != nil {
		t.Fatal(err)
	}
}

// waits counts busctl's waits for signal.
func (e *env) waits(signal string) int {
	n := 0
	for _, c := range e.Calls("busctl") {
		if slices.Contains(c.Args, "wait") && slices.Contains(c.Args, signal) {
			n++
		}
	}
	return n
}

func (e *env) waitWaits(t *testing.T, signal string, n int) {
	t.Helper()
	tmuxtest.WaitFor(t, 10*time.Second, "busctl wait "+signal, func() bool { return e.waits(signal) >= n })
}

// scrollbackHas reports whether a scrollback file in dir holds text.
func scrollbackHas(t *testing.T, dir, text string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err == nil && strings.Contains(string(b), text) {
			return true
		}
	}
	return false
}

// printAfterCapture prints a line in the session's pane, which no capture
// has seen: the scrollback timer is off.
func (e *env) printAfterCapture(t *testing.T, session string) string {
	t.Helper()
	pane := e.mustTmux(t, "display-message", "-p", "-t", session, "#{pane_id}")
	e.mustTmux(t, "send-keys", "-t", pane, "echo last-words-$((6*7))", "Enter")
	e.waitContent(t, pane, "last-words-42")
	return "last-words-42"
}

// A shutdown saves the scrollback under the delay lock, and the lock goes
// only after the files are written; a cancelled shutdown captures nothing.
// While a shutdown is under way no new lock is taken; once it is
// cancelled, one is.
func TestShutdownSavesTheScrollback(t *testing.T) {
	e := newEnv(t, "")
	stubLogind(t, e)
	e.start(t, "proj-a", e.mkdir(t, "proj-a"))
	e.waitWaits(t, "PrepareForShutdown", 1)
	inhibit := e.Calls("systemd-inhibit")
	if len(inhibit) != 1 || !slices.Contains(inhibit[0].Args, "--what=shutdown") || !slices.Contains(inhibit[0].Args, "--mode=delay") {
		t.Fatalf("want one delay lock on shutdown, got %+v", inhibit)
	}
	text := e.printAfterCapture(t, "proj-a")
	scrollback := filepath.Join(e.StateHome, "tmux-revive", "scrollback")

	e.signal(t, "shutdown", "b false")
	e.waitWaits(t, "PrepareForShutdown", 2)
	if scrollbackHas(t, scrollback, text) {
		t.Fatal("a cancelled shutdown captured the scrollback")
	}

	e.signal(t, "preparing", "b true")
	e.signal(t, "shutdown", "b true")
	var released []string
	tmuxtest.WaitFor(t, 10*time.Second, "the lock to go after a capture", func() bool {
		released, _ = filepath.Glob(filepath.Join(e.Home, "released-*"))
		return slices.ContainsFunc(released, func(d string) bool { return scrollbackHas(t, d, text) })
	})
	if !scrollbackHas(t, scrollback, text) {
		t.Fatal("the scrollback file lacks the last output")
	}
	time.Sleep(1500 * time.Millisecond)
	if n := len(e.Calls("systemd-inhibit")); n != 2 {
		t.Fatalf("%d locks taken while a shutdown is under way; want 2 (none new)", n)
	}
	e.signal(t, "preparing", "b false")
	tmuxtest.WaitFor(t, 10*time.Second, "a new lock after the cancel", func() bool {
		return len(e.Calls("systemd-inhibit")) == 3
	})
}

// The guard and everything it started end with the server: tmux sends its
// run-shell jobs SIGTERM as it exits. A guard that died of it left its
// lock, shutdown-wait and the waits running, the lock held for no server.
func TestShutdownGuardEndsWithItsServer(t *testing.T) {
	e := newEnv(t, "")
	stubLogind(t, e)
	e.start(t, "proj-a", e.mkdir(t, "proj-a"))
	e.waitWaits(t, "PrepareForShutdown", 1)
	e.waitWaits(t, "SessionRemoved", 1)
	e.mustTmux(t, "kill-server")
	data, err := os.ReadFile(filepath.Join(e.Home, "pids"))
	if err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(string(data))
	var left []string
	defer func() {
		for _, pid := range left {
			stat, _ := os.ReadFile(filepath.Join("/proc", pid, "stat")) // gone since: empty
			t.Logf("left running: %s", stat)
		}
	}()
	tmuxtest.WaitFor(t, 5*time.Second, "the guard's processes to end", func() bool {
		left = slices.DeleteFunc(slices.Clone(pids), ended)
		return len(left) == 0
	})
}

// ended reports whether the process pid has exited: gone, or a zombie
// (a container's first process may never reap the orphans it adopts).
func ended(pid string) bool {
	stat, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		return true
	}
	// The state follows the command, which is in parentheses.
	i := strings.LastIndex(string(stat), ") ")
	return i >= 0 && strings.HasPrefix(string(stat)[i+2:], "Z")
}
