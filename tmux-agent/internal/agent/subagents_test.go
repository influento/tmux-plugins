package agent

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

const sessionA = "11111111-2222-4333-8444-555555555555"

// subAgent stands in for an adapter that reads its subagents' files.
type subAgent struct {
	mu    sync.Mutex
	files map[string]Subagent // by id
	reads []string
}

func (*subAgent) Name() string    { return "fakeagent" }
func (*subAgent) Command() string { return "fakeagent" }

func (f *subAgent) SubagentIDs(string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.files {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func (f *subAgent) ReadSubagent(_, id string) (Subagent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, id)
	return f.files[id], nil
}

func (f *subAgent) SubagentSessions(func(string) string) ([]SessionFiles, error) {
	return []SessionFiles{{Session: sessionA, Transcript: "/proj-a/" + sessionA + ".jsonl"}}, nil
}

func (f *subAgent) set(id string, s Subagent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[id] = s
}

type fillCall struct {
	session string
	ids     []string
}

// subEnv is an Env on a pane that runs fakeagent in session sessionA, with
// a store in a temp folder; fills are recorded, not run.
func subEnv(t *testing.T) (*Env, *subAgent, *[]fillCall, *tmuxtest.Server) {
	t.Helper()
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	pane := agentWindow(t, s)
	a := &subAgent{files: map[string]Subagent{}}
	var fills []fillCall
	e := &Env{
		Tmux: s.Client, Core: &attention.Core{Tmux: s.Client}, Registry: NewRegistry(a), Pane: pane,
		Store:  &store.Store{Dir: t.TempDir()},
		Nested: func([]string, int) bool { return false },
		Fill:   func(_ Agent, sf SessionFiles, ids []string) { fills = append(fills, fillCall{sf.Session, ids}) },
	}
	if err := e.Apply(context.Background(), a, Event{Kind: SessionStart, SessionID: sessionA, Transcript: "/proj-a/" + sessionA + ".jsonl"}); err != nil {
		t.Fatal(err)
	}
	return e, a, &fills, s
}

func running(t *testing.T, s *tmuxtest.Server, pane string) string {
	t.Helper()
	return s.Display(t, pane, "#{"+attention.OptSubagents+"}")
}

// The pane lists the subagents that run: a start adds one, an end takes
// it out, a turn end sets the list. One gone from a turn end's list
// without an end of its own (the user stopped it) ends there.
func TestSubagentsRunning(t *testing.T) {
	e, a, fills, s := subEnv(t)
	ctx := context.Background()
	apply := func(ev Event) {
		t.Helper()
		ev.SessionID = sessionA
		if err := e.Apply(ctx, a, ev); err != nil {
			t.Fatal(err)
		}
	}
	apply(Event{Kind: Working})
	apply(Event{Kind: SubagentStart, By: "a1", Subagent: &Subagent{ID: "a1", Type: "Explore"}})
	apply(Event{Kind: Working, ToolEnd: true, Subagent: &Subagent{ID: "a1", Type: "Explore", Description: "probe", Prompt: "look\nhere", Model: "model-a"}})
	apply(Event{Kind: SubagentStart, By: "a2", Subagent: &Subagent{ID: "a2", Type: "general-purpose"}})
	if got := running(t, s, e.Pane); got != "a1 a2" {
		t.Errorf("running = %q, want a1 a2", got)
	}
	apply(Event{Kind: TurnEnd, Subagents: []string{"a1", "a2"}})
	apply(Event{Kind: SubagentEnd, By: "a1", Subagent: &Subagent{ID: "a1", Type: "Explore", Transcript: "/proj-a/a1.jsonl"}})
	if got := running(t, s, e.Pane); got != "a2" {
		t.Errorf("running after a1's end = %q, want a2", got)
	}
	apply(Event{Kind: TurnEnd}) // a2 no longer runs, with no end of its own
	if got := running(t, s, e.Pane); got != "" {
		t.Errorf("running after the last turn end = %q, want none", got)
	}
	if st, _, _ := e.Core.FlagOf(ctx, e.Pane); st != attention.Done {
		t.Errorf("flag = %s, want done", st)
	}
	want := []fillCall{{sessionA, []string{"a1"}}, {sessionA, []string{"a2"}}}
	if len(*fills) != 2 || !slices.Equal((*fills)[0].ids, want[0].ids) || !slices.Equal((*fills)[1].ids, want[1].ids) {
		t.Errorf("fills = %+v, want %+v", *fills, want)
	}
	r1, ok, err := e.Store.LoadSubagent("fakeagent", sessionA, "a1")
	if err != nil || !ok {
		t.Fatalf("a1's record: %v %v", ok, err)
	}
	if r1.Type != "Explore" || r1.Description != "probe" || r1.Prompt != "look\nhere" || r1.Model != "model-a" ||
		r1.Transcript != "/proj-a/a1.jsonl" || r1.Started == 0 || r1.Ended == 0 || r1.Complete {
		t.Errorf("a1's record = %+v", r1)
	}
	if r2, _, _ := e.Store.LoadSubagent("fakeagent", sessionA, "a2"); r2.Ended == 0 || r2.Type != "general-purpose" {
		t.Errorf("a2's record = %+v, want it ended", r2)
	}
}

// A session that starts again, or ends, leaves no subagent listed.
func TestSubagentsGoWithTheSession(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   Event
	}{
		{"new session", Event{Kind: SessionStart, SessionID: sessionA}},
		{"killed", Event{Kind: SessionEnd, SessionID: sessionA, End: EndKilled}},
		{"exited", Event{Kind: SessionEnd, SessionID: sessionA, End: EndExited}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, a, _, s := subEnv(t)
			ctx := context.Background()
			if err := e.Apply(ctx, a, Event{Kind: SubagentStart, SessionID: sessionA, By: "a1", Subagent: &Subagent{ID: "a1", Type: "Explore"}}); err != nil {
				t.Fatal(err)
			}
			if err := e.Apply(ctx, a, c.ev); err != nil {
				t.Fatal(err)
			}
			if got := running(t, s, e.Pane); got != "" {
				t.Errorf("running = %q, want none", got)
			}
		})
	}
}

