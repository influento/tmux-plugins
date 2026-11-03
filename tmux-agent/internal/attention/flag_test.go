package attention

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// panes starts a server with one window of two panes, named w-a.
func panes(t *testing.T) (s *tmuxtest.Server, p1, p2 string) {
	t.Helper()
	s = tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	p1 = s.Display(t, "proj-a:0.0", "#{pane_id}")
	p2 = s.MustTmux(t, "split-window", "-d", "-P", "-F", "#{pane_id}", "-t", p1)
	s.MustTmux(t, "rename-window", "-t", p1, "w-a")
	return s, p1, p2
}

// wantMarks checks a pane's failed and drawn marks, its window's, and the
// status counts.
func wantMarks(t *testing.T, s *tmuxtest.Server, step, pane, paneFailed, paneMark, winFailed, winMark, status string) {
	t.Helper()
	format := "#{@attention_failed}|#{@attention_mark}|#{@attention_win_failed}|#{@attention_win_mark}|#{@attention_status}"
	got := s.Display(t, pane, format)
	if want := strings.Join([]string{paneFailed, paneMark, winFailed, winMark, status}, "|"); got != want {
		t.Errorf("%s: pane failed|mark|window failed|mark|status =\n  %q\nwant\n  %q", step, got, want)
	}
}

// A failed run is a done flag with a mark: the pane and its window carry
// it, the counts show it apart, and it goes with the flag or with a done
// that did not fail. Each pane and window carries the mark tmux.conf draws.
func TestFailedMark(t *testing.T) {
	s, p1, p2 := panes(t)
	c := &Core{Tmux: s.Client}
	ctx := context.Background()
	set := func(pane string, f Flag) {
		t.Helper()
		if _, _, err := c.Set(ctx, pane, f); err != nil {
			t.Fatal(err)
		}
	}
	const red, green, blue = "#[fg=red]", "#[fg=green]", "#[fg=blue]"
	failed := Flag{State: Done, Reason: FailedPrefix + "exit 3", Failed: true}

	set(p1, Flag{State: Working})
	wantMarks(t, s, "working", p1, "", blue+"● ", "", blue+"● ", blue+"● 1  #[fg=default]│")

	set(p1, failed)
	wantMarks(t, s, "failed", p1, "1", red+"✗ ", "1", red+"✗ ", red+"✗ 1  #[fg=default]│")
	title, body, ok, err := c.Notification(ctx, p1, Done)
	if err != nil || !ok || title != "w-a failed" || body != "exit 3" {
		t.Errorf("notification = %q %q %v %v, want \"w-a failed\" \"exit 3\"", title, body, ok, err)
	}

	set(p2, Flag{State: Done, Reason: "turn ended"})
	wantMarks(t, s, "beside a done", p2, "", green+"✓ ", "1", red+"✗ ", red+"✗ 1  "+green+"✓ 1  #[fg=default]│")

	set(p2, Flag{State: NeedsYou})
	wantMarks(t, s, "beside a needs-you", p2, "", red+"? ", "", red+"? ", red+"? 1  "+red+"✗ 1  #[fg=default]│")

	set(p2, Flag{State: Done, Reason: "turn ended"})
	set(p1, Flag{State: Done, Reason: "turn ended"})
	wantMarks(t, s, "done again without failing", p1, "", green+"✓ ", "", green+"✓ ", green+"✓ 2  #[fg=default]│")

	set(p1, failed)
	if err := c.Seen(ctx, p1); err != nil {
		t.Fatal(err)
	}
	wantMarks(t, s, "seen", p1, "", "", "", green+"✓ ", green+"✓ 1  #[fg=default]│")

	if _, _, err := c.Set(ctx, p1, Flag{State: Working, Failed: true}); err == nil || !strings.Contains(err.Error(), "only a done flag") {
		t.Errorf("a failed working flag: err = %v, want refused", err)
	}
}

// A mark keeps the colours it was drawn in; Redraw draws them all in the
// theme's colours now.
func TestRedrawUsesTheTheme(t *testing.T) {
	s, p1, p2 := panes(t)
	c := &Core{Tmux: s.Client}
	ctx := context.Background()
	if _, _, err := c.Set(ctx, p1, Flag{State: Done, Reason: FailedPrefix + "exit 1", Failed: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Set(ctx, p2, Flag{State: Working}); err != nil {
		t.Fatal(err)
	}
	s.MustTmux(t, "set-option", "-g", "@attention_fg_failed", "#aa0000", ";",
		"set-option", "-g", "@attention_fg_working", "#0000aa")
	// A stale mark on a pane without a flag goes too.
	p3 := s.MustTmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "proj-a")
	s.MustTmux(t, "set-option", "-p", "-t", p3, "@attention_mark", "#[fg=red]? ")
	if err := c.Redraw(ctx); err != nil {
		t.Fatal(err)
	}
	// The window shows its best-ranked pane: done ranks before working.
	wantMarks(t, s, "p1", p1, "1", "#[fg=#aa0000]✗ ", "1", "#[fg=#aa0000]✗ ",
		"#[fg=#0000aa]● 1  #[fg=#aa0000]✗ 1  #[fg=default]│")
	wantMarks(t, s, "p2", p2, "", "#[fg=#0000aa]● ", "1", "#[fg=#aa0000]✗ ",
		"#[fg=#0000aa]● 1  #[fg=#aa0000]✗ 1  #[fg=default]│")
	if got := s.Display(t, p3, "#{@attention_mark}|#{@attention_win_mark}"); got != "|" {
		t.Errorf("idle pane marks = %q, want none", got)
	}
}

// A flag whose owner is gone is swept, and its marks with it.
func TestSweepDropsMarks(t *testing.T) {
	s, p1, _ := panes(t)
	c := &Core{Tmux: s.Client}
	ctx := context.Background()
	if _, _, err := c.Set(ctx, p1, Flag{State: Working, Owner: "fakeagent"}); err != nil {
		t.Fatal(err)
	}
	// Set sweeps as it renders the counts, and the pane runs sh, not the
	// owner: the flag is gone by the time Set returns.
	tmuxtest.WaitFor(t, 5*time.Second, "the sweep", func() bool {
		return s.Display(t, p1, "#{@attention}") == ""
	})
	wantMarks(t, s, "swept", p1, "", "", "", "", "")
}

// tmux refuses a command over about 16 KiB or 1,000 arguments, so Redraw
// must split its work on a server with many windows.
func TestRedrawManyWindows(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	c := &Core{Tmux: s.Client}
	const n = 80
	var args []string
	for i := range n {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "new-window", "-d", "-t", "proj-a")
	}
	s.MustTmux(t, args...)
	out := s.MustTmux(t, "list-panes", "-s", "-t", "proj-a", "-F", "#{pane_id}")
	panes := strings.Fields(out)
	if len(panes) != n+1 {
		t.Fatalf("panes = %d, want %d", len(panes), n+1)
	}
	args = nil
	for i, p := range panes {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option", "-p", "-t", p, "@attention", "working")
	}
	s.MustTmux(t, args...)
	if err := c.Redraw(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{panes[0], panes[n]} {
		if got := s.Display(t, p, "#{@attention_mark}|#{@attention_win_mark}"); got != "#[fg=blue]● |#[fg=blue]● " {
			t.Errorf("%s marks = %q, want working marks", p, got)
		}
	}
}
