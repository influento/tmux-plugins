package agent

import (
	"context"
	"testing"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// A turn held by runs the agent started ends when the runs do; a turn the
// agent takes meanwhile is its own, and the runs' end leaves it working.
func TestHeldTurn(t *testing.T) {
	ctx := context.Background()
	hold := func(t *testing.T, e *Env, a *subAgent) {
		t.Helper()
		if err := e.Apply(ctx, a, Event{Kind: Working, SessionID: sessionA}); err != nil {
			t.Fatal(err)
		}
		// As a turn end with runs below would leave it; the test has no
		// agent process for them to run below.
		if err := e.paneOpts(ctx, map[string]string{optHeld: "1", attention.OptNested: "fakeagent"}); err != nil {
			t.Fatal(err)
		}
	}
	flag := func(t *testing.T, e *Env) (attention.State, string) {
		t.Helper()
		st, reason, err := e.Core.FlagOf(ctx, e.Pane)
		if err != nil {
			t.Fatal(err)
		}
		return st, reason
	}

	t.Run("runs end", func(t *testing.T) {
		e, a, _, s := subEnv(t)
		hold(t, e, a)
		if err := e.SweepNested(ctx); err != nil {
			t.Fatal(err)
		}
		if st, reason := flag(t, e); st != attention.Done || reason != "turn ended" {
			t.Errorf("flag = %s %q, want done, turn ended", st, reason)
		}
		if got := s.Display(t, e.Pane, "#{"+optHeld+"}|#{"+attention.OptNested+"}"); got != "|" {
			t.Errorf("held|nested = %q, want both gone", got)
		}
	})
	t.Run("a new turn", func(t *testing.T) {
		e, a, _, s := subEnv(t)
		hold(t, e, a)
		if err := e.Apply(ctx, a, Event{Kind: Working, SessionID: sessionA}); err != nil {
			t.Fatal(err)
		}
		if err := e.SweepNested(ctx); err != nil {
			t.Fatal(err)
		}
		if st, _ := flag(t, e); st != attention.Working {
			t.Errorf("flag = %s, want working: the agent's own turn goes on", st)
		}
		if got := s.Display(t, e.Pane, "#{"+optHeld+"}"); got != "" {
			t.Errorf("held = %q, want gone with the new turn", got)
		}
	})
}
