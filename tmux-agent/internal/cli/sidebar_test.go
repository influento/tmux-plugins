package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	p1 := s.Display(t, w1, "#{pane_id}")
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

	// A renamed window reloads the panel, through its fzf's socket. A
	// flag gives the window a row of its own: a fold shows no names.
	tmuxAgent("flag", "working", "--pane", p1)
	s.WaitForContent(t, panel, "w-two")
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
	s, origin, panel = openPanelAfter(t, func(s *tmuxtest.Server, _ string) {
		for _, args := range before {
			s.MustTmux(t, args...)
		}
	})
	s.WaitForContent(t, panel, "1 window") // the fold that holds w-one
	return s, origin, panel
}

// openPanelAfter is openPanel with setup run on the server, and the pane
// the panel opens from, before the panel opens.
func openPanelAfter(t *testing.T, setup func(s *tmuxtest.Server, origin string)) (s *tmuxtest.Server, origin, panel string) {
	t.Helper()
	tmuxtest.Require(t, "tmux", "fzf")
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	// The log then has each go, with the fzf event that ran it: a failed
	// test shows it.
	sb.Setenv("TMUX_AGENT_DEBUG", "1")
	t.Cleanup(func() {
		if t.Failed() {
			data, err := os.ReadFile(filepath.Join(sb.StateHome, "tmux-agent", "log"))
			t.Logf("tmux-agent log (%v):\n%s", err, data)
		}
	})
	s = tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	origin = s.Display(t, "=proj-a:0", "#{pane_id}")
	s.MustTmux(t, "rename-window", "-t", "=proj-a:0", "w-one")
	s.Attach(t, "proj-a", 120, 31)
	client := s.MustTmux(t, "list-clients", "-F", "#{client_name}")
	setup(s, origin)
	for _, args := range [][]string{{"tmux-init"}, {"popup", client}} {
		if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
		}
	}
	tmuxtest.WaitFor(t, 5*time.Second, "the panel", func() bool {
		panel = strings.TrimSpace(s.MustTmux(t, "list-panes", "-a", "-F", "#{?#{"+sidebar.OptClient+"},#{pane_id},}"))
		return panel != ""
	})
	s.WaitForContent(t, panel, "proj-a")
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
	s.WaitForContent(t, panel, "w-one") // a row of its own
	flag("clear")
	tmuxtest.WaitFor(t, 5*time.Second, "the cleared flag to leave the panel", func() bool {
		return !strings.Contains(s.MustTmux(t, "capture-pane", "-p", "-t", panel), "w-one")
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
// a workstation, up to 2 s on a slow CI runner with -race.
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

	const limit = 5 * time.Second
	t0 := time.Now()
	var seen []string
	lastC, lastW := "", ""
	defer func() {
		if t.Failed() {
			t.Logf("timeline from %s:\n%s", t0.Format("15:04:05.000"), strings.Join(seen, "\n"))
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
		for time.Since(start) < limit && to == "" {
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
		for end := start.Add(limit); time.Now().Before(end); {
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
			if time.Since(start) > limit {
				t.Fatalf("cursor on %s %v after a switch to %s", c, time.Since(start), to)
			}
		}
	}
}

// panelRow is the label and the target of the row under the panel's
// cursor.
func panelRow(t *testing.T, socket string) (label, target string) {
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
		return "", ""
	}
	f := strings.Split(st.Current.Text, "\t")
	if len(f) < 2 {
		return "", ""
	}
	return f[0], f[1]
}

// Keys at a held arrow's repeat move the cursor as fast as they come, and
// the client follows to the row it stops on: no queue of moves runs on
// after the keys end. Up at the top row stays there, never on the
// session's name above it. Each key's move used to hold up the next
// key, so the moves ran on for seconds.
func TestPanelHeldKey(t *testing.T) {
	s, origin, panel := openPanel(t)
	for _, n := range []string{"w-two", "w-three", "w-four", "w-five", "w-six"} {
		p := s.MustTmux(t, "new-window", "-d", "-n", n, "-P", "-F", "#{pane_id}")
		if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", p).CombinedOutput(); err != nil {
			t.Fatalf("flag: %v: %s", err, out)
		}
	}
	s.WaitForContent(t, panel, "w-six")
	socket := s.Display(t, panel, "#{"+sidebar.OptListen+"}")
	client := func() string {
		return s.MustTmux(t, "list-clients", "-F", "=#{session_name}:#{window_index}")
	}
	// A held key's repeat: one every 30 ms.
	key := func(k string) {
		s.MustTmux(t, "send-keys", "-t", panel, k)
		time.Sleep(30 * time.Millisecond)
	}
	var keys sync.WaitGroup
	keys.Go(func() {
		key("Up") // from the fold to w-six
		for range 30 {
			key("Up")
			key("Down")
		}
		for range 8 { // past the top
			key("Up")
		}
	})
	// The keys end in the top row. From when the cursor is there, the
	// client makes at most two moves: the one running, then the one to
	// that row. A go per key made one per key still queued.
	keysSent := make(chan struct{})
	go func() { keys.Wait(); close(keysSent) }()
	var labels []string
	var settled time.Time // the cursor on its last row, the keys sent
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		label, target := panelRow(t, socket)
		if label != "" {
			labels = append(labels, label)
		}
		if settled.IsZero() {
			select {
			case <-keysSent:
				if target == "=proj-a:1" {
					settled = time.Now()
				}
			default:
			}
			continue
		}
		if client() == target {
			break
		}
	}
	if settled.IsZero() {
		t.Fatal("the cursor never stopped on the top row after the keys")
	}
	if moves := movesSince(t, s, settled); moves > 2 {
		t.Errorf("the client made %d moves after the cursor stopped; want at most 2", moves)
	}
	if label, target := panelRow(t, socket); target != "=proj-a:1" || client() != target {
		t.Fatalf("cursor on %q %s, client on %s; want both on the top row", label, target, client())
	}
	if len(labels) > 0 {
		t.Errorf("the cursor stopped on labels: %q", labels)
	}
}

