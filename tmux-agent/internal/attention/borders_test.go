package attention

import (
	"context"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// With @attention_borders off, a split window gets no label line: Borders
// leaves it to the global pane-border-status, and takes back one it set.
func TestBordersOff(t *testing.T) {
	tmuxtest.Require(t, "tmux")
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	c := &Core{Tmux: s.Client}
	ctx := context.Background()
	win := s.MustTmux(t, "display-message", "-p", "-t", "=proj-a:", "#{window_id}")
	s.MustTmux(t, "split-window", "-t", win)
	status := func() string {
		return s.MustTmux(t, "show-options", "-wqv", "-t", win, "pane-border-status")
	}
	if err := c.Borders(ctx, win); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "top" {
		t.Fatalf("before: pane-border-status = %q, want top", got)
	}
	s.MustTmux(t, "set-option", "-g", OptBorders, "off")
	if err := c.Borders(ctx, win); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "" {
		t.Errorf("off: the window's pane-border-status = %q, want none of its own", got)
	}
}
