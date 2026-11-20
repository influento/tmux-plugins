package contract

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// taskResult is what `start --json` prints of a task.
type taskResult struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	Title  string `json:"title"`
}

// tmuxAgent runs tmux-agent from the test's pane; it returns stdout, stderr and
// the exit code.
func (e *env) tmuxAgent(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := e.Command(t, e.pane, "tmux-agent", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	code := 0
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out2str(out), stderr.String(), code
}

// paneOpt is a pane option of pane.
func (e *env) paneOpt(t *testing.T, pane, name string) string {
	t.Helper()
	return e.MustTmux(t, "show-options", "-pqv", "-t", pane, name)
}

// A tool binds the windows it starts for a task: the item's window, its
// demo window, and a window open already. Each shows the task and the
// role its agent runs as; the agent's session notes the task. task list
// gives them; task end forgets the task and, with --close, closes them,
// the caller's own last. An unknown task closes nothing and exits 4.
func TestTask(t *testing.T) {
	e := newEnv(t)
	hooks := e.hooks(t, "start.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home))
	e.stubEnv(t, "STUB_MODE", "wait", "STUB_HOOK", hooks)
	out, stderr, code := e.start(t, "--name", "item-1", "--task", "item-1", "--source", "tool-a", "--title", "item one",
		"--json", "--", "claude", "--agent", "role-w")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var got struct {
		startResult
		Task *taskResult `json:"task"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if got.Task == nil || *got.Task != (taskResult{"tool-a", "item-1", "item one"}) {
		t.Errorf("task = %+v", got.Task)
	}
	worker := got.Pane
	for name, want := range map[string]string{"@agent_task": "tool-a/item-1", "@agent_task_title": "item one", "@agent_role": "role-w"} {
		if v := e.paneOpt(t, worker, name); v != want {
			t.Errorf("%s = %q, want %q", name, v, want)
		}
	}
	rec, err := os.ReadFile(filepath.Join(e.StateHome, "tmux-agent", "sessions", "claude", sessionA+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rec), `"task": "tool-a/item-1"`) {
		t.Errorf("the session's record notes no task: %s", rec)
	}

	e.stubEnv(t, "STUB_HOOK", "")
	demo, stderr, code := e.start(t, "--name", "item-1-demo", "--task", "item-1", "--source", "tool-a", "fakeagent")
	if code != 0 {
		t.Fatalf("demo: exit %d: %s", code, stderr)
	}
	open := e.MustTmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-n", "item-1-old")
	if _, stderr, code := e.start(t, "--name", "item-1-old", "--task", "item-1", "--source", "tool-a", "fakeagent"); code != 3 {
		t.Errorf("a name in use: exit %d, %s; want 3", code, stderr)
	}
	for _, p := range []string{demo, open} {
		if v := e.paneOpt(t, p, "@agent_task") + " " + e.paneOpt(t, p, "@agent_task_title"); v != "tool-a/item-1 item one" {
			t.Errorf("pane %s: task %q, want the task and its title", p, v)
		}
	}
	if _, stderr, code := e.start(t, "--task", "item-1", "fakeagent"); code != 2 {
		t.Errorf("--task without --source: exit %d, %s; want 2", code, stderr)
	}

	out, stderr, code = e.tmuxAgent(t, "task", "list", "--json")
	if code != 0 {
		t.Fatalf("task list: exit %d: %s", code, stderr)
	}
	var list struct {
		Version int `json:"version"`
		Tasks   []struct {
			taskResult
			Panes []struct {
				Pane, Name, Role string
			} `json:"panes"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if list.Version != 1 || len(list.Tasks) != 1 || list.Tasks[0].Title != "item one" || len(list.Tasks[0].Panes) != 3 {
		t.Fatalf("task list = %s", out)
	}
	roles := map[string]string{}
	for _, p := range list.Tasks[0].Panes {
		roles[p.Name] = p.Role
	}
	if roles["item-1"] != "role-w" || roles["item-1-demo"] != "" {
		t.Errorf("roles = %v", roles)
	}

	// By id: the caller's window keeps its automatic name, which follows
	// its program (sh, then bash) while the test runs.
	windows := func() []string {
		return strings.Split(e.MustTmux(t, "list-windows", "-a", "-F", "#{window_id} #{window_name}"), "\n")
	}
	ids := func(ws []string) []string {
		out := make([]string, len(ws))
		for i, w := range ws {
			out[i], _, _ = strings.Cut(w, " ")
		}
		return out
	}
	before := windows()
	if _, stderr, code := e.tmuxAgent(t, "task", "end", "item-9", "--source", "tool-a", "--close"); code != 4 || !strings.Contains(stderr, "no open task") {
		t.Errorf("an unknown task: exit %d, %q; want 4", code, stderr)
	}
	if now := windows(); !slices.Equal(ids(now), ids(before)) {
		t.Errorf("an unknown task closed windows: %q, were %q", now, before)
	}

	// The caller is the item's own window, as merge runs in it.
	e.MustTmux(t, "set-option", "-p", "-t", e.pane, "@agent_task", "tool-a/item-1")
	e.MustTmux(t, "rename-window", "-t", e.pane, "item-1-shell")
	out, stderr, code = e.tmuxAgent(t, "task", "end", "item-1", "--source", "tool-a", "--close", "--json")
	if code != 0 {
		t.Fatalf("task end: exit %d: %s", code, stderr)
	}
	var ended struct {
		Version int                     `json:"version"`
		Task    taskResult              `json:"task"`
		Closed  []struct{ Name string } `json:"closed"`
	}
	if err := json.Unmarshal([]byte(out), &ended); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	var closed []string
	for _, w := range ended.Closed {
		closed = append(closed, w.Name)
	}
	if len(closed) != 4 || closed[3] != "item-1-shell" {
		t.Errorf("closed %q, want the four windows, the caller's last", closed)
	}
	if _, err := os.Stat(filepath.Join(e.StateHome, "tmux-agent", "tasks", "tool-a", "item-1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the task's record stays: %v", err)
	}
}

// task end without --close forgets the task and takes its windows out of
// it; they stay open.
func TestTaskEndKeepsWindows(t *testing.T) {
	e := newEnv(t)
	e.stubEnv(t, "STUB_MODE", "wait")
	pane, stderr, code := e.start(t, "--name", "item-2", "--task", "item-2", "--source", "tool-a", "fakeagent")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if v := e.paneOpt(t, pane, "@agent_task_title"); v != "item-2" {
		t.Errorf("title %q, want the id for none given", v)
	}
	if _, stderr, code := e.tmuxAgent(t, "task", "end", "item-2", "--source", "tool-a"); code != 0 {
		t.Fatalf("task end: exit %d: %s", code, stderr)
	}
	if v := e.paneOpt(t, pane, "@agent_task"); v != "" {
		t.Errorf("@agent_task = %q after the end", v)
	}
	if !slices.Contains(e.windowNames(t), "item-2") {
		t.Error("the window went")
	}
	if out, _, _ := e.tmuxAgent(t, "task", "list"); out != "" {
		t.Errorf("task list = %q, want none", out)
	}
	e.release(t)
}
