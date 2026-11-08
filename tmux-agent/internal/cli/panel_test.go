package cli

import (
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// ctrl-x in the panel kills the row it is on: a pane, a window or a
// session. A kill that would end a session follows the session's rule: the
// last session stays, as the terminal would close with it.
func TestKillRow(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	s.MustTmux(t, "split-window", "-d", "-t", "=proj-a:0", ";",
		"new-window", "-d", "-t", "=proj-a:1", ";",
		"new-session", "-d", "-s", "proj-b")
	s.InPane(t, s.Display(t, "=proj-b:0", "#{pane_id}"))
	windows := func() string {
		return s.MustTmux(t, "list-windows", "-a", "-F", "#{session_name}:#{window_index}/#{window_panes}")
	}

	steps := []struct {
		target, want string
	}{
		{"=proj-a:0.1", "proj-a:0/1\nproj-a:1/1\nproj-b:0/1"}, // a pane of a split window
		{"=proj-a:1", "proj-a:0/1\nproj-b:0/1"},               // a window
		{"=proj-a:0.0", "proj-b:0/1"},                         // the last pane of a session
		{"=proj-b:0", "proj-b:0/1"},                           // the last session: refused
		{"=proj-b:0.0", "proj-b:0/1"},
		{"=proj-b:", "proj-b:0/1"},
	}
	for _, st := range steps {
		if _, code := run(t, "kill", st.target); code != 0 {
			t.Fatalf("kill %s: exit %d", st.target, code)
		}
		if got := windows(); got != st.want {
			t.Fatalf("after kill %s: windows\n%s\nwant\n%s", st.target, got, st.want)
		}
	}
}

// A session killed from the panel keeps its clients: each one moves to
// another session first, so its terminal stays open (detach-on-destroy is
// on by default).
func TestKillSessionKeepsItsClient(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	s.MustTmux(t, "new-session", "-d", "-s", "proj-b")
	c := s.Attach(t, "proj-a", 100, 30)
	s.InPane(t, s.Display(t, "=proj-b:0", "#{pane_id}"))
	if _, code := run(t, "kill", "=proj-a:"); code != 0 {
		t.Fatalf("kill exit %d", code)
	}
	if got := s.MustTmux(t, "list-sessions", "-F", "#{session_name}"); got != "proj-b" {
		t.Errorf("sessions = %q, want proj-b", got)
	}
	if exited, _ := c.Exited(); exited {
		t.Fatal("the client exited with its session")
	}
	if got := s.MustTmux(t, "list-clients", "-F", "#{client_session}"); got != "proj-b" {
		t.Errorf("client on %q, want proj-b", got)
	}
}