// movesSince counts the panel's moves of a client, whatever their
// reason, in the log from since on.
var clientMove = regexp.MustCompile(`client \S+ @\d+ → @\d+`)

func movesSince(t *testing.T, s *tmuxtest.Server, since time.Time) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.StateHome, "tmux-agent", "log"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		stamp, rest, _ := strings.Cut(l, " ")
		if !clientMove.MatchString(rest) {
			continue
		}
		at, err := time.Parse("2006-01-02T15:04:05.000Z07:00", stamp)
		if err != nil {
			t.Fatal(err)
		}
		if !at.Before(since.Truncate(time.Millisecond)) {
			n++
		}
	}
	return n
}

// A fold's row keeps the cursor when the panel reloads, though its go
// leaves the client on a window that has a row of its own: the
// session's current one.
func TestPanelFoldKeepsCursor(t *testing.T) {
	s, origin, panel := openPanel(t)
	p := s.MustTmux(t, "new-window", "-d", "-n", "w-two", "-P", "-F", "#{pane_id}")
	if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", p).CombinedOutput(); err != nil {
		t.Fatalf("flag: %v: %s", err, out)
	}
	s.WaitForContent(t, panel, "w-two")
	socket := s.Display(t, panel, "#{"+sidebar.OptListen+"}")
	client := func() string {
		return s.MustTmux(t, "list-clients", "-F", "=#{session_name}:#{window_index}")
	}
	s.MustTmux(t, "send-keys", "-t", panel, "Up") // from the fold to w-two
	tmuxtest.WaitFor(t, 5*time.Second, "the client on w-two", func() bool { return client() == "=proj-a:1" })
	s.MustTmux(t, "send-keys", "-t", panel, "Down") // back to the fold
	tmuxtest.WaitFor(t, 5*time.Second, "the cursor on the fold", func() bool {
		_, target := panelRow(t, socket)
		return target == "=proj-a:"
	})
	if out, err := s.Command(t, origin, "tmux-agent", "sidebar", "reload").CombinedOutput(); err != nil {
		t.Fatalf("reload: %v: %s", err, out)
	}
	time.Sleep(500 * time.Millisecond) // the reload and its place
	if _, target := panelRow(t, socket); target != "=proj-a:" {
		t.Errorf("cursor on %s after a reload, want the fold", target)
	}
	if got := client(); got != "=proj-a:1" {
		t.Errorf("client on %s, want w-two, the session's current window", got)
	}
}

// After a row chosen in the panel, a switch some other way (a key of
// tmux's, a click on the status line) moves the cursor to the client's
// window. The chosen row used to keep it: the panel took it for a move
// still on its way.
func TestPanelFollowsSwitchAfterGo(t *testing.T) {
	s, origin, panel := openPanel(t)
	for _, n := range []string{"w-two", "w-three"} {
		p := s.MustTmux(t, "new-window", "-d", "-n", n, "-P", "-F", "#{pane_id}")
		if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", p).CombinedOutput(); err != nil {
			t.Fatalf("flag: %v: %s", err, out)
		}
	}
	s.WaitForContent(t, panel, "w-three")
	socket := s.Display(t, panel, "#{"+sidebar.OptListen+"}")
	client := func() string {
		return s.MustTmux(t, "list-clients", "-F", "=#{session_name}:#{window_index}")
	}
	s.MustTmux(t, "send-keys", "-t", panel, "Up") // from the fold to w-three
	tmuxtest.WaitFor(t, 5*time.Second, "the client on w-three", func() bool { return client() == "=proj-a:2" })
	s.MustTmux(t, "select-pane", "-t", "=proj-a:2.0") // the focus in the main area, as after C-l
	for _, w := range []string{"=proj-a:1", "=proj-a:2", "=proj-a:1"} {
		s.MustTmux(t, "select-window", "-t", w)
		tmuxtest.WaitFor(t, 5*time.Second, "the cursor on "+w, func() bool {
			_, target := panelRow(t, socket)
			return target == w
		})
	}
}

