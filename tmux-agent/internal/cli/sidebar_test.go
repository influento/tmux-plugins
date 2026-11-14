package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/selftest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/sidebar"
)

// The panel end to end, with tmux-init's hooks and the real fzf: prefix f
// opens it at the left edge; it is no row of its own and adds no border
// line; a switch outside the panel takes it along (the follow hook); Esc
// in it goes back to where it opened and closes it, the hooks idle again.
func TestSidebarEndToEnd(t *testing.T) {
	tmuxtest.Require(t, "tmux", "fzf")
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	w0 := s.Display(t, "=proj-a:0", "#{window_id}")
	origin := s.Display(t, "=proj-a:0", "#{pane_id}")
	w1 := s.MustTmux(t, "new-window", "-d", "-t", "=proj-a:1", "-n", "w-two", "-P", "-F", "#{window_id}")
	s.Attach(t, "proj-a", 120, 31)
	client := s.MustTmux(t, "list-clients", "-F", "#{client_name}")
	tmuxAgent := func(args ...string) string {
		t.Helper()
		out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	tmuxAgent("tmux-init")
	panelIn := func() string {
		out := s.MustTmux(t, "list-panes", "-a", "-F", "#{window_id} #{"+sidebar.OptClient+"}")
		for _, l := range strings.Split(out, "\n") {
			if w, c, _ := strings.Cut(l, " "); c != "" {
				return w
			}
		}
		return ""
	}

	tmuxAgent("popup", client)
	if got := panelIn(); got != w0 {
		t.Fatalf("panel in %q, want %s", got, w0)
	}
	panel := strings.TrimSpace(s.MustTmux(t, "list-panes", "-t", w0, "-F", "#{?#{"+sidebar.OptClient+"},#{pane_id},}"))
	s.WaitForContent(t, panel, "proj-a")
	if got := s.Display(t, w0, "#{pane-border-status}"); got == "top" {
		t.Error("the panel turned the window's border line on")
	}
	var rows []struct{ Pane string }
	if err := json.Unmarshal([]byte(tmuxAgent("list", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Pane == panel {
			t.Errorf("list has a row for the panel %s", panel)
		}
	}

	s.MustTmux(t, "send-keys", "-t", panel, "C-l")
	tmuxtest.WaitFor(t, 5*time.Second, "C-l to leave the panel", func() bool {
		return s.Display(t, w0, "#{pane_id}") == origin
	})
	s.MustTmux(t, "select-pane", "-t", panel)

	s.MustTmux(t, "select-window", "-t", w1)
	tmuxtest.WaitFor(t, 5*time.Second, "the panel to follow to "+w1, func() bool { return panelIn() == w1 })

	// A renamed window reloads the panel, through its fzf's socket.
	s.MustTmux(t, "rename-window", "-t", w1, "w-renamed")
	s.WaitForContent(t, panel, "w-renamed")

	s.MustTmux(t, "send-keys", "-t", panel, "Escape")
	tmuxtest.WaitFor(t, 5*time.Second, "the panel to close", func() bool { return panelIn() == "" })
	if got := s.MustTmux(t, "list-clients", "-F", "#{pane_id}"); got != origin {
		t.Errorf("client on %s after Esc, want %s, where the panel opened", got, origin)
	}
	if got := s.MustTmux(t, "show-options", "-gqv", sidebar.OptOpen); got != "" {
		t.Errorf("%s = %q with no panel open", sidebar.OptOpen, got)
	}
}

// openPanel starts a server with tmux-init's hooks and a client, opens the
// panel, and returns the server, the pane it opened from, and the panel.
func openPanel(t *testing.T, before ...[]string) (s *tmuxtest.Server, origin, panel string) {
	t.Helper()
	tmuxtest.Require(t, "tmux", "fzf")
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	s = tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	origin = s.Display(t, "=proj-a:0", "#{pane_id}")
	s.MustTmux(t, "rename-window", "-t", "=proj-a:0", "w-one")
	s.Attach(t, "proj-a", 120, 31)
	client := s.MustTmux(t, "list-clients", "-F", "#{client_name}")
	for _, args := range before {
		s.MustTmux(t, args...)
	}
	for _, args := range [][]string{{"tmux-init"}, {"popup", client}} {
		if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
		}
	}
	tmuxtest.WaitFor(t, 5*time.Second, "the panel", func() bool {
		panel = strings.TrimSpace(s.MustTmux(t, "list-panes", "-a", "-F", "#{?#{"+sidebar.OptClient+"},#{pane_id},}"))
		return panel != ""
	})
	s.WaitForContent(t, panel, "w-one")
	return s, origin, panel
}

// The panel is live: a flag set or cleared, by a hook or by hand, shows in
// the open panel without a key pressed or a window renamed.
func TestPanelShowsFlagChanges(t *testing.T) {
	s, origin, panel := openPanel(t)
	flag := func(args ...string) {
		t.Helper()
		args = append([]string{"flag"}, append(args, "--pane", origin)...)
		if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
		}
	}
	flag("needs-you", "--reason", "Bash(make)")
	s.WaitForContent(t, panel, "Bash(make)")
	flag("clear")
	tmuxtest.WaitFor(t, 5*time.Second, "the cleared flag to leave the panel", func() bool {
		return !strings.Contains(s.MustTmux(t, "capture-pane", "-p", "-t", panel), "Bash(make)")
	})
}

// An open panel reads its rows again when what they show changes with no
// event to tell: an age moves on, a label's time ends.
func TestPanelTicks(t *testing.T) {
	s, origin, panel := openPanel(t)
	run := func(args ...string) {
		t.Helper()
		if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
		}
	}
	run("flag", "working", "--pane", origin)
	s.WaitForContent(t, panel, "●") // the flag's own reload is done
	s.WaitForContent(t, panel, " 3s")
	run("report", "--source", "probe", "--text", "tick-label", "--ttl", "2s", "--pane", origin)
	s.WaitForContent(t, panel, "tick-label")
	tmuxtest.WaitFor(t, 5*time.Second, "the label to go at its time", func() bool {
		return !strings.Contains(s.MustTmux(t, "capture-pane", "-p", "-t", panel), "tick-label")
	})
}

