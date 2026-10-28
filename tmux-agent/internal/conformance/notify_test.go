package conformance

// Desktop notifications: for needs-you and done only, after a settle that
// re-reads the state, never for the pane on screen in a focused client;
// clicking one lands on the pane.

import (
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// eachWith is each with a step that prepares the sandbox before the server
// starts.
func eachWith(t *testing.T, prepare func(sb *tmuxtest.Sandbox), fn func(t *testing.T, e *env)) {
	name := t.Name()
	for _, d := range drivers(t) {
		t.Run(d.Name(), func(t *testing.T) {
			t.Parallel()
			if why := d.KnownBug(name); why != "" {
				t.Skipf("known %s bug: %s", d.Name(), why)
			}
			fn(t, newEnv(t, d, prepare))
		})
	}
}

func TestNotifyNeedsYouAndDone(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.newWindow(t, "proj-a", "w-build", "")
		e.d.Set(t, e.Server, p, Flag{State: "needs-you", Reason: "Bash(make)"})
		e.waitNotifications(t, 1)
		e.d.Set(t, e.Server, p, Flag{State: "done"})
		ns := e.waitNotifications(t, 2)
		want := []Notification{{"w-build needs you", "Bash(make)"}, {"w-build is done", "turn ended"}}
		if len(ns) != 2 || ns[0] != want[0] || ns[1] != want[1] {
			t.Errorf("notifications = %+v, want %+v", ns, want)
		}
	})
}

func TestNotifyDefaultBodies(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.newWindow(t, "proj-a", "w-q", "")
		e.d.Set(t, e.Server, p, Flag{State: "needs-you"})
		ns := e.waitNotifications(t, 1)
		if want := (Notification{"w-q needs you", "waiting for a decision"}); ns[0] != want {
			t.Errorf("notification = %+v, want %+v", ns[0], want)
		}
	})
}

func TestNotifyNotForWorkingOrQuiet(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.newWindow(t, "proj-a", "w-a", "")
		q := e.newWindow(t, "proj-a", "w-b", "")
		e.d.Set(t, e.Server, p, Flag{State: "working"})
		e.d.Set(t, e.Server, q, Flag{State: "done", Quiet: true})
		e.noNotification(t)
	})
}

// A state that changes again within the settle second stays quiet.
func TestNotifySettles(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.newWindow(t, "proj-a", "w-a", "")
		e.d.Set(t, e.Server, p, Flag{State: "needs-you", Reason: "Bash(ls)"})
		e.d.Set(t, e.Server, p, Flag{State: "working"})
		e.noNotification(t)
	})
}

// The pane on screen in a focused client gets no notification.
func TestNotifyNotForVisiblePane(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.firstPane(t)
		e.Attach(t, "proj-a", 100, 30)
		e.d.Set(t, e.Server, p, Flag{State: "done"})
		e.noNotification(t)
	})
}

// The same pane notifies once the terminal has lost focus.
func TestNotifyVisiblePaneOfUnfocusedClient(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.firstPane(t)
		c := e.Attach(t, "proj-a", 100, 30)
		c.Blur(t)
		tmuxtest.WaitFor(t, 2*time.Second, "blur", func() bool {
			return !strings.Contains(e.MustTmux(t, "list-clients", "-F", "#{client_flags}"), "focused")
		})
		e.d.Set(t, e.Server, p, Flag{State: "done"})
		e.waitNotifications(t, 1)
	})
}

// clickStub is a notify-send that supports actions and reports a click on
// the default one.
const clickStub = "#!/bin/sh\n" + tmuxtest.RecordCall + `case "$1" in --help) echo '  -A, --action=[NAME=]Text...'; exit 0 ;; esac
for a in "$@"; do [ "$a" = "-A" ] && { echo default; exit 0; }; done
exit 0
`

// Clicking a notification lands on the pane, and raises the terminal when
// there is a sway socket.
func TestNotifyClickJumps(t *testing.T) {
	prepare := func(sb *tmuxtest.Sandbox) {
		sb.Stub(t, "notify-send", clickStub)
		sb.Setenv("SWAYSOCK", sb.RuntimeDir+"/sway.sock")
	}
	eachWith(t, prepare, func(t *testing.T, e *env) {
		e.Attach(t, "proj-a", 100, 30)
		e.newSession(t, "proj-b", "")
		p := e.newWindow(t, "proj-b", "w-x", "")
		e.d.Set(t, e.Server, p, Flag{State: "needs-you", Reason: "Bash(rm)"})
		tmuxtest.WaitFor(t, 5*time.Second, "the client on the flagged pane", func() bool {
			out, err := e.Tmux("display-message", "-p", "-c", e.clientName(t), "#{pane_id}")
			return err == nil && out == p
		})
		tmuxtest.WaitFor(t, 3*time.Second, "swaymsg", func() bool { return len(e.Calls("swaymsg")) > 0 })
		if got := strings.Join(e.Calls("swaymsg")[0].Args, " "); !strings.Contains(got, "focus") {
			t.Errorf("swaymsg %q, want a focus command", got)
		}
	})
}