// openPanelSearch opens the panel over w-one to w-ten, moves the client
// to w-ten with the panel, and types the search w-t: its rows are w-two
// (proj-a:1), w-three (proj-a:2) and w-ten (proj-a:3). The cursor goes to
// the first match, not the client's row, and the client follows it. It
// returns w-three's pane, and settle, which waits until the reloads begun
// before it are done, and returns where the cursor and the client are.
func openPanelSearch(t *testing.T) (s *tmuxtest.Server, panel, three string, settle func() (cursor, client string)) {
	t.Helper()
	s, origin, panel := openPanel(t)
	panes := map[string]string{}
	for _, n := range []string{"w-two", "w-three", "w-ten"} {
		panes[n] = s.MustTmux(t, "new-window", "-d", "-n", n, "-P", "-F", "#{pane_id}")
		if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", panes[n]).CombinedOutput(); err != nil {
			t.Fatalf("flag: %v: %s", err, out)
		}
	}
	s.WaitForContent(t, panel, "w-ten")
	socket := s.Display(t, panel, "#{"+sidebar.OptListen+"}")
	client := func() string {
		return s.MustTmux(t, "list-clients", "-F", "=#{session_name}:#{window_index}")
	}
	s.MustTmux(t, "send-keys", "-t", panel, "Up") // from the fold to w-ten
	tmuxtest.WaitFor(t, 5*time.Second, "the client on w-ten", func() bool { return client() == "=proj-a:3" })
	s.MustTmux(t, "send-keys", "-t", panel, "-l", "w-t")
	tmuxtest.WaitFor(t, 5*time.Second, "the cursor and the client on the first match", func() bool {
		return panelCursor(t, socket) == "=proj-a:1" && client() == "=proj-a:1"
	})
	n := 0
	settle = func() (string, string) {
		t.Helper()
		// Each report reloads the panel; once the last shows, the reloads
		// before it are done, with all they ran.
		for range 3 {
			n++
			text := fmt.Sprintf("probe-%d", n)
			if out, err := s.Command(t, origin, "tmux-agent", "report", "--source", "probe", "--text", text, "--pane", panes["w-three"]).CombinedOutput(); err != nil {
				t.Fatalf("report: %v: %s", err, out)
			}
			s.WaitForContent(t, panel, text)
		}
		return panelCursor(t, socket), client()
	}
	return s, panel, panes["w-three"], settle
}

// During a search, a switch some other way (prefix n, a click) moves
// the cursor to the client's row, and the client stays there while the
// panel reloads. The result event of every reload put the cursor back on
// the search's first row, and the client followed it.
func TestPanelSearchFollowsSwitch(t *testing.T) {
	s, _, three, settle := openPanelSearch(t)
	s.MustTmux(t, "switch-client", "-c", s.MustTmux(t, "list-clients", "-F", "#{client_name}"), "-t", three)
	if cursor, client := settle(); cursor != "=proj-a:2" || client != "=proj-a:2" {
		t.Errorf("cursor on %s, client on %s after the switch to w-three; want both on =proj-a:2", cursor, client)
	}
}

// During a search, a move in the panel to another row takes the client
// there, and both stay while the panel reloads (its join, a label).
func TestPanelSearchKeepsMove(t *testing.T) {
	s, panel, _, settle := openPanelSearch(t)
	s.MustTmux(t, "send-keys", "-t", panel, "Down")
	tmuxtest.WaitFor(t, 5*time.Second, "the client on w-three", func() bool {
		return s.MustTmux(t, "list-clients", "-F", "=#{session_name}:#{window_index}") == "=proj-a:2"
	})
	if cursor, client := settle(); cursor != "=proj-a:2" || client != "=proj-a:2" {
		t.Errorf("cursor on %s, client on %s after Down to w-three; want both on =proj-a:2", cursor, client)
	}
}