// A reload of the panel does nothing to its client: a flag on the pane
// the client shows stays, as it does with the panel closed. A reload that
// sent the client to its row again marked the pane seen, and one read
// before the user changed windows took the client back.
func TestPanelReloadLeavesClient(t *testing.T) {
	s, origin, panel := openPanel(t)
	run := func(args ...string) {
		t.Helper()
		if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
		}
	}
	other := s.MustTmux(t, "new-window", "-d", "-n", "w-two", "-P", "-F", "#{pane_id}")
	run("flag", "needs-you", "--pane", other) // a row to show labels on
	s.WaitForContent(t, panel, "w-two")
	// The focus in the main area, as after a switch. Its hook marks the
	// pane seen in the background: once that cleared a flag, it is done.
	run("flag", "done", "--pane", origin)
	s.MustTmux(t, "select-pane", "-t", origin)
	// No fail here: a reload that moves the client keeps it from coming;
	// the checks below tell.
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if s.Display(t, origin, "#{@attention}") == "" {
			break
		}
	}
	run("flag", "done", "--pane", origin)
	// Each report reloads the panel; once the last shows, the reloads
	// before it are done, with all they ran.
	for _, text := range []string{"label-one", "label-two", "label-three"} {
		run("report", "--source", "probe", "--text", text, "--pane", other)
		s.WaitForContent(t, panel, text)
	}
	if got := s.Display(t, origin, "#{@attention}"); got != "done" {
		t.Errorf("flag on the client's pane = %q after reloads, want done", got)
	}
	if got, want := s.MustTmux(t, "list-clients", "-F", "#{pane_id}"), origin; got != want {
		t.Errorf("client on %s after reloads, want %s", got, want)
	}
}

