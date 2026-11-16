package cli_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// The panel's rows follow its width: rows read while it was zoomed (the
// window's full width) are read again when it is back at its own. A
// panel alone in its window, which a closed window leaves, is as wide.
func TestPanelRowsFollowItsWidth(t *testing.T) {
	s, origin, panel := openPanel(t)
	if out, err := s.Command(t, origin, "tmux-agent", "flag", "done", "--pane", origin).CombinedOutput(); err != nil {
		t.Fatalf("flag: %v: %s", err, out)
	}
	s.WaitForContent(t, panel, "w-one")
	// Two hours old: the next tick, which reads the rows again, is an
	// hour away.
	s.MustTmux(t, "set-option", "-p", "-t", origin, "@attention_at", strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10))

	s.MustTmux(t, "resize-pane", "-Z", "-t", panel)
	s.MustTmux(t, "rename-window", "-t", "=proj-a:0", "w-wide") // a reload at the full width
	tmuxtest.WaitFor(t, 5*time.Second, "rows read at the full width", func() bool {
		for _, l := range strings.Split(s.MustTmux(t, "capture-pane", "-p", "-t", panel), "\n") {
			if strings.Contains(l, "w-wide") && strings.HasSuffix(strings.TrimRight(l, " "), "2h") && len(l) > 60 {
				return true
			}
		}
		return false
	})
	s.MustTmux(t, "resize-pane", "-Z", "-t", panel)
	tmuxtest.WaitFor(t, 5*time.Second, "rows that fit the panel again", func() bool {
		text := s.MustTmux(t, "capture-pane", "-p", "-t", panel)
		return strings.Contains(text, " 2h") && !strings.Contains(text, "…")
	})
}