// The log tells each move of the client and why, and each switch the panel's follow hook sees, whoever made it.
// A panel that only ticks writes nothing: the log stays readable for the
// day a switch nobody asked for needs explaining.
func TestPanelTracesMoves(t *testing.T) {
	s, origin, panel := openPanel(t)
	p := s.MustTmux(t, "new-window", "-d", "-n", "w-two", "-P", "-F", "#{pane_id}")
	if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", p).CombinedOutput(); err != nil {
		t.Fatalf("flag: %v: %s", err, out)
	}
	s.WaitForContent(t, panel, "w-two")
	two := s.Display(t, "=proj-a:1", "#{window_id}")
	logPath := filepath.Join(s.Sandbox.StateHome, "tmux-agent", "log")
	log := func() string {
		data, _ := os.ReadFile(logPath) // none yet: nothing logged
		return string(data)
	}
	s.MustTmux(t, "send-keys", "-t", panel, "Up") // from the fold to w-two
	tmuxtest.WaitFor(t, 5*time.Second, "the go in the log", func() bool {
		return strings.Contains(log(), "go to the row in .want =proj-a:1: client ") &&
			strings.Contains(log(), " → "+two+", panel "+panel)
	})
	tmuxtest.WaitFor(t, 5*time.Second, "the follow of the go's switch", func() bool {
		return strings.Contains(log(), "follow: client ") && strings.Contains(log(), " on "+two+", panel "+panel)
	})
	n := len(log())
	s.MustTmux(t, "select-window", "-t", "=proj-a:0") // a switch some other way
	one := s.Display(t, "=proj-a:0", "#{window_id}")
	tmuxtest.WaitFor(t, 5*time.Second, "the other switch and the cursor's place", func() bool {
		l := log()[n:]
		return strings.Contains(l, " on "+one+", panel "+panel) &&
			strings.Contains(l, "place: client on =proj-a:0, cursor row ")
	})
	// The ages tick each second: each reload places the cursor where it is.
	before := strings.Count(log(), "place: ")
	time.Sleep(2500 * time.Millisecond)
	if after := strings.Count(log(), "place: "); after != before {
		t.Errorf("%d place lines while nothing moved:\n%s", after-before, log())
	}
}

// openPanelRows opens the panel over three agent windows: proj-a:1 and
// proj-a:2 with names the panel cuts, proj-a:2 split with a shell (so its
// row's target names the agent's pane, which the panel's join renumbers),
// and proj-a:3 with a name that fits. The cursor is on the fold of w-one,
// where the client is.
func openPanelRows(t *testing.T) (s *tmuxtest.Server, origin, panel, socket string, panes []string) {
	t.Helper()
	s, origin, panel = openPanel(t)
	for _, n := range []string{"a-popup-closed-by-its-hotkey-leaves-the-client", "a-split-window-with-an-agent-and-a-shell", "short-one"} {
		p := s.MustTmux(t, "new-window", "-d", "-n", n, "-P", "-F", "#{pane_id}")
		if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", p).CombinedOutput(); err != nil {
			t.Fatalf("flag: %v: %s", err, out)
		}
		panes = append(panes, p)
	}
	s.MustTmux(t, "split-window", "-d", "-t", panes[1])
	s.WaitForContent(t, panel, "short-one")
	return s, origin, panel, s.Display(t, panel, "#{"+sidebar.OptListen+"}"), panes
}

// The rows' names as the panel shows them: open, whole, or cut.
const (
	popupCut   = "hotkey-lea…"
	popupWhole = "hotkey-\n"
	splitCut   = "agent-and…"
	splitWhole = "agent-and-\n"
)

// The row under the panel's cursor shows its whole name; the row it left
// is cut again. It stays open while the panel reloads: as the panel joins
// its window (the join renumbers the window's panes), as an age ticks and
// a flag changes.
func TestPanelOpensCursorRow(t *testing.T) {
	s, origin, panel, socket, panes := openPanelRows(t)
	screen := func() string { return s.MustTmux(t, "capture-pane", "-p", "-t", panel) }
	if sc := screen(); !strings.Contains(sc, popupCut) || !strings.Contains(sc, splitCut) {
		t.Fatalf("rows not cut with the cursor elsewhere:\n%s", sc)
	}
	s.MustTmux(t, "send-keys", "-t", panel, "Up") // to short-one
	tmuxtest.WaitFor(t, 5*time.Second, "the cursor on short-one", func() bool { return panelCursor(t, socket) == "=proj-a:3" })
	s.MustTmux(t, "send-keys", "-t", panel, "Up") // to the split window
	opened := time.Time{}
	reloaded := false
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		sc := screen()
		if opened.IsZero() {
			if strings.Contains(sc, splitWhole) {
				opened = time.Now()
			}
			continue
		}
		if !strings.Contains(sc, splitWhole) || strings.Contains(sc, splitCut) {
			t.Fatalf("the open row closed after %v:\n%s", time.Since(opened), sc)
		}
		if !reloaded && time.Since(opened) > 300*time.Millisecond {
			if out, err := s.Command(t, origin, "tmux-agent", "flag", "needs-you", "--pane", panes[2]).CombinedOutput(); err != nil {
				t.Fatalf("flag: %v: %s", err, out)
			}
			reloaded = true
		}
		if time.Since(opened) > 2500*time.Millisecond { // an age's tick, and the flag's reload
			break
		}
	}
	if opened.IsZero() {
		t.Fatalf("the split window's row never opened:\n%s", screen())
	}
	if got := s.MustTmux(t, "list-clients", "-F", "=#{session_name}:#{window_index}"); got != "=proj-a:2" {
		t.Errorf("client on %s, want =proj-a:2", got)
	}

	s.MustTmux(t, "send-keys", "-t", panel, "Up")
	tmuxtest.WaitFor(t, 5*time.Second, "the first row open, the split one cut", func() bool {
		sc := screen()
		return strings.Contains(sc, popupWhole) && strings.Contains(sc, "leaves-the-client") && strings.Contains(sc, splitCut)
	})
}

