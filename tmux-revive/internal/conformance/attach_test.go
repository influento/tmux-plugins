package conformance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// Driver runs one implementation of the terminal's entry point.
type Driver interface {
	Name() string
	Setup(t *testing.T, sb *tmuxtest.Sandbox)
	// Saved makes the next restore bring back these sessions.
	Saved(t *testing.T, sb *tmuxtest.Sandbox, sessions ...string)
	// Command is the shell command the terminal runs on alt+enter.
	Command() string
	// Restores counts how often saved state was put back.
	Restores(t *testing.T, sb *tmuxtest.Sandbox) int
}

// drivers are the implementations to check: tmux-revive always, and the
// bash script when a dotfiles checkout is named.
func drivers(t *testing.T) []Driver {
	t.Helper()
	ds := []Driver{&goDriver{}}
	if dir := os.Getenv("TMUX_PLUGINS_DOTFILES"); dir != "" {
		ds = append(ds, &bashDriver{dotfiles: dir})
	} else if os.Getenv("TMUXTEST_REQUIRE") != "" {
		t.Fatal("TMUX_PLUGINS_DOTFILES is not set (TMUXTEST_REQUIRE is)")
	}
	return ds
}

// env is a sandbox with no server yet: the entry point starts it on the
// default socket, as on a real login.
type env struct {
	*tmuxtest.Sandbox
	d      Driver
	server *tmux.Client
}

func each(t *testing.T, fn func(t *testing.T, e *env)) {
	for _, d := range drivers(t) {
		t.Run(d.Name(), func(t *testing.T) {
			t.Parallel()
			tmuxtest.Require(t, "tmux", "script", "flock")
			sb := tmuxtest.NewSandbox(t)
			d.Setup(t, sb)
			// The default socket's folder, which tmux makes itself only when
			// it picks the socket; tests that start a server first use -S.
			sockDir := filepath.Join(sb.RuntimeDir, fmt.Sprintf("tmux-%d", os.Getuid()))
			if err := os.MkdirAll(sockDir, 0o700); err != nil {
				t.Fatal(err)
			}
			e := &env{
				Sandbox: sb,
				d:       d,
				server:  &tmux.Client{Socket: filepath.Join(sockDir, "default"), Env: sb.Environ()},
			}
			t.Cleanup(func() { e.server.Run(context.Background(), "kill-server") })
			fn(t, e)
		})
	}
}

func (e *env) tmux(t *testing.T, args ...string) string {
	t.Helper()
	out, err := e.server.Output(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// open runs the entry point in a new terminal and waits until it has a
// client attached.
func (e *env) open(t *testing.T) *tmuxtest.AttachedClient {
	t.Helper()
	before := e.clients()
	c := e.Terminal(t, e.d.Command(), 100, 30)
	tmuxtest.WaitFor(t, 10*time.Second, "the terminal to attach", func() bool {
		if done, err := c.Exited(); done {
			t.Fatalf("the entry point exited (%v) before attaching:\n%s", err, c.Output())
		}
		return len(e.clients()) > len(before)
	})
	return c
}

// clients returns "client session" lines.
func (e *env) clients() []string {
	out, err := e.server.Output(context.Background(), "list-clients", "-F", "#{client_tty} #{client_session}")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// newestClientSession is the session of the most recently attached client.
func (e *env) newestClientSession(t *testing.T) string {
	t.Helper()
	out := e.tmux(t, "list-clients", "-F", "#{client_created} #{client_session}")
	lines := strings.Split(out, "\n")
	slices.Sort(lines)
	_, s, _ := strings.Cut(lines[len(lines)-1], " ")
	return s
}

func (e *env) sessions(t *testing.T) []string {
	t.Helper()
	out := e.tmux(t, "list-sessions", "-F", "#{session_name}")
	names := strings.Split(out, "\n")
	slices.Sort(names)
	return names
}

func (e *env) wantSessions(t *testing.T, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := e.sessions(t); !slices.Equal(got, want) {
		t.Errorf("sessions = %v, want %v", got, want)
	}
}

// startServer starts a server on the default socket before any terminal,
// as something other than the terminal can.
func (e *env) startServer(t *testing.T, session string, windows int) {
	t.Helper()
	e.tmux(t, "new-session", "-d", "-s", session)
	for i := 1; i < windows; i++ {
		e.tmux(t, "new-window", "-d", "-t", session+":")
	}
}

// A bare server gets the saved state back, and the terminal lands on it:
// no bootstrap session and no empty main are left behind.
func TestOpenOnBareServerRestores(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.d.Saved(t, e.Sandbox, "proj-a", "proj-b")
		e.open(t)
		e.wantSessions(t, "proj-a", "proj-b")
		if got := e.d.Restores(t, e.Sandbox); got != 1 {
			t.Errorf("restores = %d, want 1", got)
		}
		if s := e.newestClientSession(t); s != "proj-a" && s != "proj-b" {
			t.Errorf("terminal on %q, want a restored session", s)
		}
	})
}

// With nothing saved the terminal gets a plain main session.
func TestOpenWithNothingSavedGivesMain(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.open(t)
		e.wantSessions(t, "main")
		if s := e.newestClientSession(t); s != "main" {
			t.Errorf("terminal on %q, want main", s)
		}
	})
}

