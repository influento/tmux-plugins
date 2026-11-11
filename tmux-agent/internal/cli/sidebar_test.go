package cli_test

import (
	"encoding/json"
	"strings"
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

	s.MustTmux(t, "send-keys", "-t", panel, "Escape")
	tmuxtest.WaitFor(t, 5*time.Second, "the panel to close", func() bool { return panelIn() == "" })
	if got := s.MustTmux(t, "list-clients", "-F", "#{pane_id}"); got != origin {
		t.Errorf("client on %s after Esc, want %s, where the panel opened", got, origin)
	}
	if got := s.MustTmux(t, "show-options", "-gqv", sidebar.OptOpen); got != "" {
		t.Errorf("%s = %q with no panel open", sidebar.OptOpen, got)
	}
}