// A search that matches no row, cleared, brings the rows back, the
// client's open: with no row under the cursor fzf runs no command that
// names a field. The flag is two hours old: no age ticks, so no reload
// but the search's comes.
func TestPanelSearchWithoutMatch(t *testing.T) {
	s, _, panel := openPanelAfter(t, func(s *tmuxtest.Server, origin string) {
		s.MustTmux(t, "rename-window", "-t", origin, "a-popup-closed-by-its-hotkey-leaves-the-client")
		if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", origin).CombinedOutput(); err != nil {
			t.Fatalf("flag: %v: %s", err, out)
		}
		s.MustTmux(t, "set-option", "-p", "-t", origin, "@attention_at", strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10))
	})
	screen := func() string { return s.MustTmux(t, "capture-pane", "-p", "-t", panel) }
	s.WaitForContent(t, panel, popupWhole)
	s.MustTmux(t, "send-keys", "-t", panel, "-l", "zzzq")
	tmuxtest.WaitFor(t, 5*time.Second, "no rows for the search", func() bool { return !strings.Contains(screen(), "hotkey") })
	s.MustTmux(t, "send-keys", "-t", panel, "C-u")
	tmuxtest.WaitFor(t, 5*time.Second, "the rows back, the client's row open", func() bool { return strings.Contains(screen(), popupWhole) })
}

// A reload that lands while a move's reload runs (an age's tick, a flag,
// the resize of the panel's join, a key of the search) opens the row the
// cursor moved to: fzf kills the move's reload for the new one, which
// opened the row the cursor had left. Here the new reload is the first of
// a search typed at once, whose cursor stays on its first row: a move
// within a window switches no window, so no place opens the cursor's row
// later. The tmux server stopped holds the move's reload until the search
// starts; the move and the search go to fzf's socket, and the move's own
// go would take the client nowhere. The flags are two hours old: no age
// ticks.
func TestPanelReloadDuringMoveOpensCursorRow(t *testing.T) {
	s, _, panel := openPanelAfter(t, func(s *tmuxtest.Server, origin string) {
		s.MustTmux(t, "rename-window", "-t", origin, "a-split-window-whose-name-is-cut-in-the-panel")
		second := s.MustTmux(t, "split-window", "-d", "-t", origin, "-P", "-F", "#{pane_id}")
		old := strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)
		for i, p := range []string{origin, second} {
			for _, args := range [][]string{{"flag", "working", "--pane", p}, {"report", "--source", "git", "--text", "⎇ a-long-branch-name-for-the-pane-" + strconv.Itoa(i) + "-here", "--pane", p}} {
				if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
					t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
				}
			}
			s.MustTmux(t, "set-option", "-p", "-t", p, "@attention_at", old)
		}
	})
	screen := func() string { return s.MustTmux(t, "capture-pane", "-p", "-t", panel) }
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("panel:\n%s", screen())
		}
	})
	socket := s.Display(t, panel, "#{"+sidebar.OptListen+"}")
	// The only open row is the cursor's, whose lines have want.
	open := func(want string) func() bool {
		return func() bool {
			n := 0
			for _, l := range strings.Split(screen(), "\n") {
				if strings.Contains(l, "in-the-panel") || strings.Contains(l, "-pane-0-here") || strings.Contains(l, "-pane-1-here") {
					if n++; !strings.HasPrefix(l, "▌") || !strings.Contains(l, want) {
						return false
					}
				}
			}
			return n == 1
		}
	}
	tmuxtest.WaitFor(t, 5*time.Second, "the window's row open", open("in-the-panel"))
	s.MustTmux(t, "send-keys", "-t", panel, "Down")
	tmuxtest.WaitFor(t, 5*time.Second, "the first pane's row open", open("-pane-0-here"))

	server, err := strconv.Atoi(s.Display(t, panel, "#{pid}"))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(server, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	stopped := true
	resume := func() {
		if stopped {
			stopped = false
			if err := syscall.Kill(server, syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
		}
	}
	defer resume()
	// fzf takes a post when it has done the one before, the focus event's
	// reload included.
	panelPost(t, socket, "up")
	panelPost(t, socket, "put(a-split)")
	resume()
	tmuxtest.WaitFor(t, 5*time.Second, "the search's rows, the window's row open", func() bool {
		return strings.Contains(screen(), "a-split\n") && open("in-the-panel")()
	})
}

// panelPost sends actions to the panel's fzf on its socket.
func panelPost(t *testing.T, socket, actions string) {
	t.Helper()
	hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := hc.Post("http://fzf/", "text/plain", strings.NewReader(actions))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post %q: %s", actions, resp.Status)
	}
}

