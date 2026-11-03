package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/selftest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

func TestMain(m *testing.M) { selftest.Main(m) }

const (
	sessionA = "11111111-2222-4333-8444-555555555555"
	sessionB = "66666666-7777-4888-9999-aaaaaaaaaaaa"
)

// agents are the agents every launcher test runs: Claude Code, and an
// agent the registry does not know, with only a name and a command.
var agents = []string{"claude", "fakeagent"}

// stub stands in for an agent's program. It records how it was started in
// $STUB_DIR/$STUB_TAG.* (tag "agent" by default), sends each line of
// $STUB_HOOK to tmux-agent hook claude, writes its pid as the sign that it
// is ready, and then does what $STUB_MODE says:
//
//	exit    exit with $STUB_CODE (0 by default)
//	wait    wait for $STUB_DIR/release, and die from Ctrl-C; with
//	        $STUB_TRAP set, record Ctrl-C and Ctrl-\ instead
//	raw     read keys in raw mode, as a TUI does: Ctrl-Z stops the stub
//	        alone, and q ends it. (bash's read -n turns the terminal's
//	        signals back on, so head reads the keys.)
//	die     kill itself with $STUB_SIG (TERM by default)
//	nested  run $STUB_INNER as a tool call would, with $STUB_INNER_HOOK as
//	        its hooks, then wait
//
// It waits on a FIFO, not with sleep, so the terminal's signals reach no
// child of its own.
const stub = `#!/usr/bin/env bash
set -uo pipefail
d=${STUB_DIR:?}
tag=${STUB_TAG:-agent}
printf '%s\n' "${TMUX_AGENT_RUN-}" > "$d/$tag.run"
printf '%s\n' "$PPID" > "$d/$tag.ppid"
printf '%s\0' "$@" > "$d/$tag.args"
if [[ -n ${STUB_HOOK:-} ]]; then
  while IFS= read -r line; do
    tmux-agent hook claude <<< "$line"
  done < "$STUB_HOOK"
fi
mkfifo "$d/$tag.fifo"
exec {nap}<> "$d/$tag.fifo"
wait_release() {
  until [[ -e $d/release ]]; do read -r -t 0.02 -u "$nap" _ || true; done
}
printf '%s\n' "$$" > "$d/$tag.pid.tmp"
mv "$d/$tag.pid.tmp" "$d/$tag.pid"
case ${STUB_MODE:-exit} in
  wait)
    if [[ -n ${STUB_TRAP:-} ]]; then
      trap 'echo INT > "$d/$tag.INT"' INT
      trap 'echo QUIT > "$d/$tag.QUIT"' QUIT
    else
      trap 'trap - INT; kill -INT "$$"' INT
    fi
    wait_release
    ;;
  raw)
    saved=$(stty -g)
    stty raw -echo
    echo raw > "$d/$tag.raw"
    while c=$(head -c1) && [[ -n $c ]]; do
      case $c in
        $'\x1a')
          stty "$saved"
          kill -TSTP "$$"
          stty raw -echo
          echo cont > "$d/$tag.cont"
          ;;
        q) break ;;
      esac
    done
    stty "$saved"
    ;;
  die) kill -"${STUB_SIG:-TERM}" "$$" ;;
  nested)
    STUB_TAG=inner STUB_MODE=exit STUB_HOOK=${STUB_INNER_HOOK:-} $STUB_INNER
    echo "$?" > "$d/inner.rc"
    wait_release
    ;;
esac
exit "${STUB_CODE:-0}"
`

// config gives every pane a plain interactive bash, with job control.
const config = tmuxtest.DefaultConfig + `set -g default-command "exec bash --norc --noprofile"
`

// env is one private server with one pane, its shell at a prompt.
type env struct {
	*tmuxtest.Server
	pane string
	dir  string // the stub's records, the shell's exit codes, the release file
}

