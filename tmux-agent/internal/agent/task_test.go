package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

const sessionB = "22222222-3333-4444-8555-666666666666"

// roleAgent runs as the role --agent names.
type roleAgent struct{}

func (roleAgent) Name() string           { return "fakeagent" }
func (roleAgent) Command() string        { return "fakeagent" }
func (roleAgent) ProcessNames() []string { return []string{"fakeagent"} }

func (roleAgent) Role(args []string) string {
	for i, a := range args {
		if a == "--agent" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// A session that starts in a bound pane notes its task, with the role its
// agent runs as. After a restart, its resume in a new pane binds that
// pane to the task again while the task is open; a task that ended binds
// nothing.
func TestTaskAfterRestart(t *testing.T) {
	ctx := context.Background()
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	st := &store.Store{Dir: t.TempDir()}
	a := roleAgent{}
	envFor := func(pane string) *Env {
		return &Env{
			Tmux: s.Client, Core: &attention.Core{Tmux: s.Client}, Registry: NewRegistry(a), Pane: pane, Store: st,
			Nested:    func([]string, int) bool { return false },
			AgentArgs: func([]string, int) ([]string, bool) { return []string{"--agent", "role-w"}, true },
		}
	}
	task := store.Task{Source: "tool-a", ID: "item-1", Title: "item one"}
	if err := st.SaveTask(task); err != nil {
		t.Fatal(err)
	}

	first := agentWindow(t, s)
	e := envFor(first)
	if err := e.BindTask(ctx, task, []string{first}); err != nil {
		t.Fatal(err)
	}
	if err := e.Apply(ctx, a, Event{Kind: SessionStart, SessionID: sessionA}); err != nil {
		t.Fatal(err)
	}
	if got := s.Display(t, first, "#{"+attention.OptRole+"}"); got != "role-w" {
		t.Errorf("role = %q, want role-w", got)
	}
	if rec, ok, err := st.Load("fakeagent", sessionA); err != nil || !ok || rec.Task != task.Key() {
		t.Fatalf("record = %+v, %v, %v; want the task noted", rec, ok, err)
	}

	resumeIn := func(t *testing.T) string {
		t.Helper()
		pane := agentWindow(t, s)
		e := envFor(pane)
		// As the launcher of a restore's resume leaves it.
		if err := e.paneOpts(ctx, map[string]string{optResume: sessionA}); err != nil {
			t.Fatal(err)
		}
		if err := e.Apply(ctx, a, Event{Kind: SessionStart, SessionID: sessionA, Resume: true}); err != nil {
			t.Fatal(err)
		}
		return pane
	}
	t.Run("the task open", func(t *testing.T) {
		pane := resumeIn(t)
		if got := s.Display(t, pane, "#{"+attention.OptTask+"}|#{"+attention.OptTaskTitle+"}"); got != "tool-a/item-1|item one" {
			t.Errorf("task = %q, want the task again", got)
		}
	})
	t.Run("the task ended", func(t *testing.T) {
		if _, err := st.DeleteTask(task.Source, task.ID); err != nil {
			t.Fatal(err)
		}
		pane := resumeIn(t)
		if got := s.Display(t, pane, "#{"+attention.OptTask+"}"); got != "" {
			t.Errorf("task = %q, want none", got)
		}
		if rec, _, _ := st.Load("fakeagent", sessionA); rec.Task != "" {
			t.Errorf("record task = %q, want none", rec.Task)
		}
	})
}

// The runs below a pane's agent show by name, and those that run as a
// role by their role.
func TestRunsOf(t *testing.T) {
	fakeProcs(t, map[int][]string{
		12: {"11", "S", "fakeagent", "fakeagent"},
		13: {"12", "S", "bash", "bash", ".hooks/review"},
		14: {"13", "S", "fakeagent", "fakeagent", "-p", "review", "--agent", "role-r"},
		15: {"12", "S", "fakeagent", "fakeagent", "-p", "hi"},
	})
	e := &Env{Registry: NewRegistry(roleAgent{})}
	names, roles := e.runsOf(nestedProcs(12, []string{"fakeagent"}, processTree()))
	if strings.Join(names, " ") != "fakeagent fakeagent" {
		t.Errorf("names = %q", names)
	}
	if role, _, ok := strings.Cut(roles, "@"); !ok || role != "role-r" || strings.Contains(roles, " ") {
		t.Errorf("roles = %q, want role-r@start", roles)
	}
}