// A click and the wheel open the row they put the cursor on, as the
// arrows do. After a held key the cursor and the client stop together
// on one row, which is open.
func TestPanelOpensRowOnMouseAndHeldKey(t *testing.T) {
	s, _, panel, socket, _ := openPanelRows(t)
	screen := func() string { return s.MustTmux(t, "capture-pane", "-p", "-t", panel) }
	// The panel's line of a row's text, from 1, for fzf's mouse.
	lineOf := func(text string) int {
		for i, l := range strings.Split(screen(), "\n") {
			if strings.Contains(l, text) {
				return i + 1
			}
		}
		t.Fatalf("no %q in the panel:\n%s", text, screen())
		return 0
	}
	y := lineOf("a-popup-closed")
	s.MustTmux(t, "send-keys", "-t", panel, "-l", fmt.Sprintf("\x1b[<0;10;%dM", y))
	s.MustTmux(t, "send-keys", "-t", panel, "-l", fmt.Sprintf("\x1b[<0;10;%dm", y))
	tmuxtest.WaitFor(t, 5*time.Second, "the clicked row open", func() bool {
		sc := screen()
		return panelCursor(t, socket) == "=proj-a:1" && strings.Contains(sc, popupWhole) && strings.Contains(sc, splitCut)
	})
	s.MustTmux(t, "send-keys", "-t", panel, "-l", fmt.Sprintf("\x1b[<65;10;%dM", lineOf("a-split-window")))
	tmuxtest.WaitFor(t, 5*time.Second, "the row the wheel went to open", func() bool {
		sc := screen()
		return strings.HasPrefix(panelCursor(t, socket), "=proj-a:2") && strings.Contains(sc, splitWhole) && strings.Contains(sc, popupCut)
	})

	for _, k := range []string{"Down", "Up", "Up", "Down", "Up", "Down", "Down", "Up", "Up"} {
		s.MustTmux(t, "send-keys", "-t", panel, k)
		time.Sleep(30 * time.Millisecond) // a held key's repeat
	}
	tmuxtest.WaitFor(t, 5*time.Second, "the cursor and the client on the first row, open", func() bool {
		client := s.MustTmux(t, "list-clients", "-F", "=#{session_name}:#{window_index}")
		sc := screen()
		return panelCursor(t, socket) == "=proj-a:1" && client == "=proj-a:1" && strings.Contains(sc, popupWhole) && strings.Contains(sc, splitCut)
	})
}

// In a window split into two agent panes, only the row of the cursor's
// pane opens, also on a move between the two rows, which takes the
// client nowhere: the panel opens in that window, on the window's row.
// The flags are two hours old: no age ticks, so no reload but the
// move's own comes.
func TestPanelOpensPaneRow(t *testing.T) {
	var panes [2]string // by pane index
	s, _, panel := openPanelAfter(t, func(s *tmuxtest.Server, origin string) {
		second := s.MustTmux(t, "split-window", "-d", "-t", origin, "-P", "-F", "#{pane_id}")
		panes = [2]string{origin, second}
		old := strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)
		for i, p := range panes {
			for _, args := range [][]string{{"flag", "working", "--pane", p}, {"report", "--source", "git", "--text", "⎇ a-long-branch-name-for-the-pane-" + strconv.Itoa(i) + "-here", "--pane", p}} {
				if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
					t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
				}
			}
			s.MustTmux(t, "set-option", "-p", "-t", p, "@attention_at", old)
		}
	})
	screen := func() string { return s.MustTmux(t, "capture-pane", "-p", "-t", panel) }
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("panel:\n%s", screen())
		}
	})
	s.WaitForContent(t, panel, "2h")
	if sc := screen(); strings.Contains(sc, "-pane-0-here") || strings.Contains(sc, "-pane-1-here") {
		t.Fatalf("a pane's row open with the cursor on the window's:\n%s", sc)
	}
	open := func(want, not string) func() bool {
		return func() bool { sc := screen(); return strings.Contains(sc, want) && !strings.Contains(sc, not) }
	}
	s.MustTmux(t, "send-keys", "-t", panel, "Down") // from the window's row to its first pane's
	tmuxtest.WaitFor(t, 5*time.Second, "the first pane's row open, the second cut", open("-pane-0-here", "-pane-1-here"))
	s.MustTmux(t, "send-keys", "-t", panel, "Down")
	tmuxtest.WaitFor(t, 5*time.Second, "the second pane's row open, the first cut", open("-pane-1-here", "-pane-0-here"))
}

