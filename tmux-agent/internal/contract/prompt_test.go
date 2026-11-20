package contract

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// promptResult is what `prompt --json` prints.
type promptResult struct {
	Version      int    `json:"version"`
	Pane         string `json:"pane"`
	Name         string `json:"name"`
	AgentSession string `json:"agent_session"`
	State        string `json:"state"`
	Answer       *struct {
		Text string    `json:"text"`
		At   time.Time `json:"at"`
	} `json:"answer"`
}

// tmuxAgentIn runs tmux-agent from pane with stdin; it returns stdout,
// stderr and the exit code.
func (e *env) tmuxAgentIn(t *testing.T, pane, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := e.Command(t, pane, "tmux-agent", args...)
	cmd.Stdin = strings.NewReader(stdin)
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

// startChat starts a chat stub in a window of its own, named name, from
// the window's shell as a user does: tmux names the pane after the
// launcher, which the agent's flags are owned by.
func (e *env) startChat(t *testing.T, name, session string) string {
	t.Helper()
	pane := e.MustTmux(t, "new-window", "-d", "-t", "proj-a:", "-n", name, "-P", "-F", "#{pane_id}")
	e.WaitForContent(t, pane, "$")
	e.MustTmux(t, "send-keys", "-t", pane, "-l", "STUB_MODE=chat STUB_TAG="+name+" STUB_SESSION="+session+" tmux-agent run claude")
	e.MustTmux(t, "send-keys", "-t", pane, "Enter")
	tmuxtest.WaitFor(t, 10*time.Second, name+"'s session", func() bool { return e.paneOpt(t, pane, "@agent_session") == session })
	return pane
}

// prompt types a prompt into an agent's window, as the user would, and
// with --wait prints the turn's answer. It refuses an agent that works or
// waits on the user, a target with no agent or with two, and the caller's
// own pane; read gives the last answers.
func TestPrompt(t *testing.T) {
	e := newEnv(t)
	paneA := e.startChat(t, "agent-a", sessionA)
	paneB := e.startChat(t, "agent-b", sessionB)
	prompt := func(t *testing.T, stdin string, args ...string) (string, string, int) {
		t.Helper()
		return e.tmuxAgentIn(t, e.pane, stdin, append([]string{"prompt"}, args...)...)
	}

	t.Run("wait prints the answer", func(t *testing.T) {
		out, stderr, code := prompt(t, "", "--wait", "--timeout", "20s", "agent-a", "first prompt")
		if code != 0 || out != "answer 1" {
			t.Fatalf("exit %d, %q, %s", code, out, stderr)
		}
		if b, err := os.ReadFile(filepath.Join(e.dir, "agent-a.prompt1")); err != nil || string(b) != "first prompt" {
			t.Errorf("the agent got %q, %v", b, err)
		}
		if flag := e.paneOpt(t, paneA, "@attention"); flag != "done" {
			t.Errorf("flag = %q, want done", flag)
		}
	})
	t.Run("lines from stdin are one prompt", func(t *testing.T) {
		out, stderr, code := prompt(t, "line one\nline two\n", "--wait", "--json", "--timeout", "20s", "proj-a:agent-a")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		var got promptResult
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v: %q", err, out)
		}
		if got.Version != 1 || got.Pane != paneA || got.Name != "agent-a" || got.AgentSession != sessionA ||
			got.State != "done" || got.Answer == nil || got.Answer.Text != "answer 2" {
			t.Errorf("prompt = %+v", got)
		}
		if b, err := os.ReadFile(filepath.Join(e.dir, "agent-a.prompt2")); err != nil || string(b) != "line one\nline two" {
			t.Errorf("the agent got %q, %v", b, err)
		}
	})
	t.Run("read gives the last answers", func(t *testing.T) {
		out, stderr, code := e.tmuxAgentIn(t, e.pane, "", "read", "--last", "2", "agent-a")
		if code != 0 || out != "answer 1\n---\nanswer 2" {
			t.Errorf("exit %d, %q, %s", code, out, stderr)
		}
	})
	t.Run("without wait it returns at once", func(t *testing.T) {
		out, stderr, code := prompt(t, "", "agent-a", "third")
		if code != 0 || out != "" {
			t.Fatalf("exit %d, %q, %s", code, out, stderr)
		}
		tmuxtest.WaitFor(t, 10*time.Second, "the third answer", func() bool {
			out, _, _ := e.tmuxAgentIn(t, e.pane, "", "read", "agent-a")
			return out == "answer 3"
		})
	})
	t.Run("an agent that asks the user", func(t *testing.T) {
		_, stderr, code := prompt(t, "", "--wait", "--timeout", "20s", "agent-b", "ask")
		if code != 3 || !strings.Contains(stderr, "waits on the user: Bash(ls)") {
			t.Errorf("exit %d, %q; want 3", code, stderr)
		}
		_, stderr, code = prompt(t, "", "agent-b", "more")
		if code != 3 || !strings.Contains(stderr, "waits on the user") {
			t.Errorf("a second prompt: exit %d, %q; want 3", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(e.dir, "agent-b.prompt2")); err == nil {
			t.Error("the second prompt reached the agent")
		}
	})
	t.Run("no agent", func(t *testing.T) {
		if _, stderr, code := prompt(t, "", "w-none", "hi"); code != 4 {
			t.Errorf("exit %d, %q; want 4", code, stderr)
		}
	})
	t.Run("two agents of one name", func(t *testing.T) {
		e.MustTmux(t, "rename-window", "-t", paneB, "agent-a")
		defer e.MustTmux(t, "rename-window", "-t", paneB, "agent-b")
		_, stderr, code := prompt(t, "", "agent-a", "hi")
		if code != 2 || !strings.Contains(stderr, paneA) || !strings.Contains(stderr, paneB) {
			t.Errorf("exit %d, %q; want 2 and both panes", code, stderr)
		}
	})
	t.Run("the caller's own pane", func(t *testing.T) {
		_, stderr, code := e.tmuxAgentIn(t, paneA, "", "prompt", "agent-a", "hi")
		if code != 2 || !strings.Contains(stderr, "own pane") {
			t.Errorf("exit %d, %q; want 2", code, stderr)
		}
	})
	t.Run("an agent that ends", func(t *testing.T) {
		_, stderr, code := prompt(t, "", "--wait", "--timeout", "20s", paneA, "bye")
		if code != 5 {
			t.Errorf("exit %d, %q; want 5", code, stderr)
		}
		if w := e.paneOpt(t, paneA, "@attention_wait"); w != "" {
			t.Errorf("@attention_wait = %q after the wait", w)
		}
	})
}
