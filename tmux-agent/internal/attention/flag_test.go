package attention

import (
	"context"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// A failed run is a done flag with a mark: the pane and its window carry
// it, the counts show it apart, and it goes with the flag or with a done
// that did not fail.
func TestFailedMark(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	p1 := s.Display(t, "proj-a:0.0", "#{pane_id}")
	p2 := s.MustTmux(t, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", p1)
	s.MustTmux(t, "rename-window", "-t", p1, "w-a")
	c := &Core{Tmux: s.Client}
	ctx := context.Background()

	set := func(pane string, f Flag) {
		t.Helper()
		if _, _, err := c.Set(ctx, pane, f); err != nil {
			t.Fatal(err)
		}
	}
	want := func(step, paneMark, winMark, status string) {
		t.Helper()
		got := s.Display(t, p1, "#{@attention_failed}|#{@attention_win_failed}|#{@attention_status}")
		if w := paneMark + "|" + winMark + "|" + status; got != w {
			t.Errorf("%s: pane mark | window mark | status = %q, want %q", step, got, w)
		}
	}
	failed := Flag{State: Done, Reason: FailedPrefix + "exit 3", Failed: true}

	set(p1, failed)
	want("failed", "1", "1", "#[fg=red]✗ 1  #[fg=default]│")
	title, body, ok, err := c.Notification(ctx, p1, Done)
	if err != nil || !ok || title != "w-a failed" || body != "exit 3" {
		t.Errorf("notification = %q %q %v %v, want \"w-a failed\" \"exit 3\"", title, body, ok, err)
	}

	set(p2, Flag{State: Done, Reason: "turn ended"})
	want("beside a done", "1", "1", "#[fg=red]✗ 1  #[fg=green]✓ 1  #[fg=default]│")

	set(p1, Flag{State: Done, Reason: "turn ended"})
	want("done again without failing", "", "", "#[fg=green]✓ 2  #[fg=default]│")

	set(p1, failed)
	if err := c.Seen(ctx, p1); err != nil {
		t.Fatal(err)
	}
	want("seen", "", "", "#[fg=green]✓ 1  #[fg=default]│")

	if _, _, err := c.Set(ctx, p1, Flag{State: Working, Failed: true}); err == nil || !strings.Contains(err.Error(), "only a done flag") {
		t.Errorf("a failed working flag: err = %v, want refused", err)
	}
}