// A task's row takes the client to the window of the pane it stands for:
// the one that needs the user.
func TestPanelTaskRowGoesToWaitingPane(t *testing.T) {
	s, _, panel := openPanelAfter(t, func(s *tmuxtest.Server, origin string) {
		for _, args := range [][]string{
			{"start", "--session", "proj-a", "--name", "task-one-a", "--task", "t1", "--source", "wb", "--title", "t1-title", "--", "sleep", "99999"},
			{"start", "--session", "proj-a", "--name", "task-one-b", "--task", "t1", "--source", "wb", "--", "sleep", "99999"},
		} {
			if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
				t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
			}
		}
		for name, args := range map[string][]string{
			"task-one-a": {"flag", "working"},
			"task-one-b": {"flag", "needs-you", "--reason", "permission"},
		} {
			p := s.Display(t, "=proj-a:"+name, "#{pane_id}")
			if out, err := s.Command(t, origin, "tmux-agent", append(args, "--pane", p)...).CombinedOutput(); err != nil {
				t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
			}
		}
	})
	s.WaitForContent(t, panel, "t1-title")
	s.MustTmux(t, "send-keys", "-t", panel, "Up") // from the fold to the task's row
	tmuxtest.WaitFor(t, 5*time.Second, "the client in task-one-b", func() bool {
		return s.MustTmux(t, "list-clients", "-F", "#{window_name}") == "task-one-b"
	})
}

// A split window's open row stays open when one of its two flagged panes
// loses its flag: the row then stands for the other pane, and shows
// whole the whole time.
func TestPanelSplitRowStaysOpen(t *testing.T) {
	var panes [2]string
	s, origin, panel := openPanelAfter(t, func(s *tmuxtest.Server, origin string) {
		s.MustTmux(t, "rename-window", "-t", origin, "split-window-with-two-agents-in-its-panes")
		second := s.MustTmux(t, "split-window", "-d", "-t", origin, "-P", "-F", "#{pane_id}")
		panes = [2]string{origin, second}
		for _, p := range panes {
			if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", p).CombinedOutput(); err != nil {
				t.Fatalf("flag: %v: %s", err, out)
			}
		}
	})
	screen := func() string { return s.MustTmux(t, "capture-pane", "-p", "-t", panel) }
	const whole, cut = "agents-in-\n", "agents-in-…"
	s.WaitForContent(t, panel, "its-panes")
	if out, err := s.Command(t, origin, "tmux-agent", "flag", "clear", "--pane", panes[1]).CombinedOutput(); err != nil {
		t.Fatalf("flag: %v: %s", err, out)
	}
	for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if sc := screen(); strings.Contains(sc, cut) || !strings.Contains(sc, whole) {
			t.Fatalf("the open row showed cut:\n%s", sc)
		}
	}
}

// When the split beside a window's one agent pane gets a flag, the window's
// row becomes a header over a row per pane; only the cursor's row is open,
// and it never shows cut.
func TestPanelOneRowOpensAsWindowSplits(t *testing.T) {
	var second string
	s, origin, panel := openPanelAfter(t, func(s *tmuxtest.Server, origin string) {
		s.MustTmux(t, "rename-window", "-t", origin, "a-split-window-whose-name-is-cut-in-the-panel")
		second = s.MustTmux(t, "split-window", "-d", "-h", "-t", origin, "-P", "-F", "#{pane_id}")
		for _, args := range [][]string{
			{"flag", "working", "--pane", origin},
			{"report", "--source", "git", "--text", "⎇ a-long-branch-name-for-the-first-pane-here", "--pane", origin},
		} {
			if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
				t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
			}
		}
	})
	s.WaitForContent(t, panel, "first-pane-here")
	if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", second).CombinedOutput(); err != nil {
		t.Fatalf("flag: %v: %s", err, out)
	}
	for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		sc := s.MustTmux(t, "capture-pane", "-p", "-t", panel)
		for _, l := range strings.Split(sc, "\n") {
			cursor := strings.HasPrefix(l, "▌")
			if (!cursor && strings.Contains(l, "cut-in-the-panel")) || (cursor && strings.Contains(l, "…")) {
				t.Fatalf("a row other than the cursor's open, or the cursor's cut:\n%s", sc)
			}
		}
	}
}

// The panel's cursor on an agent pane's row takes the client to that
// pane, in a window where another pane has the focus: the README's split
// keys, pressed in the panel, then split the agent's pane.
func TestPanelGoesToRowPane(t *testing.T) {
	var agent string
	s, _, panel := openPanelAfter(t, func(s *tmuxtest.Server, origin string) {
		shellPane := s.MustTmux(t, "new-window", "-d", "-t", "=proj-a:", "-n", "w-split", "-P", "-F", "#{pane_id}")
		agent = s.MustTmux(t, "split-window", "-d", "-h", "-t", shellPane, "-P", "-F", "#{pane_id}")
		if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", agent).CombinedOutput(); err != nil {
			t.Fatalf("flag: %v: %s", err, out)
		}
	})
	s.MustTmux(t, "send-keys", "-t", panel, "Up") // from the fold to w-split's row
	tmuxtest.WaitFor(t, 5*time.Second, "the client on "+agent, func() bool {
		return s.MustTmux(t, "show-options", "-wqv", "-t", "=proj-a:w-split", sidebar.OptLast) == agent
	})
}

