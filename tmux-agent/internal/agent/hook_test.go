package agent

import (
	"context"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// A decision pending on one tool stays when another tool, run beside it,
// finishes; it goes when its own tool finishes, or when any tool or turn
// starts, since the agent then went on.
func TestWorkingKeepsAPendingDecision(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   Event
		want attention.State
	}{
		{"another tool finished", Event{Kind: Working, Tool: "k2", ToolEnd: true}, attention.NeedsYou},
		{"its own tool finished", Event{Kind: Working, Tool: "k1", ToolEnd: true}, attention.Working},
		{"a tool started", Event{Kind: Working, Tool: "k2"}, attention.Working},
		{"a turn started", Event{Kind: Working}, attention.Working},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			// The flag lives with its owner, the pane's command.
			s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a", Command: "exec bash -c 'exec -a fakeagent sleep 600'"})
			pane := s.Display(t, "proj-a:0.0", "#{pane_id}")
			core := &attention.Core{Tmux: s.Client}
			tmuxtest.WaitFor(t, 5*time.Second, "the pane to run fakeagent", func() bool {
				return s.Display(t, pane, "#{pane_current_command}") == "fakeagent"
			})
			e := &Env{
				Tmux: s.Client, Core: core, Registry: NewRegistry(), Pane: pane,
				Nested: func([]string, int) bool { return false },
			}
			ctx := context.Background()
			a := Generic("fakeagent")
			if err := e.Apply(ctx, a, Event{Kind: NeedsInput, Tool: "k1", Reason: "Bash(make)"}); err != nil {
				t.Fatal(err)
			}
			if err := e.Apply(ctx, a, c.ev); err != nil {
				t.Fatal(err)
			}
			got, _, err := core.FlagOf(ctx, pane)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("flag = %s, want %s", got, c.want)
			}
		})
	}
}