func sandbox(t *testing.T) (*tmuxtest.Sandbox, string) {
	t.Helper()
	tmuxtest.Require(t, "tmux", "bash", "stty", "mkfifo")
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	for _, name := range agents {
		sb.Stub(t, name, stub)
	}
	dir := filepath.Join(sb.Dir, "stub")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sb.Setenv("STUB_DIR", dir)
	sb.Setenv("PS1", "$ ")
	sb.Setenv("HISTFILE", "/dev/null")
	return sb, dir
}

func newEnv(t *testing.T) *env {
	t.Helper()
	sb, dir := sandbox(t)
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Config: config, Session: "proj-a"})
	e := &env{Server: s, pane: s.Display(t, "proj-a:0.0", "#{pane_id}"), dir: dir}
	s.WaitForContent(t, e.pane, "$")
	t.Cleanup(func() {
		if t.Failed() {
			e.dump(t)
		}
	})
	return e
}

// dump logs what a failed test left: the pane's screen and the processes
// in it.
func (e *env) dump(t *testing.T) {
	screen, _ := e.Tmux("capture-pane", "-p", "-t", e.pane)
	t.Logf("pane:\n%s", screen)
	shell, _ := e.Tmux("display-message", "-p", "-t", e.pane, "#{pane_pid}")
	out, _ := exec.Command("ps", "-o", "pid,ppid,pgid,tpgid,stat,args", "--ppid", shell, "-g", shell).CombinedOutput()
	t.Logf("processes:\n%s", out)
}

// each runs fn for every agent, each in its own server.
func each(t *testing.T, fn func(t *testing.T, e *env, agent string)) {
	t.Helper()
	for _, a := range agents {
		t.Run(a, func(t *testing.T) {
			t.Parallel()
			fn(t, newEnv(t), a)
		})
	}
}

// typeLine types a command line into the pane's shell.
func (e *env) typeLine(t *testing.T, line string) {
	t.Helper()
	e.MustTmux(t, "send-keys", "-t", e.pane, "-l", line)
	e.MustTmux(t, "send-keys", "-t", e.pane, "Enter")
}

// launch types line into the shell; the shell saves its exit code as rc.
func (e *env) launch(t *testing.T, rc, line string) {
	t.Helper()
	e.typeLine(t, line+`; echo $? > "$STUB_DIR/`+rc+`"`)
}

func (e *env) keys(t *testing.T, keys ...string) {
	t.Helper()
	e.MustTmux(t, append([]string{"send-keys", "-t", e.pane}, keys...)...)
}

// waitFile waits for a file the stub or the shell writes whole, ending in
// a newline, and returns it without that.
func waitFile(t *testing.T, dir, name string) string {
	t.Helper()
	var b []byte
	tmuxtest.WaitFor(t, 10*time.Second, name, func() bool {
		var err error
		b, err = os.ReadFile(filepath.Join(dir, name))
		return err == nil && len(b) > 0 && b[len(b)-1] == '\n'
	})
	return strings.TrimSuffix(string(b), "\n")
}

func (e *env) file(t *testing.T, name string) string {
	t.Helper()
	return waitFile(t, e.dir, name)
}

