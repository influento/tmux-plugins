package contract

import (
	"encoding/json"
	"errors"
	"fmt"
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

// touch creates the empty file name in the stub's folder.
func (e *env) touch(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitExists waits until the file name exists in the stub's folder.
func (e *env) waitExists(t *testing.T, name string) {
	t.Helper()
	tmuxtest.WaitFor(t, 10*time.Second, name, func() bool {
		_, err := os.Stat(filepath.Join(e.dir, name))
		return err == nil
	})
}

// promptRun is what one run of prompt printed and its exit code.
type promptRun struct {
	out, stderr string
	code        int
}

// promptBackground runs prompt in the background.
func (e *env) promptBackground(t *testing.T, args ...string) <-chan promptRun {
	t.Helper()
	ch := make(chan promptRun, 1)
	go func() {
		out, stderr, code := e.tmuxAgentIn(t, e.pane, "", append([]string{"prompt"}, args...)...)
		ch <- promptRun{out, stderr, code}
	}()
	return ch
}

// A window in two sessions, grouped or linked, is one agent: its pane, its
// name and each SESSION:WINDOW name it.
func TestPromptGroupedSessions(t *testing.T) {
	e := newEnv(t)
	paneA := e.startChat(t, "agent-a", sessionA)
	e.MustTmux(t, "new-session", "-d", "-t", "proj-a", "-s", "proj-b")
	for i, target := range []string{paneA, "agent-a", "proj-a:agent-a", "proj-b:agent-a"} {
		out, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "--wait", "--timeout", "20s", target, "hello")
		if want := fmt.Sprintf("answer %d", i+1); code != 0 || out != want {
			t.Errorf("prompt %s: exit %d, %q, %q; want 0 and %q", target, code, out, stderr, want)
		}
	}
	if out, stderr, code := e.tmuxAgentIn(t, e.pane, "", "read", paneA); code != 0 || out != "answer 4" {
		t.Errorf("read %s: exit %d, %q, %q; want 0 and \"answer 4\"", paneA, code, out, stderr)
	}
}

// sessionC is a third agent session.
const sessionC = "bbbbbbbb-cccc-4ddd-8eee-ffffffffffff"

// A pane whose agent no longer runs keeps its session for a resume, and
// its shell reads the terminal: prompt refuses it (exit 4) and types
// nothing. The launcher killed with SIGTERM, the launcher and the agent
// with SIGKILL, or the agent stopped with Ctrl-Z.
func TestPromptNoRunningAgent(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		name, session string
		end           func(t *testing.T, pane, launcher string)
	}{
		{"agent-a", sessionA, func(t *testing.T, pane, launcher string) {
			t.Helper()
			e.MustTmux(t, "run-shell", "kill -TERM "+launcher)
		}},
		{"agent-b", sessionB, func(t *testing.T, pane, launcher string) {
			t.Helper()
			// An agent left alone, in a group with no terminal, would end
			// only at its next read: the agent goes too.
			e.MustTmux(t, "run-shell", "kill -KILL "+launcher+" "+e.file(t, "agent-b.pid"))
		}},
		{"agent-c", sessionC, func(t *testing.T, pane, launcher string) {
			t.Helper()
			e.MustTmux(t, "send-keys", "-t", pane, "C-z")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			pane := e.startChat(t, c.name, c.session)
			c.end(t, pane, e.file(t, c.name+".ppid"))
			tmuxtest.WaitFor(t, 10*time.Second, "the shell", func() bool {
				return e.Display(t, pane, "#{pane_current_command}") == "bash"
			})
			if s := e.paneOpt(t, pane, "@agent_session"); s != c.session {
				t.Fatalf("@agent_session = %q, want the session kept", s)
			}
			_, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", c.name, `touch "$STUB_DIR/`+c.name+`.typed"`)
			if code != 4 {
				t.Errorf("prompt: exit %d, %q; want 4", code, stderr)
			}
			// The shell runs what it read in order: the prompt's text before this.
			e.MustTmux(t, "send-keys", "-t", pane, "-l", `touch "$STUB_DIR/`+c.name+`.after"`)
			e.MustTmux(t, "send-keys", "-t", pane, "Enter")
			e.waitExists(t, c.name+".after")
			if _, err := os.Stat(filepath.Join(e.dir, c.name+".typed")); err == nil {
				t.Error("the shell ran the prompt's text")
			}
		})
	}
}

// A second prompt that comes before the agent took the first one in sees
// it working (exit 3); the first prompt gets its own answer.
func TestPromptTwoAtOnce(t *testing.T) {
	e := newEnv(t)
	e.startChat(t, "agent-a", sessionA)
	first := e.promptBackground(t, "--wait", "--timeout", "20s", "agent-a", "gate")
	e.waitExists(t, "agent-a.prompt1")
	if _, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "--wait", "--timeout", "5s", "agent-a", "second"); code != 3 {
		t.Errorf("the second prompt: exit %d, %q; want 3", code, stderr)
	}
	e.touch(t, "agent-a.submit1")
	if r := <-first; r.code != 0 || r.out != "answer 1" {
		t.Errorf("the first prompt: exit %d, %q, %q; want 0 and \"answer 1\"", r.code, r.out, r.stderr)
	}
}

