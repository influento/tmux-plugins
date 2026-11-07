package agent

import (
	"context"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// fakeAgent stands in for an adapter that reads its transcript.
type fakeAgent struct {
	cut   time.Time // when the user last cut a turn short; zero for never
	title string
	sure  bool
}

func (fakeAgent) Name() string    { return "fakeagent" }
func (fakeAgent) Command() string { return "fakeagent" }

func (f fakeAgent) Interrupted(string) (time.Time, bool) { return f.cut, !f.cut.IsZero() }
func (f fakeAgent) Title(string) (string, bool)          { return f.title, f.sure }

// agentWindow starts a window that runs fakeagent and returns its pane.
func agentWindow(t *testing.T, s *tmuxtest.Server) string {
	t.Helper()
	pane := s.MustTmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "exec bash -c 'exec -a fakeagent sleep 600'")
	tmuxtest.WaitFor(t, 5*time.Second, "the pane to run fakeagent", func() bool {
		return s.Display(t, pane, "#{pane_current_command}") == "fakeagent"
	})
	return pane
}

// A turn the user cut short after its flag was set clears a working or
// needs-you flag; an older interrupt, or a done flag, leaves it.
func TestSweepTurnsClearsCutTurns(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		flag attention.State
		cut  time.Time
		want attention.State
	}{
		{"declined permission", attention.NeedsYou, t0.Add(2 * time.Second), attention.Idle},
		{"esc while working", attention.Working, t0.Add(500 * time.Millisecond), attention.Idle},
		{"interrupt before the flag", attention.NeedsYou, t0.Add(-10 * time.Second), attention.NeedsYou},
		{"done stays", attention.Done, t0.Add(2 * time.Second), attention.Done},
		{"never interrupted", attention.Working, time.Time{}, attention.Working},
	}
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	ctx := context.Background()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pane := agentWindow(t, s)
			core := &attention.Core{Tmux: s.Client, Now: func() time.Time { return t0 }}
			e := &Env{Tmux: s.Client, Core: core, Registry: NewRegistry(fakeAgent{cut: c.cut}), Pane: pane}
			s.MustTmux(t, "set-option", "-p", "-t", pane, optAgent, "fakeagent", ";",
				"set-option", "-p", "-t", pane, optTranscript, "/proj-a/t.jsonl", ";",
				"set-option", "-p", "-t", pane, optState, string(c.flag))
			if _, _, err := core.Set(ctx, pane, attention.Flag{State: c.flag, Owner: "fakeagent", Quiet: true}); err != nil {
				t.Fatal(err)
			}
			if err := e.SweepTurns(ctx); err != nil {
				t.Fatal(err)
			}
			got, _, err := core.FlagOf(ctx, pane)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("flag = %s, want %s", got, c.want)
			}
			if state := s.Display(t, pane, "#{"+optState+"}"); c.want == attention.Idle && state != "" {
				t.Errorf("%s = %q after the flag went, want unset", optState, state)
			}
		})
	}
}

// The agent's own title replaces a stand-in this pane's agent gave the
// window, but no name the user gave, and a stand-in never replaces a name.
func TestTurnEndTitles(t *testing.T) {
	stand := fakeAgent{title: "write the docs"}
	sure := fakeAgent{title: "Docs rewrite", sure: true}
	cases := []struct {
		name   string
		first  fakeAgent
		rename string // the user's name between the turns; "" for none
		second fakeAgent
		want   string
	}{
		{"sure replaces a stand-in", stand, "", sure, "Docs rewrite"},
		{"stand-in stays", stand, "", fakeAgent{title: "another task"}, "write the docs"},
		{"user's name stays", stand, "mine", sure, "mine"},
		{"sure first", sure, "", fakeAgent{title: "Docs rewrite v2", sure: true}, "Docs rewrite v2"},
	}
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	ctx := context.Background()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pane := agentWindow(t, s)
			e := &Env{
				Tmux: s.Client, Core: &attention.Core{Tmux: s.Client}, Registry: NewRegistry(), Pane: pane,
				Nested: func([]string, int) bool { return false },
			}
			if err := e.Apply(ctx, c.first, Event{Kind: TurnEnd}); err != nil {
				t.Fatal(err)
			}
			if got := s.Display(t, pane, "#{window_name}"); got != c.first.title {
				t.Fatalf("window = %q after the first turn, want %q", got, c.first.title)
			}
			if c.rename != "" {
				s.MustTmux(t, "rename-window", "-t", pane, c.rename)
			}
			if err := e.Apply(ctx, c.second, Event{Kind: Working}); err != nil {
				t.Fatal(err)
			}
			if err := e.Apply(ctx, c.second, Event{Kind: TurnEnd}); err != nil {
				t.Fatal(err)
			}
			if got := s.Display(t, pane, "#{window_name}"); got != c.want {
				t.Errorf("window = %q, want %q", got, c.want)
			}
		})
	}
}