func (e *env) number(t *testing.T, name string) int {
	t.Helper()
	n, err := strconv.Atoi(e.file(t, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return n
}

// args returns the arguments the stub with tag was started with.
func (e *env) args(t *testing.T, tag string) []string {
	t.Helper()
	e.file(t, tag+".pid")
	b, err := os.ReadFile(filepath.Join(e.dir, tag+".args"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

func (e *env) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *env) opt(t *testing.T, name string) string {
	t.Helper()
	return e.MustTmux(t, "show-options", "-pqv", "-t", e.pane, name)
}

// run is the launcher's pid, as the pane records it.
func (e *env) run(t *testing.T) int {
	t.Helper()
	n, err := strconv.Atoi(e.opt(t, "@agent_run"))
	if err != nil {
		t.Fatalf("@agent_run: %v", err)
	}
	return n
}

func (e *env) wantFlag(t *testing.T, state, reason string) {
	t.Helper()
	gotState, gotReason := e.opt(t, "@attention"), e.opt(t, "@attention_reason")
	if gotState != state || gotReason != reason {
		t.Errorf("flag = %q %q, want %q %q", gotState, gotReason, state, reason)
	}
}

func (e *env) wantOpt(t *testing.T, name, want string) {
	t.Helper()
	if got := e.opt(t, name); got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func (e *env) record(t *testing.T, id string) store.Session {
	t.Helper()
	st := &store.Store{Dir: filepath.Join(e.StateHome, "tmux-agent")}
	r, ok, err := st.Load("claude", id)
	if err != nil || !ok {
		t.Fatalf("record %s: ok=%v, %v", id, ok, err)
	}
	return r
}

type notification struct{ title, body string }

func (e *env) notifications() []notification {
	var ns []notification
	for _, c := range e.Calls("notify-send") {
		if slices.Contains(c.Args, "--help") || len(c.Args) < 2 {
			continue
		}
		ns = append(ns, notification{c.Args[len(c.Args)-2], c.Args[len(c.Args)-1]})
	}
	return ns
}

// wantNotification waits for the one notification there must be, then past
// the notifier's settle second, so a second one would show.
func (e *env) wantNotification(t *testing.T, want notification) {
	t.Helper()
	tmuxtest.WaitFor(t, 5*time.Second, "a notification", func() bool { return len(e.notifications()) > 0 })
	time.Sleep(2500 * time.Millisecond)
	if ns := e.notifications(); len(ns) != 1 || ns[0] != want {
		t.Errorf("notifications = %+v, want only %+v", ns, want)
	}
}

// hooks writes hook events, one JSON object per line, for the stub to send.
func (e *env) hooks(t *testing.T, name string, events ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, ev := range events {
		j, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	path := filepath.Join(e.dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hookEvent(name, session string, fields ...any) map[string]any {
	ev := map[string]any{"hook_event_name": name, "session_id": session}
	for i := 0; i+1 < len(fields); i += 2 {
		ev[fields[i].(string)] = fields[i+1]
	}
	return ev
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// procState reads a process's state (R, S, T and so on) and parent.
func procState(t *testing.T, pid int) (state byte, ppid int) {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("process %d: %v", pid, err)
	}
	f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	ppid, err = strconv.Atoi(f[1])
	if err != nil {
		t.Fatal(err)
	}
	return f[0][0], ppid
}

// The pane runs shell, launcher, agent: the launcher waits on the same
// terminal under the agent's name, and the agent gets the run's pid.
func TestRunProcessTree(t *testing.T) {
	each(t, func(t *testing.T, e *env, agent string) {
		e.launch(t, "rc", "STUB_MODE=wait tmux-agent run "+agent+" --model x 'two words'")
		if got, want := e.args(t, "agent"), []string{"--model", "x", "two words"}; !slices.Equal(got, want) {
			t.Errorf("agent args = %q, want %q", got, want)
		}
		run := e.run(t)
		if got := e.file(t, "agent.run"); got != strconv.Itoa(run) {
			t.Errorf("TMUX_AGENT_RUN = %q, want the launcher's pid %d", got, run)
		}
		if _, ppid := procState(t, e.number(t, "agent.pid")); ppid != run {
			t.Errorf("the agent's parent is %d, want the launcher %d", ppid, run)
		}
		if _, ppid := procState(t, run); strconv.Itoa(ppid) != e.Display(t, e.pane, "#{pane_pid}") {
			t.Errorf("the launcher's parent is %d, want the pane's shell", ppid)
		}
		e.wantOpt(t, "@agent", agent)
		if got := e.Display(t, e.pane, "#{pane_current_command}"); got != agent {
			t.Errorf("pane_current_command = %q, want %q", got, agent)
		}
		e.release(t)
		if rc := e.number(t, "rc"); rc != 0 {
			t.Errorf("exit code = %d, want 0", rc)
		}
		e.wantOpt(t, "@agent_run", "")
		e.wantOpt(t, "@agent", "")
		e.wantFlag(t, "", "")
	})
}

// The agent's exit code is the launcher's, and an interactive run that
// fails is flagged.
func TestRunExitCode(t *testing.T) {
	each(t, func(t *testing.T, e *env, agent string) {
		e.launch(t, "rc", "STUB_CODE=3 tmux-agent run "+agent)
		if rc := e.number(t, "rc"); rc != 3 {
			t.Errorf("exit code = %d, want 3", rc)
		}
		e.wantFlag(t, "done", "failed: exit 3")
	})
}

// An agent that dies from a signal takes the launcher with it, so the shell
// sees 128+n.
func TestRunSignalDeath(t *testing.T) {
	each(t, func(t *testing.T, e *env, agent string) {
		e.launch(t, "rc", "STUB_MODE=die tmux-agent run "+agent+" && echo ran > \"$STUB_DIR/next\"")
		if rc := e.number(t, "rc"); rc != 128+int(syscall.SIGTERM) {
			t.Errorf("exit code = %d, want %d", rc, 128+int(syscall.SIGTERM))
		}
		if _, err := os.Stat(filepath.Join(e.dir, "next")); err == nil {
			t.Error("&& ran the next command after the agent was killed")
		}
		e.wantFlag(t, "done", "failed: terminated")
	})
}

// Outside tmux the launcher runs the agent in its place.
func TestRunOutsideTmux(t *testing.T) {
	for _, agent := range agents {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			sb, dir := sandbox(t)
			cmd := sb.Cmd(t, "tmux-agent", "run", agent, "a1")
			cmd.Env = append(cmd.Env, "STUB_CODE=3")
			err := cmd.Run()
			if ee, ok := errors.AsType[*exec.ExitError](err); !ok || ee.ExitCode() != 3 {
				t.Fatalf("run = %v, want exit 3", err)
			}
			if got := waitFile(t, dir, "agent.run"); got != "" {
				t.Errorf("TMUX_AGENT_RUN = %q outside tmux, want unset", got)
			}
			if ppid := waitFile(t, dir, "agent.ppid"); ppid != strconv.Itoa(os.Getpid()) {
				t.Errorf("the agent's parent is %s, want this test: the launcher runs it in its place", ppid)
			}
		})
	}
}

// The launcher outlives Ctrl-C and Ctrl-\, which reach the agent from the
// terminal.
func TestRunCtrlCTrapped(t *testing.T) {
	for _, c := range []struct{ name, key, sig string }{
		{"Ctrl-C", "C-c", "INT"},
		{`Ctrl-\`, `C-\`, "QUIT"},
	} {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(t *testing.T, e *env, agent string) {
				e.launch(t, "rc", "STUB_TRAP=1 STUB_MODE=wait tmux-agent run "+agent)
				e.file(t, "agent.pid")
				e.keys(t, c.key)
				e.file(t, "agent."+c.sig)
				e.release(t)
				if rc := e.number(t, "rc"); rc != 0 {
					t.Errorf("exit code = %d, want 0: the agent's", rc)
				}
				e.wantFlag(t, "", "")
			})
		})
	}
}

// An agent that dies from Ctrl-C takes the launcher with it by the same
// signal, so the shell drops the rest of the command line, as it would
// without the launcher.
func TestRunCtrlCKills(t *testing.T) {
	each(t, func(t *testing.T, e *env, agent string) {
		e.launch(t, "rc", "STUB_MODE=wait tmux-agent run "+agent)
		e.file(t, "agent.pid")
		e.keys(t, "C-c")
		tmuxtest.WaitFor(t, 10*time.Second, "the run to end", func() bool { return e.opt(t, "@agent_run") == "" })
		e.typeLine(t, `echo $? > "$STUB_DIR/after"`)
		if rc := e.number(t, "after"); rc != 130 {
			t.Errorf("$? = %d, want 130", rc)
		}
		if _, err := os.Stat(filepath.Join(e.dir, "rc")); err == nil {
			t.Error("the shell went on with the command line: the launcher exited instead of dying from SIGINT")
		}
		e.wantFlag(t, "done", "failed: interrupt")
	})
}

// Ctrl-Z stops the agent and the launcher, the shell gets the terminal, and
// fg resumes both. A TUI reads Ctrl-Z as a key and stops only itself; in
// cooked mode the terminal stops the whole job.
func TestRunCtrlZ(t *testing.T) {
	for _, c := range []struct{ name, mode, ready string }{
		{"raw", "raw", "agent.raw"},
		{"cooked", "wait", "agent.pid"},
	} {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(t *testing.T, e *env, agent string) {
				e.launch(t, "rc1", "STUB_MODE="+c.mode+" tmux-agent run "+agent)
				e.file(t, c.ready)
				pid, run := e.number(t, "agent.pid"), e.run(t)
				e.keys(t, "C-z")
				if rc := e.number(t, "rc1"); rc != 128+int(syscall.SIGTSTP) {
					t.Fatalf("the shell's $? = %d, want %d: the job stopped", rc, 128+int(syscall.SIGTSTP))
				}
				for _, p := range []struct {
					name string
					pid  int
				}{{"agent", pid}, {"launcher", run}} {
					if state, _ := procState(t, p.pid); state != 'T' {
						t.Errorf("the %s is in state %c, want stopped", p.name, state)
					}
				}
				e.wantOpt(t, "@agent_run", strconv.Itoa(run))

				e.typeLine(t, `fg; echo $? > "$STUB_DIR/rc2"`)
				if c.mode == "raw" {
					e.file(t, "agent.cont")
					e.keys(t, "-l", "q")
				} else {
					e.release(t)
				}
				if rc := e.number(t, "rc2"); rc != 0 {
					t.Errorf("fg = %d, want 0: both resumed and the agent ended", rc)
				}
				e.wantFlag(t, "", "")
			})
		})
	}
}

// The launcher killed with SIGTERM or SIGHUP (a closing terminal, a
// shutdown) passes the signal on and keeps the session for a resume; an
// agent that ends by itself lets it go.
func TestRunEnd(t *testing.T) {
	resume := "tmux-agent run claude --resume " + sessionA
	for _, c := range []struct {
		name string
		sig  syscall.Signal
	}{{"SIGTERM", syscall.SIGTERM}, {"SIGHUP", syscall.SIGHUP}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			hooks := e.hooks(t, "start.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home))
			e.launch(t, "rc", "STUB_MODE=wait STUB_HOOK="+shellQuote(hooks)+" tmux-agent run claude")
			e.file(t, "agent.pid")
			e.wantOpt(t, "@revive_cmd", resume)
			if err := syscall.Kill(e.run(t), c.sig); err != nil {
				t.Fatal(err)
			}
			if rc := e.number(t, "rc"); rc != 128+int(c.sig) {
				t.Errorf("exit code = %d, want %d", rc, 128+int(c.sig))
			}
			e.wantOpt(t, "@revive_cmd", resume)
			e.wantOpt(t, "@agent_session", sessionA)
			e.wantOpt(t, "@agent_run", "")
			if r := e.record(t, sessionA); r.End != "killed" {
				t.Errorf("record end = %q, want killed", r.End)
			}
		})
	}
	t.Run("exit", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		hooks := e.hooks(t, "start.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home))
		e.launch(t, "rc", "STUB_HOOK="+shellQuote(hooks)+" tmux-agent run claude")
		if rc := e.number(t, "rc"); rc != 0 {
			t.Errorf("exit code = %d, want 0", rc)
		}
		for _, o := range []string{"@revive_cmd", "@agent_session", "@agent", "@agent_run"} {
			e.wantOpt(t, o, "")
		}
		if r := e.record(t, sessionA); r.End != "exited" {
			t.Errorf("record end = %q, want exited", r.End)
		}
	})
}

// A one-shot run is one turn: working from launch, done or failed at exit,
// with one notification. Its own hooks only name the session, and it leaves
// no resume command.
func TestRunOneShot(t *testing.T) {
	for _, c := range []struct {
		name, code, reason string
	}{{"finished", "0", "finished"}, {"failed", "2", "failed: exit 2"}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.MustTmux(t, "rename-window", "-t", e.pane, "w-a")
			hooks := e.hooks(t, "turn.jsonl",
				hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home),
				hookEvent("UserPromptSubmit", sessionA, "prompt", "say hi"),
				hookEvent("Stop", sessionA),
			)
			e.launch(t, "rc", "STUB_CODE="+c.code+" STUB_MODE=wait STUB_HOOK="+shellQuote(hooks)+" tmux-agent run claude -p 'say hi'")
			e.file(t, "agent.pid")
			e.wantFlag(t, "working", "")
			e.wantOpt(t, "@agent_session", sessionA)
			e.wantOpt(t, "@revive_cmd", "")
			e.release(t)
			if rc := e.number(t, "rc"); strconv.Itoa(rc) != c.code {
				t.Errorf("exit code = %d, want %s", rc, c.code)
			}
			e.wantFlag(t, "done", c.reason)
			e.wantNotification(t, notification{"w-a is done", c.reason})
			e.wantOpt(t, "@revive_cmd", "")
		})
	}
}

// A claude started inside the agent's tool call inherits the pane and the
// run; its hooks leave both alone, whether it bypasses the launcher or goes
// through it.
func TestRunNestedAgent(t *testing.T) {
	for _, c := range []struct{ name, inner string }{
		{"direct", "claude -p inner"},
		{"launcher", "tmux-agent run claude -p inner"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			outer := e.hooks(t, "outer.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home))
			inner := e.hooks(t, "inner.jsonl",
				hookEvent("SessionStart", sessionB, "source", "startup", "cwd", e.Home),
				hookEvent("UserPromptSubmit", sessionB, "prompt", "inner"),
				hookEvent("Stop", sessionB),
				hookEvent("SessionEnd", sessionB, "reason", "other"),
			)
			e.launch(t, "rc", fmt.Sprintf("STUB_MODE=nested STUB_HOOK=%s STUB_INNER_HOOK=%s STUB_INNER=%s tmux-agent run claude",
				shellQuote(outer), shellQuote(inner), shellQuote(c.inner)))
			e.file(t, "inner.rc")
			run := e.run(t)
			if got := e.file(t, "inner.run"); got != strconv.Itoa(run) {
				t.Fatalf("the inner agent's TMUX_AGENT_RUN = %q, want the outer run %d", got, run)
			}
			if got, want := e.file(t, "inner.ppid"), e.file(t, "agent.pid"); got != want {
				t.Errorf("the inner agent's parent is %s, want the outer agent %s", got, want)
			}
			e.wantOpt(t, "@agent_session", sessionA)
			e.wantOpt(t, "@revive_cmd", "tmux-agent run claude --resume "+sessionA)
			e.wantFlag(t, "", "")
			e.release(t)
			if rc := e.number(t, "rc"); rc != 0 {
				t.Errorf("exit code = %d, want 0", rc)
			}
		})
	}
}

// --resume names the session the run resumes, so its start brings back what
// the session was waiting on before the restart.
func TestRunResume(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	st := &store.Store{Dir: filepath.Join(e.StateHome, "tmux-agent")}
	if err := st.Save(store.Session{
		Agent: "claude", ID: sessionA, Cwd: e.Home, State: "needs-you", Reason: "Bash(make)",
		Title: "write the docs", Updated: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	hooks := e.hooks(t, "resume.jsonl", hookEvent("SessionStart", sessionA, "source", "resume", "cwd", e.Home))
	e.launch(t, "rc", "STUB_MODE=wait STUB_HOOK="+shellQuote(hooks)+" tmux-agent run claude --resume "+sessionA)
	e.file(t, "agent.pid")
	e.wantFlag(t, "needs-you", "before restart: Bash(make)")
	if got := e.Display(t, e.pane, "#{window_name}"); got != "write the docs" {
		t.Errorf("window = %q, want the recorded title", got)
	}
	e.wantOpt(t, "@agent_resume", "")
	e.release(t)
	e.number(t, "rc")
}