// The panel's cursor on the row of a window with no agent, in an opened
// fold, keeps the pane that window had focused: the README's split keys
// then split that pane.
func TestPanelKeepsFocusedPaneOfPlainWindow(t *testing.T) {
	var focused string
	s, _, panel := openPanelAfter(t, func(s *tmuxtest.Server, origin string) {
		p := s.MustTmux(t, "new-window", "-t", "=proj-a:", "-n", "agent-one", "-P", "-F", "#{pane_id}")
		if out, err := s.Command(t, origin, "tmux-agent", "flag", "working", "--pane", p).CombinedOutput(); err != nil {
			t.Fatalf("flag: %v: %s", err, out)
		}
		left := s.MustTmux(t, "new-window", "-d", "-t", "=proj-a:", "-n", "w-two", "-P", "-F", "#{pane_id}")
		focused = s.MustTmux(t, "split-window", "-d", "-h", "-t", left, "-P", "-F", "#{pane_id}")
		s.MustTmux(t, "select-pane", "-t", focused)
	})
	s.MustTmux(t, "send-keys", "-t", panel, "Down") // onto the fold
	tmuxtest.WaitFor(t, 5*time.Second, "the cursor on the fold", func() bool {
		return strings.Contains(s.MustTmux(t, "capture-pane", "-p", "-t", panel), "▌   2 more windows")
	})
	s.MustTmux(t, "send-keys", "-t", panel, "Enter")
	s.WaitForContent(t, panel, "w-two")
	s.MustTmux(t, "send-keys", "-t", panel, "Down", "Down") // onto w-two's row
	tmuxtest.WaitFor(t, 5*time.Second, "the client on w-two's "+focused, func() bool {
		return s.MustTmux(t, "list-clients", "-F", "#{window_name}") == "w-two" &&
			s.MustTmux(t, "show-options", "-wqv", "-t", "=proj-a:w-two", sidebar.OptLast) == focused
	})
}

// A window in two sessions (a group of sessions, or link-window) has a
// row in each, and place keeps the panel's cursor on the row of the
// client's session: the two rows had one key, and place put the cursor
// on the first. The rows: proj-a, its w-one, proj-b, its w-one.
func TestPanelPlacesSharedWindowInClientsSession(t *testing.T) {
	for _, c := range []struct {
		name   string
		share  [][]string
		window string
	}{
		{"group", [][]string{{"new-session", "-d", "-t", "proj-a", "-s", "proj-b"}}, "=proj-b:0"},
		{"link", [][]string{{"new-session", "-d", "-s", "proj-b"}, {"link-window", "-s", "=proj-a:0", "-t", "=proj-b:5"}}, "=proj-b:5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tmuxtest.Require(t, "tmux", "fzf")
			sb := tmuxtest.NewSandbox(t)
			selftest.Install(t, sb)
			sb.Setenv("TMUX_AGENT_DEBUG", "1")
			t.Cleanup(func() {
				if t.Failed() {
					data, err := os.ReadFile(filepath.Join(sb.StateHome, "tmux-agent", "log"))
					t.Logf("tmux-agent log (%v):\n%s", err, data)
				}
			})
			s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
			origin := s.Display(t, "=proj-a:0", "#{pane_id}")
			s.MustTmux(t, "rename-window", "-t", "=proj-a:0", "w-one")
			for _, args := range c.share {
				s.MustTmux(t, args...)
			}
			s.Attach(t, "proj-b", 120, 31)
			s.MustTmux(t, "select-window", "-t", c.window)
			client := s.MustTmux(t, "list-clients", "-F", "#{client_name}")
			for _, args := range [][]string{{"flag", "needs-you", "--pane", origin}, {"tmux-init"}, {"popup", client}} {
				if out, err := s.Command(t, origin, "tmux-agent", args...).CombinedOutput(); err != nil {
					t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
				}
			}
			var panel string
			tmuxtest.WaitFor(t, 5*time.Second, "the panel", func() bool {
				// Once for each session that holds its window.
				if ps := strings.Fields(s.MustTmux(t, "list-panes", "-a", "-F", "#{?#{"+sidebar.OptClient+"},#{pane_id},}")); len(ps) > 0 {
					panel = ps[0]
				}
				return panel != ""
			})
			s.WaitForContent(t, panel, "proj-b")
			place := func() string {
				out, err := s.Command(t, panel, "tmux-agent", "pick-key", "place", client).CombinedOutput()
				if err != nil {
					t.Fatalf("pick-key place: %v: %s", err, out)
				}
				return string(out)
			}
			var first string
			tmuxtest.WaitFor(t, 5*time.Second, "the rows noted", func() bool { first = place(); return first != "" })
			// The second reads the row the first, or the panel's own, noted.
			for _, got := range []string{first, place()} {
				if !strings.Contains(got, "pos(4)") {
					t.Errorf("place = %q, want the cursor on proj-b's w-one, pos(4)", got)
				}
			}
		})
	}
}
