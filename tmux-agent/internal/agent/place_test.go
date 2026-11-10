package agent

import (
	"context"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/git"
)

// The git label follows the agent's folder: set at the session's start,
// read again at each turn end and when the folder changes (a worktree),
// not on every tool call; gone when the session ends.
func TestGitLabelFollowsTheFolder(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a", Command: "exec bash -c 'exec -a fakeagent sleep 600'"})
	pane := s.Display(t, "proj-a:0.0", "#{pane_id}")
	tmuxtest.WaitFor(t, 5*time.Second, "the pane to run fakeagent", func() bool {
		return s.Display(t, pane, "#{pane_current_command}") == "fakeagent"
	})
	heads := map[string]git.Head{
		"/src/proj-a":                  {Branch: "main"},
		"/src/proj-a/.wt/feat-a":       {Branch: "feat-a", Worktree: true},
		"/src/proj-a/.wt/feat-a/inner": {Branch: "feat-a", Worktree: true},
	}
	calls := 0
	e := &Env{
		Tmux: s.Client, Core: &attention.Core{Tmux: s.Client}, Registry: NewRegistry(), Pane: pane,
		Nested: func([]string, int) bool { return false },
		Git: func(_ context.Context, dir string) (git.Head, error) {
			calls++
			if h, ok := heads[dir]; ok {
				return h, nil
			}
			return git.Head{}, git.ErrNotARepo
		},
	}
	ctx := context.Background()
	a := Generic("fakeagent")
	for _, step := range []struct {
		name  string
		ev    Event
		want  string
		calls int
	}{
		{"start", Event{Kind: SessionStart, Cwd: "/src/proj-a"}, "⎇ main", 1},
		{"a tool, same folder", Event{Kind: Working, Cwd: "/src/proj-a"}, "⎇ main", 1},
		{"into a worktree", Event{Kind: Working, Cwd: "/src/proj-a/.wt/feat-a"}, "⎇ feat-a wt", 2},
		{"turn end", Event{Kind: TurnEnd, Cwd: "/src/proj-a/.wt/feat-a"}, "⎇ feat-a wt", 3},
		{"no folder", Event{Kind: Working}, "⎇ feat-a wt", 3},
		{"outside git", Event{Kind: Working, Cwd: "/tmp/scratch"}, "", 4},
		{"back", Event{Kind: TurnEnd, Cwd: "/src/proj-a"}, "⎇ main", 5},
		{"end", Event{Kind: SessionEnd, End: EndExited}, "", 5},
	} {
		if err := e.Apply(ctx, a, step.ev); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		got := ""
		for _, l := range attention.ParseLabels(s.Display(t, pane, "#{@labels}"), 0) {
			if l.Source == GitSource {
				got = l.Shown()
			}
		}
		if got != step.want || calls != step.calls {
			t.Errorf("%s: label %q after %d git calls, want %q after %d", step.name, got, calls, step.want, step.calls)
		}
	}
}