// panelCursor is the target of the row under the panel's cursor, as its
// fzf tells on its socket.
func panelCursor(t *testing.T, socket string) string {
	t.Helper()
	hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := hc.Get("http://fzf/?limit=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st struct{ Current *struct{ Text string } }
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Current == nil {
		return ""
	}
	f := strings.Split(st.Current.Text, "\t")
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

// The window follows the panel's cursor at once, and the cursor stays on
// the row it was moved to, while the panel reloads (its ticks, a flag
// changed): a reload put the cursor back on the row of the window the
// client showed before the move, and the window and the row disagreed
// until the next reload. A switch some other way moves the cursor. The
// limits rule out a lost move, not a slow one: a move takes 25-75 ms on
// a workstation, up to 600 ms on CI's runner with -race.
func TestPanelMoveWhileReloading(t *testing.T) {
	s, origin, panel := openPanel(t)
	names := []string{"w-two", "w-three", "w-four", "w-five"}
	for _, n := range names {
		p := s.MustTmux(t, "new-window", "-d", "-n", n, "-P", "-F", "#{pane_id}")
		// Working: a visit leaves the flag, so the rows stay.
		if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", p).CombinedOutput(); err != nil {
			t.Fatalf("flag: %v: %s", err, out)
		}
	}
	s.WaitForContent(t, panel, "w-five")
	socket := s.Display(t, panel, "#{"+sidebar.OptListen+"}")
	client := func() string {
		return s.MustTmux(t, "list-clients", "-F", "=#{session_name}:#{window_index}")
	}

	// One reload per move, at a point of its race: just before the key,
	// with it, during the move's switch. A steady stream of reloads
	// outran fzf on a slow runner, and every move waited behind them.
	reload := s.Command(t, origin, "tmux-agent", "sidebar", "reload")
	var reloads sync.WaitGroup
	defer reloads.Wait()
	reloadIn := func(d time.Duration) {
		reloads.Go(func() {
			time.Sleep(d)
			cmd := exec.CommandContext(t.Context(), reload.Path, reload.Args[1:]...)
			cmd.Env = reload.Env
			_ = cmd.Run() // the panel reloads or it does not; the moves tell
		})
	}
	offsets := []time.Duration{-10 * time.Millisecond, 0, 5 * time.Millisecond, 15 * time.Millisecond, 30 * time.Millisecond}

	t0 := time.Now()
	var seen []string
	lastC, lastW := "", ""
	defer func() {
		if t.Failed() {
			t.Logf("timeline:\n%s", strings.Join(seen, "\n"))
		}
	}()
	note := func(c, w string) {
		if c != lastC || w != lastW {
			seen = append(seen, fmt.Sprintf("%6.1f ms cursor %s client %s", float64(time.Since(t0).Microseconds())/1000, c, w))
			lastC, lastW = c, w
		}
	}
	note(panelCursor(t, socket), client())
	key := "Up" // the cursor starts on the fold below the windows
	for i := range 15 {
		from := panelCursor(t, socket)
		switch from {
		case "=proj-a:1":
			key = "Down"
		case "=proj-a:4", "=proj-a:":
			key = "Up"
		}
		if d := offsets[i%len(offsets)]; d < 0 {
			reloadIn(0)
			time.Sleep(-d)
		} else {
			reloadIn(d)
		}
		start := time.Now()
		seen = append(seen, fmt.Sprintf("%6.1f ms send %s", float64(time.Since(t0).Microseconds())/1000, key))
		s.MustTmux(t, "send-keys", "-t", panel, key)
		to := ""
		for time.Since(start) < 2*time.Second && to == "" {
			c := panelCursor(t, socket)
			note(c, client())
			if c != from {
				to = c
			}
		}
		if to == "" {
			t.Fatalf("%s from %s: the cursor did not move", key, from)
		}
		// Until the client is there, and a moment after: the cursor stays.
		var followed time.Duration
		for end := start.Add(2 * time.Second); time.Now().Before(end); {
			c, w := panelCursor(t, socket), client()
			note(c, w)
			if c != to {
				t.Fatalf("cursor moved from %s to %s, then to %s (client on %s)", from, to, c, w)
			}
			if followed == 0 && w == to {
				followed = time.Since(start)
				end = time.Now().Add(200 * time.Millisecond)
			}
		}
		if followed == 0 {
			t.Fatalf("client on %s %v after the cursor moved to %s", client(), time.Since(start), to)
		}
		t.Logf("%s to %s: client followed in %v", key, to, followed)
	}

	// A switch some other way (prefix n, a notification) takes the cursor
	// along.
	for _, to := range []string{"=proj-a:2", "=proj-a:4"} {
		start := time.Now()
		s.MustTmux(t, "select-window", "-t", to)
		for c := panelCursor(t, socket); c != to; c = panelCursor(t, socket) {
			note(c, client())
			if time.Since(start) > 2*time.Second {
				t.Fatalf("cursor on %s %v after a switch to %s", c, time.Since(start), to)
			}
		}
	}
}
