package agent

import (
	"context"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/git"
)

// gaugeAgent reports what its fields hold, and counts the reads.
type gaugeAgent struct {
	Generic
	used, limit *int64
	reads       *int
	env         *string
}

func (g gaugeAgent) Context(_, _, _ string, getenv func(string) string) (int64, int64, bool) {
	*g.reads++
	*g.env = getenv("TEST_GAUGE")
	return *g.used, *g.limit, *g.used > 0
}

// The context share is read at a session start, a compaction and the main
// agent's turn end, not on a tool call or a subagent's events. It is the
// share of the limit read each time, writes and reloads the panels only
// when it changes, and goes with the session.
func TestContextShare(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a", Command: "exec bash -c 'exec -a fakeagent sleep 600'"})
	pane := s.Display(t, "proj-a:0.0", "#{pane_id}")
	tmuxtest.WaitFor(t, 5*time.Second, "the pane to run fakeagent", func() bool {
		return s.Display(t, pane, "#{pane_current_command}") == "fakeagent"
	})
	var used, limit int64
	reads, env, reloads := 0, "", 0
	a := gaugeAgent{Generic: "fakeagent", used: &used, limit: &limit, reads: &reads, env: &env}
	e := &Env{
		Tmux: s.Client, Registry: NewRegistry(), Pane: pane,
		Core:   &attention.Core{Tmux: s.Client, Changed: func(context.Context) { reloads++ }},
		Nested: func([]string, int) bool { return false },
		Git:    func(context.Context, string) (git.Head, error) { return git.Head{}, git.ErrNotARepo },
		Getenv: func(k string) string { return map[string]string{"TEST_GAUGE": "seen"}[k] },
	}
	ctx := context.Background()
	for _, step := range []struct {
		name        string
		ev          Event
		used, limit int64
		want        string
		reads       int
		reload      bool
	}{
		{"start", Event{Kind: SessionStart, SessionID: "s-1"}, 40_000, 400_000, "10", 1, true},
		{"a tool", Event{Kind: Working, SessionID: "s-1"}, 90_000, 400_000, "10", 1, false},
		{"turn end", Event{Kind: TurnEnd, SessionID: "s-1"}, 92_000, 400_000, "23", 2, true},
		{"same share", Event{Kind: TurnEnd, SessionID: "s-1"}, 93_000, 400_000, "23", 3, false},
		{"a subagent's turn end", Event{Kind: TurnEnd, SessionID: "s-1", By: "sub-a"}, 300_000, 400_000, "23", 3, false},
		{"resumed, same share", Event{Kind: SessionStart, SessionID: "s-1", Resume: true}, 93_000, 400_000, "23", 4, false},
		{"compaction", Event{Kind: Compacted, SessionID: "s-1"}, 30_000, 400_000, "8", 5, true},
		{"the limit changed", Event{Kind: TurnEnd, SessionID: "s-1"}, 30_000, 200_000, "15", 6, true},
		{"under half a percent", Event{Kind: TurnEnd, SessionID: "s-1"}, 100, 200_000, "1", 7, true},
		{"no call yet", Event{Kind: TurnEnd, SessionID: "s-1"}, 0, 200_000, "", 8, true},
		{"again", Event{Kind: TurnEnd, SessionID: "s-1"}, 190_000, 200_000, "95", 9, true},
		{"end", Event{Kind: SessionEnd, SessionID: "s-1", End: EndExited}, 190_000, 200_000, "", 9, true},
	} {
		used, limit, reloads = step.used, step.limit, 0
		if err := e.Apply(ctx, a, step.ev); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		got := s.Display(t, pane, "#{"+attention.OptContext+"}")
		if got != step.want || reads != step.reads {
			t.Errorf("%s: share %q after %d reads, want %q after %d", step.name, got, reads, step.want, step.reads)
		}
		if step.reload && reloads == 0 || !step.reload && step.ev.Kind != SessionStart && step.ev.Kind != Working && reloads != 0 {
			t.Errorf("%s: %d panel reloads, want reload %v", step.name, reloads, step.reload)
		}
	}
	if env != "seen" {
		t.Errorf("the gauge read %q from the environment, want the Env's", env)
	}
}
