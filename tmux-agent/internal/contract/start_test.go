package contract

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// startResult is what `start --json` prints.
type startResult struct {
	Version      int    `json:"version"`
	Pane         string `json:"pane"`
	Window       string `json:"window"`
	Session      string `json:"session"`
	Name         string `json:"name"`
	Agent        string `json:"agent"`
	AgentSession string `json:"agent_session"`
}

// stubEnv sets the stub's settings in the server's environment, which a
// new window starts with.
func (e *env) stubEnv(t *testing.T, kv ...string) {
	t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		e.MustTmux(t, "set-environment", "-g", kv[i], kv[i+1])
	}
}

// start runs `tmux-agent start` from the test's pane; it returns stdout,
// stderr and the exit code.
func (e *env) start(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := e.Command(t, e.pane, "tmux-agent", append([]string{"start"}, args...)...)
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

func out2str(b []byte) string { return strings.TrimSuffix(string(b), "\n") }

// windowNames lists the server's window names.
func (e *env) windowNames(t *testing.T) []string {
	t.Helper()
	return strings.Split(e.MustTmux(t, "list-windows", "-a", "-F", "#{window_name}"), "\n")
}

// start opens a named window in the background, in the folder given, and
// returns once the agent's session started, with the agent's arguments
// passed on.
func TestStartReady(t *testing.T) {
	e := newEnv(t)
	hooks := e.hooks(t, "start.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home))
	e.stubEnv(t, "STUB_MODE", "wait", "STUB_HOOK", hooks)
	out, stderr, code := e.start(t, "--name", "rev-a", "--cwd", e.Home, "--json", "--", "claude", "--agent", "wb x")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var got startResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if got.Version != 1 || got.AgentSession != sessionA || got.Session != "proj-a" || got.Name != "rev-a" || got.Agent != "claude" {
		t.Errorf("start = %+v", got)
	}
	if w := e.Display(t, got.Pane, "#{window_id} #{window_name} #{window_active} #{pane_current_path}"); w != got.Window+" rev-a 0 "+e.Home {
		t.Errorf("window = %q", w)
	}
	if args := e.args(t, "agent"); !slices.Equal(args, []string{"--agent", "wb x"}) {
		t.Errorf("agent args = %q", args)
	}
	if run := e.MustTmux(t, "show-options", "-pqv", "-t", got.Pane, "@agent_run"); run != e.file(t, "agent.ppid") {
		t.Errorf("the agent's parent is %s, the launcher %s", e.file(t, "agent.ppid"), run)
	}
	if ch := e.MustTmux(t, "show-options", "-pqv", "-t", got.Pane, "@agent_starting"); ch != "" {
		t.Errorf("@agent_starting = %q after the start", ch)
	}

	t.Run("a name in use", func(t *testing.T) {
		before := len(e.windowNames(t))
		if _, stderr, code := e.start(t, "--name", "rev-a", "claude"); code != 3 || !strings.Contains(stderr, got.Window) {
			t.Errorf("exit %d, %q; want 3 and the open window", code, stderr)
		}
		if n := len(e.windowNames(t)); n != before {
			t.Errorf("%d windows, want %d", n, before)
		}
	})
	t.Run("a shell stays after the agent", func(t *testing.T) {
		e.release(t)
		tmuxtest.WaitFor(t, 10*time.Second, "the launcher's end", func() bool {
			return e.MustTmux(t, "show-options", "-pqv", "-t", got.Pane, "@agent_run") == ""
		})
		if dead := e.Display(t, got.Pane, "#{pane_dead}"); dead != "0" {
			t.Errorf("pane_dead = %q", dead)
		}
	})
}

// An agent with no hooks is ready once the launcher started it.
func TestStartWithoutHooks(t *testing.T) {
	e := newEnv(t)
	e.stubEnv(t, "STUB_MODE", "wait")
	out, stderr, code := e.start(t, "fakeagent")
	if code != 0 || !strings.HasPrefix(out, "%") {
		t.Fatalf("exit %d, %q, %s", code, out, stderr)
	}
	e.file(t, "agent.pid")
	e.release(t)
}

// An agent that ends before its session starts ends the wait at once, and
// start says how it ended.
func TestStartEndsBeforeReady(t *testing.T) {
	e := newEnv(t)
	e.stubEnv(t, "STUB_MODE", "exit", "STUB_CODE", "4")
	began := time.Now()
	_, stderr, code := e.start(t, "--timeout", "20s", "claude")
	if code != 1 || !strings.Contains(stderr, "ended before it was ready: failed: exit 4") {
		t.Errorf("exit %d, %q", code, stderr)
	}
	if d := time.Since(began); d > 10*time.Second {
		t.Errorf("took %s: start waited for its timeout", d)
	}
}

// A missing program ends the wait too.
func TestStartMissingProgram(t *testing.T) {
	e := newEnv(t)
	path, err := e.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := e.start(t, "--timeout", "20s", "claude")
	if code != 1 || !strings.Contains(stderr, "ended before it was ready") {
		t.Errorf("exit %d, %q", code, stderr)
	}
}

// A session that never starts runs into the timeout; the window stays.
func TestStartTimeout(t *testing.T) {
	e := newEnv(t)
	e.stubEnv(t, "STUB_MODE", "wait")
	_, stderr, code := e.start(t, "--name", "rev-t", "--timeout", "1s", "claude")
	if code != 1 || !strings.Contains(stderr, "not ready after 1s") {
		t.Errorf("exit %d, %q", code, stderr)
	}
	if !slices.Contains(e.windowNames(t), "rev-t") {
		t.Error("the window went")
	}
	pane := e.MustTmux(t, "list-panes", "-t", "=proj-a:rev-t", "-F", "#{pane_id}")
	if ch := e.MustTmux(t, "show-options", "-pqv", "-t", pane, "@agent_starting"); ch != "" {
		t.Errorf("@agent_starting = %q: a late start would signal no one", ch)
	}
	e.release(t)
}

// close closes every window with a name, in every session, and the
// caller's own window last; other windows stay.
func TestClose(t *testing.T) {
	e := newEnv(t)
	e.MustTmux(t, "rename-window", "-t", e.pane, "rev-c")
	e.MustTmux(t, "new-window", "-d", "-t", "proj-a:", "-n", "rev-b")
	e.MustTmux(t, "new-window", "-d", "-t", "proj-a:", "-n", "keep")
	e.MustTmux(t, "new-session", "-d", "-s", "proj-b", "-n", "rev-b")
	e.MustTmux(t, "new-window", "-d", "-t", "proj-b:", "-n", "keep")
	cmd := e.Command(t, e.pane, "tmux-agent", "close", "rev-b", "rev-c", "none")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out2str(out), "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[0], " rev-b") || !strings.HasSuffix(lines[1], " rev-b") || !strings.HasSuffix(lines[2], " rev-c") {
		t.Errorf("close printed %q", lines)
	}
	if names := e.windowNames(t); !slices.Equal(names, []string{"keep", "keep"}) {
		t.Errorf("windows left: %q", names)
	}
}