// A restore happens once per server, not once per terminal; and the second
// terminal is not blocked by a lock the server kept.
func TestSecondTerminalDoesNotRestoreAgain(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.d.Saved(t, e.Sandbox, "proj-a")
		e.open(t)
		e.open(t)
		if got := e.d.Restores(t, e.Sandbox); got != 1 {
			t.Errorf("restores = %d, want 1", got)
		}
		e.wantSessions(t, "proj-a")
	})
}

// Two terminals opening together restore once.
func TestConcurrentTerminalsRestoreOnce(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.d.Saved(t, e.Sandbox, "proj-a", "proj-b")
		a := e.Terminal(t, e.d.Command(), 100, 30)
		b := e.Terminal(t, e.d.Command(), 100, 30)
		tmuxtest.WaitFor(t, 10*time.Second, "both terminals to attach", func() bool {
			for _, c := range []*tmuxtest.AttachedClient{a, b} {
				if done, err := c.Exited(); done {
					t.Fatalf("a terminal exited (%v):\n%s", err, c.Output())
				}
			}
			return len(e.clients()) == 2
		})
		if got := e.d.Restores(t, e.Sandbox); got != 1 {
			t.Errorf("restores = %d, want 1", got)
		}
		e.wantSessions(t, "proj-a", "proj-b")
	})
}

// A server that already holds work is never restored onto, even when no
// earlier terminal marked it restored.
func TestOpenNeverRestoresOntoLiveWork(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.startServer(t, "proj-x", 2)
		e.d.Saved(t, e.Sandbox, "proj-a")
		e.open(t)
		if got := e.d.Restores(t, e.Sandbox); got != 0 {
			t.Errorf("restores = %d, want 0", got)
		}
		e.wantSessions(t, "proj-x")
		// Nor later.
		e.open(t)
		if got := e.d.Restores(t, e.Sandbox); got != 0 {
			t.Errorf("restores after a second terminal = %d, want 0", got)
		}
	})
}

// A server holding only a one-window main counts as bare.
func TestOneWindowMainCountsAsBare(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.startServer(t, "main", 1)
		e.d.Saved(t, e.Sandbox, "proj-a")
		e.open(t)
		if got := e.d.Restores(t, e.Sandbox); got != 1 {
			t.Errorf("restores = %d, want 1", got)
		}
		if !slices.Contains(e.sessions(t), "proj-a") {
			t.Errorf("sessions = %v, want proj-a restored", e.sessions(t))
		}
	})
}

// Saved state holding its own main: the terminal still opens, and no
// bootstrap session is left.
func TestSavedMainIsKept(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.d.Saved(t, e.Sandbox, "main", "proj-a")
		e.open(t)
		e.wantSessions(t, "main", "proj-a")
	})
}

// Hidden overlay sessions come back but are never where a terminal lands.
func TestOverlaySessionsAreNeverTheTarget(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.d.Saved(t, e.Sandbox, "ov-nvim-proj-a")
		e.open(t)
		e.wantSessions(t, "main", "ov-nvim-proj-a")
		if s := e.newestClientSession(t); s != "main" {
			t.Errorf("terminal on %q, want main", s)
		}
	})
}

// A new terminal lands on the session used last.
func TestOpenLandsOnTheLastUsedSession(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		e.d.Saved(t, e.Sandbox, "proj-a", "proj-b", "proj-c")
		first := e.open(t)
		e.tmux(t, "switch-client", "-t", "=proj-b")
		time.Sleep(1100 * time.Millisecond) // session times have one-second resolution
		e.tmux(t, "switch-client", "-t", "=proj-c")
		first.Close()
		tmuxtest.WaitFor(t, 5*time.Second, "the first terminal to go", func() bool { return len(e.clients()) == 0 })
		e.open(t)
		if s := e.newestClientSession(t); s != "proj-c" {
			t.Errorf("terminal on %q, want proj-c", s)
		}
	})
}