// A scan fills a record from the files once they say how the subagent
// ended; the files' values win over the hooks', and a complete record is
// not read again.
func TestScanSubagents(t *testing.T) {
	e, a, _, _ := subEnv(t)
	ctx := context.Background()
	if err := e.Apply(ctx, a, Event{Kind: Working, SessionID: sessionA, ToolEnd: true,
		Subagent: &Subagent{ID: "a1", Prompt: "from the hook", Model: "model-hook"}}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 12, 3, 15, 37, 0, time.UTC)
	a.set("a1", Subagent{ID: "a1", Type: "Explore"}) // still runs
	a.set("a2", Subagent{ID: "a2", Type: "Explore", Outcome: OutcomeStopped, Started: t0, Ended: t0.Add(9 * time.Second)})
	sf := SessionFiles{Session: sessionA, Transcript: "/proj-a/" + sessionA + ".jsonl"}

	n, err := e.ScanSubagents(ctx, a, sf, nil, 0)
	if err != nil || n != 1 {
		t.Fatalf("first scan filled %d (%v), want 1", n, err)
	}
	if r, _, _ := e.Store.LoadSubagent("fakeagent", sessionA, "a1"); r.Complete || r.Prompt != "from the hook" {
		t.Errorf("a1 while it runs = %+v", r)
	}

	// a1 ends while a fill waits for its files.
	go func() {
		time.Sleep(300 * time.Millisecond)
		a.set("a1", Subagent{ID: "a1", Type: "Explore", Model: "model-a", Result: "Found it.", Outcome: OutcomeDone,
			Started: t0, Ended: t0.Add(52 * time.Second), Tokens: &Tokens{Input: 3, Output: 30}})
	}()
	if n, err := e.ScanSubagents(ctx, a, sf, []string{"a1"}, 5*time.Second); err != nil || n != 1 {
		t.Fatalf("fill filled %d (%v), want 1", n, err)
	}
	r, _, _ := e.Store.LoadSubagent("fakeagent", sessionA, "a1")
	if !r.Complete || r.Outcome != "done" || r.Result != "Found it." || r.Prompt != "from the hook" || r.Model != "model-a" ||
		r.Ended-r.Started != 52 || r.Tokens == nil || r.Tokens.Total() != 33 {
		t.Errorf("a1 filled = %+v", r)
	}

	a.mu.Lock()
	a.reads = nil
	a.mu.Unlock()
	if n, err := e.ScanAllSubagents(ctx, a, func(string) string { return "" }); err != nil || n != 0 {
		t.Errorf("scan of complete records filled %d (%v), want 0", n, err)
	}
	if len(a.reads) != 0 {
		t.Errorf("complete records were read again: %v", a.reads)
	}
}