// A pane the user scrolls (copy mode) still gets the prompt, sent.
func TestPromptCopyMode(t *testing.T) {
	e := newEnv(t)
	pane := e.startChat(t, "agent-a", sessionA)
	for i, keys := range []string{"emacs", "vi"} {
		e.MustTmux(t, "set-option", "-g", "mode-keys", keys)
		e.MustTmux(t, "copy-mode", "-t", pane)
		out, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "--wait", "--timeout", "10s", "agent-a", "hello")
		if want := fmt.Sprintf("answer %d", i+1); code != 0 || out != want {
			t.Errorf("mode-keys %s: exit %d, %q, %q; want 0 and %q", keys, code, out, stderr, want)
		}
	}
}

// In a window start opens, the pane's command is sh, the launcher's
// parent: a sweep keeps the agent's flag, a second prompt sees it working,
// and the first gets its answer.
func TestPromptInStartWindow(t *testing.T) {
	e := newEnv(t)
	e.stubEnv(t, "STUB_MODE", "chat", "STUB_TAG", "w-a", "STUB_SESSION", sessionA)
	if out, stderr, code := e.start(t, "--name", "w-a", "claude"); code != 0 {
		t.Fatalf("start: exit %d, %q, %q", code, out, stderr)
	}
	pane := e.MustTmux(t, "list-panes", "-a", "-f", "#{==:#{window_name},w-a}", "-F", "#{pane_id}")
	first := e.promptBackground(t, "--wait", "--timeout", "20s", "w-a", "hold")
	e.waitExists(t, "w-a.submitted1")
	if _, stderr, code := e.tmuxAgentIn(t, e.pane, "", "sweep"); code != 0 {
		t.Fatalf("sweep: exit %d, %q", code, stderr)
	}
	if flag := e.paneOpt(t, pane, "@attention"); flag != "working" {
		t.Errorf("after a sweep the flag is %q, want working", flag)
	}
	if _, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "w-a", "second"); code != 3 {
		t.Errorf("a second prompt: exit %d, %q; want 3", code, stderr)
	}
	e.touch(t, "w-a.answer1")
	if r := <-first; r.code != 0 || r.out != "answer 1" {
		t.Errorf("the first prompt: exit %d, %q, %q; want 0 and \"answer 1\"", r.code, r.out, r.stderr)
	}
}

// promptLabel is what the pane's labels say from source prompt, or "".
func (e *env) promptLabel(t *testing.T, pane string) string {
	t.Helper()
	var labels []struct{ Source, Text string }
	if raw := e.paneOpt(t, pane, "@labels"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &labels); err != nil {
			t.Fatalf("@labels %q: %v", raw, err)
		}
	}
	for _, l := range labels {
		if l.Source == "prompt" {
			return l.Text
		}
	}
	return ""
}

// Text that the agent's input box takes as a command of its own is
// refused (exit 2) unless --raw sends it.
func TestPromptCommandText(t *testing.T) {
	e := newEnv(t)
	e.startChat(t, "agent-a", sessionA)
	if _, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "agent-a", "/clear"); code != 2 || !strings.Contains(stderr, "--raw") {
		t.Errorf("exit %d, %q; want 2 and a word on --raw", code, stderr)
	}
	if out, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "--wait", "--timeout", "20s", "agent-a", "first"); code != 0 || out != "answer 1" {
		t.Errorf("the next prompt: exit %d, %q, %q; want the first answer", code, out, stderr)
	}
}

// Text that starts no turn gets no receipt: prompt says so (exit 6), with
// or without --wait, gives the pane its flag back and labels it.
func TestPromptNotSubmitted(t *testing.T) {
	e := newEnv(t)
	pane := e.startChat(t, "agent-a", sessionA)
	for _, args := range [][]string{{"--raw", "agent-a", "/clear"}, {"--raw", "--wait", "--timeout", "30s", "agent-a", "/compact"}} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			_, stderr, code := e.tmuxAgentIn(t, e.pane, "", append([]string{"prompt"}, args...)...)
			if code != 6 || !strings.Contains(stderr, "no turn") {
				t.Errorf("exit %d, %q; want 6", code, stderr)
			}
			if flag := e.paneOpt(t, pane, "@attention"); flag != "" {
				t.Errorf("flag %q after, want none", flag)
			}
			if l := e.promptLabel(t, pane); !strings.Contains(l, "not submitted") {
				t.Errorf("label %q, want one that says not submitted", l)
			}
		})
	}
	if out, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "--wait", "--timeout", "20s", "agent-a", "hello"); code != 0 || out != "answer 3" {
		t.Errorf("a prompt after: exit %d, %q, %q; want answer 3", code, out, stderr)
	}
	if l := e.promptLabel(t, pane); l != "" {
		t.Errorf("label %q after a prompt that worked, want none", l)
	}
}

// A --timeout or a signal that ends the wait for the receipt ends it as no
// receipt does: the pane gets its flag back and the label, and the channel
// goes. The exit is the timeout's (1) or the signal's (130).
func TestPromptEndedBeforeReceipt(t *testing.T) {
	e := newEnv(t)
	paneA := e.startChat(t, "agent-a", sessionA)
	paneB := e.startChat(t, "agent-b", sessionB)
	want := func(t *testing.T, pane string) {
		t.Helper()
		if flag := e.paneOpt(t, pane, "@attention"); flag != "" {
			t.Errorf("flag %q after, want none", flag)
		}
		if l := e.promptLabel(t, pane); !strings.Contains(l, "not submitted") {
			t.Errorf("label %q, want one that says not submitted", l)
		}
		if w := e.paneOpt(t, pane, "@attention_wait"); w != "" {
			t.Errorf("@attention_wait = %q after, want none", w)
		}
	}
	t.Run("timeout", func(t *testing.T) {
		_, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "--raw", "--wait", "--timeout", "2s", "agent-a", "/clear")
		if code != 1 || !strings.Contains(stderr, "no receipt") {
			t.Errorf("exit %d, %q; want 1 and no receipt", code, stderr)
		}
		want(t, paneA)
	})
	t.Run("signal", func(t *testing.T) {
		cmd := e.Command(t, e.pane, "tmux-agent", "prompt", "--raw", "--wait", "agent-b", "/compact")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		tmuxtest.WaitFor(t, 10*time.Second, "the prompt typed", func() bool { return e.paneOpt(t, paneB, "@attention_wait") != "" })
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		err := cmd.Wait()
		if ee, ok := errors.AsType[*exec.ExitError](err); !ok || ee.ExitCode() != 130 {
			t.Errorf("prompt: %v; want exit 130", err)
		}
		want(t, paneB)
	})
}

// Text half typed in the agent's input box joins the prompt: the agent
// gets other text than was sent, and prompt says so (exit 6).
func TestPromptMergedWithDraft(t *testing.T) {
	e := newEnv(t)
	pane := e.startChat(t, "agent-a", sessionA)
	e.MustTmux(t, "send-keys", "-t", pane, "-l", "a draft ")
	_, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "--wait", "--timeout", "20s", "agent-a", "hello")
	if code != 6 || !strings.Contains(stderr, "other text") {
		t.Errorf("exit %d, %q; want 6", code, stderr)
	}
	if l := e.promptLabel(t, pane); !strings.Contains(l, "other text") {
		t.Errorf("label %q, want one that says other text", l)
	}
}

// The answer is the prompt's own turn's: an earlier turn's answer that
// Claude Code writes after the prompt was typed is not it.
func TestPromptAnswerOfItsOwnTurn(t *testing.T) {
	e := newEnv(t)
	pane := e.startChat(t, "agent-a", sessionA)
	if _, stderr, code := e.tmuxAgentIn(t, e.pane, "", "prompt", "agent-a", "late"); code != 0 {
		t.Fatalf("the first prompt: exit %d, %q", code, stderr)
	}
	tmuxtest.WaitFor(t, 10*time.Second, "the first turn's end", func() bool { return e.paneOpt(t, pane, "@attention") == "done" })
	second := e.promptBackground(t, "--wait", "--timeout", "20s", "agent-a", "mute")
	tmuxtest.WaitFor(t, 10*time.Second, "the second prompt typed", func() bool { return e.paneOpt(t, pane, "@attention_wait") != "" })
	e.touch(t, "agent-a.answer1")
	if r := <-second; r.code != 5 {
		t.Errorf("the second prompt: exit %d, %q, %q; want 5, no answer", r.code, r.out, r.stderr)
	}
}

// A wait that is interrupted takes its channel off the pane.
func TestPromptInterruptedWait(t *testing.T) {
	e := newEnv(t)
	pane := e.startChat(t, "agent-a", sessionA)
	cmd := e.Command(t, e.pane, "tmux-agent", "prompt", "--wait", "agent-a", "hold")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	e.waitExists(t, "agent-a.submitted1")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	if ee, ok := errors.AsType[*exec.ExitError](err); !ok || ee.ExitCode() != 130 {
		t.Errorf("prompt: %v; want exit 130", err)
	}
	if w := e.paneOpt(t, pane, "@attention_wait"); w != "" {
		t.Errorf("@attention_wait = %q after the interrupt, want none", w)
	}
	e.touch(t, "agent-a.answer1")
}
