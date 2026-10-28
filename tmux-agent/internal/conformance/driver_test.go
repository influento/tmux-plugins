package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// Flag is a pane flag as a program sets it.
type Flag struct {
	State  string // needs-you, done or working
	Reason string
	Owner  string // the command the flag lives with; empty for none
	Quiet  bool   // no notification
}

// ListRow is one pane in the list behind the pickers and the jump key.
type ListRow struct {
	Rank   int
	Pane   string
	Glyph  string
	Window string
	Target string
	Detail string
	Age    string
}

// SessionRow is one row of the session picker.
type SessionRow struct {
	Session string
	Display string // what the row shows, without colour codes
}

// Event is one hook event, as the agent sends it on stdin.
type Event struct {
	Name    string         // hook_event_name
	Payload map[string]any // the other fields
}

// JSON is the event as the agent writes it.
func (e Event) JSON() string {
	m := map[string]any{"hook_event_name": e.Name}
	for k, v := range e.Payload {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// Record is what an implementation remembers about the agent in a pane, as
// far as a user can tell: whether the conversation would resume after a
// restart, and with what.
type Record struct {
	Exists     bool
	SessionID  string
	Transcript string
	Cwd        string
	State      string // the last flag; "idle" or empty for none
	Reason     string
	Title      string
}

// Driver runs one implementation. Every method acts on the given server and
// fails the test when the implementation cannot run.
type Driver interface {
	Name() string
	// Setup installs the implementation into the sandbox before the server
	// starts.
	Setup(t *testing.T, sb *tmuxtest.Sandbox)

	// Flags (tmux-attention today).
	Set(t *testing.T, s *tmuxtest.Server, pane string, f Flag) (prev string)
	Clear(t *testing.T, s *tmuxtest.Server, pane string)
	Seen(t *testing.T, s *tmuxtest.Server, pane string)
	Status(t *testing.T, s *tmuxtest.Server)
	Sweep(t *testing.T, s *tmuxtest.Server)
	List(t *testing.T, s *tmuxtest.Server) []ListRow
	SessionRows(t *testing.T, s *tmuxtest.Server) []SessionRow
	KillSession(t *testing.T, s *tmuxtest.Server, name string)
	Jump(t *testing.T, s *tmuxtest.Server, from string) // prefix o, pressed in pane from
	Borders(t *testing.T, s *tmuxtest.Server, window string)

	// The agent integration (claude-tmux today).
	Hook(t *testing.T, s *tmuxtest.Server, pane string, ev Event) (stdout string)
	HookOutsideTmux(t *testing.T, s *tmuxtest.Server, ev Event) (stdout string, err error)
	AgentSweep(t *testing.T, s *tmuxtest.Server)
	Record(t *testing.T, s *tmuxtest.Server, pane string) Record
	// SeedRecord leaves what a restore leaves before the agent's
	// SessionStart arrives: a record for pane, marked as being resumed when
	// resuming is true.
	SeedRecord(t *testing.T, s *tmuxtest.Server, pane string, r Record, resuming bool)
	TitleOption() string

	// KnownBug returns why the implementation is known to fail a case, or "".
	KnownBug(name string) string
}

// drivers returns the implementations to check. The bash scripts need a
// dotfiles checkout; the Go driver joins in P0 step 2.
func drivers(t *testing.T) []Driver {
	t.Helper()
	var ds []Driver
	if dir := os.Getenv("TMUX_PLUGINS_DOTFILES"); dir != "" {
		ds = append(ds, &bashDriver{dotfiles: dir})
	} else if os.Getenv("TMUXTEST_REQUIRE") != "" {
		t.Fatal("TMUX_PLUGINS_DOTFILES is not set (TMUXTEST_REQUIRE is)")
	}
	if len(ds) == 0 {
		t.Skip("no implementation to check: set TMUX_PLUGINS_DOTFILES for the bash scripts")
	}
	return ds
}

// env is one case's server and driver.
type env struct {
	*tmuxtest.Server
	d          Driver
	fakeClaude string // a program tmux reports as "claude"
}

// testConfig is the tmux config every case starts with: theme colours for
// the flags, and focus events so a test can blur a client.
const testConfig = tmuxtest.DefaultConfig + `set -g focus-events on
set -g @attention_fg_needs_you "#ff0001"
set -g @attention_fg_done "#00ff02"
set -g @attention_fg_working "#0000f3"
set -g @attention_fg_dim "#444444"
`

// each runs fn once per driver, each on a fresh server whose first session
// is "proj-a" with one shell pane.
func each(t *testing.T, fn func(t *testing.T, e *env)) {
	name := t.Name()
	for _, d := range drivers(t) {
		t.Run(d.Name(), func(t *testing.T) {
			t.Parallel()
			if why := d.KnownBug(name); why != "" {
				t.Skipf("known %s bug: %s", d.Name(), why)
			}
			fn(t, newEnv(t, d, nil))
		})
	}
}

// newEnv starts a server for d. prepare, when not nil, runs on the sandbox
// first.
func newEnv(t *testing.T, d Driver, prepare func(sb *tmuxtest.Sandbox)) *env {
	t.Helper()
	tmuxtest.Require(t, "tmux")
	sb := tmuxtest.NewSandbox(t)
	fake := filepath.Join(sb.Dir, "agent-bin")
	if err := os.MkdirAll(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	// sleep under the name claude, so tmux reports the pane's command as
	// "claude" and the process stays until the test kills it. A symlink, not
	// a copy: a binary written by this process can fail to start with
	// ETXTBSY while parallel tests fork.
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sleep, filepath.Join(fake, "claude")); err != nil {
		t.Fatal(err)
	}
	d.Setup(t, sb)
	if prepare != nil {
		prepare(sb)
	}
	srv := tmuxtest.StartIn(t, sb, tmuxtest.Options{Config: testConfig, Session: "proj-a"})
	return &env{Server: srv, d: d, fakeClaude: filepath.Join(fake, "claude")}
}

// pane returns the id of the pane target names.
func (e *env) pane(t *testing.T, target string) string {
	t.Helper()
	return e.Display(t, target, "#{pane_id}")
}

// firstPane is the shell pane the server started with.
func (e *env) firstPane(t *testing.T) string { return e.pane(t, "proj-a:0.0") }

// newWindow opens a window in session running command (a shell when
// empty) and returns its pane id.
func (e *env) newWindow(t *testing.T, session, name, command string) string {
	t.Helper()
	args := []string{"new-window", "-d", "-P", "-F", "#{pane_id}", "-t", session + ":"}
	if name != "" {
		args = append(args, "-n", name)
	}
	if command != "" {
		args = append(args, command)
	}
	return e.MustTmux(t, args...)
}

// newSession creates a detached session and returns its first pane id.
func (e *env) newSession(t *testing.T, name, command string) string {
	t.Helper()
	args := []string{"new-session", "-d", "-P", "-F", "#{pane_id}", "-s", name}
	if command != "" {
		args = append(args, command)
	}
	return e.MustTmux(t, args...)
}

// split splits the window of pane and returns the new pane's id.
func (e *env) split(t *testing.T, pane, command string) string {
	t.Helper()
	args := []string{"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", pane}
	if command != "" {
		args = append(args, command)
	}
	return e.MustTmux(t, args...)
}

// claudeWindow opens a window running the fake claude and waits until tmux
// names its command "claude".
func (e *env) claudeWindow(t *testing.T, session string) string {
	t.Helper()
	p := e.newWindow(t, session, "", e.claudeCommand())
	e.waitCommand(t, p, "claude")
	return p
}

// claudeSplit splits pane's window with the fake claude.
func (e *env) claudeSplit(t *testing.T, pane string) string {
	t.Helper()
	p := e.split(t, pane, e.claudeCommand())
	e.waitCommand(t, p, "claude")
	return p
}

// claudeCommand is the shell command that starts the fake claude. The path
// is quoted: test names, and so temp paths, can hold shell syntax.
func (e *env) claudeCommand() string {
	return "'" + strings.ReplaceAll(e.fakeClaude, "'", `'\''`) + "' 3600"
}

func (e *env) waitCommand(t *testing.T, pane, cmd string) {
	t.Helper()
	tmuxtest.WaitFor(t, 5*time.Second, pane+" running "+cmd, func() bool {
		out, err := e.Tmux("display-message", "-p", "-t", pane, "#{pane_current_command}")
		return err == nil && out == cmd
	})
}

// opt reads an option; "" when unset. scope is -p, -w or -g.
func (e *env) opt(t *testing.T, scope, target, name string) string {
	t.Helper()
	args := []string{"show-options", "-qv", scope}
	if target != "" {
		args = append(args, "-t", target)
	}
	out, err := e.Tmux(append(args, name)...)
	if err != nil {
		t.Fatalf("show-options %s %s: %v", scope, name, err)
	}
	return out
}

// flag returns the pane's state and reason.
func (e *env) flag(t *testing.T, pane string) (state, reason string) {
	t.Helper()
	return e.opt(t, "-p", pane, "@attention"), e.opt(t, "-p", pane, "@attention_reason")
}

func (e *env) wantFlag(t *testing.T, pane, state, reason string) {
	t.Helper()
	gotState, gotReason := e.flag(t, pane)
	if gotState != state || gotReason != reason {
		t.Errorf("pane %s flag = %q %q, want %q %q", pane, gotState, gotReason, state, reason)
	}
}

// windowName returns the window's name and whether tmux still names it.
func (e *env) windowName(t *testing.T, pane string) (name string, automatic bool) {
	t.Helper()
	out := e.Display(t, pane, "#{automatic-rename}\t#{window_name}")
	auto, name, _ := strings.Cut(out, "\t")
	return name, auto == "1"
}

// Notification is one desktop notification sent.
type Notification struct{ Title, Body string }

// notifications returns the notifications sent so far. A stub call that only
// probed notify-send's options is not one.
func (e *env) notifications() []Notification {
	var ns []Notification
	for _, c := range e.Calls("notify-send") {
		if slices.Contains(c.Args, "--help") || len(c.Args) < 2 {
			continue
		}
		ns = append(ns, Notification{Title: c.Args[len(c.Args)-2], Body: c.Args[len(c.Args)-1]})
	}
	return ns
}

// notifySettle is how long a test waits before it decides no notification
// came: the implementations wait a second before sending one.
const notifySettle = 2500 * time.Millisecond

func (e *env) waitNotifications(t *testing.T, n int) []Notification {
	t.Helper()
	tmuxtest.WaitFor(t, 5*time.Second, fmt.Sprintf("%d notification(s)", n), func() bool {
		return len(e.notifications()) >= n
	})
	return e.notifications()
}

func (e *env) noNotification(t *testing.T) {
	t.Helper()
	time.Sleep(notifySettle)
	if ns := e.notifications(); len(ns) > 0 {
		t.Errorf("notifications = %+v, want none", ns)
	}
}

// writeTranscript writes a transcript of JSONL lines and returns its path.
func (e *env) writeTranscript(t *testing.T, lines ...map[string]any) string {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		j, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	f, err := os.CreateTemp(e.Dir, "transcript-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

// userPrompt is a transcript line holding a prompt the user typed.
func userPrompt(text string) map[string]any {
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}
}

// setAge moves a pane's flag time back by d, so ages and order can be tested
// without waiting.
func (e *env) setAge(t *testing.T, pane string, d time.Duration) {
	t.Helper()
	at := strconv.FormatInt(time.Now().Add(-d).Unix(), 10)
	e.MustTmux(t, "set-option", "-p", "-t", pane, "@attention_at", at)
}

// run runs a program as if from inside pane, feeding stdin, and returns its
// stdout. A pane of "" runs it outside tmux.
func run(t *testing.T, s *tmuxtest.Server, pane, stdin, name string, args ...string) (string, error) {
	t.Helper()
	var cmd *exec.Cmd
	if pane == "" {
		cmd = s.Cmd(t, name, args...)
	} else {
		cmd = s.Command(t, pane, name, args...)
	}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// The output is read only after Wait, which returns once it is copied;
	// a hung program is killed so that Wait returns.
	limit := time.AfterFunc(15*time.Second, func() { _ = cmd.Process.Kill() })
	err := cmd.Wait()
	if !limit.Stop() {
		return stdout.String(), fmt.Errorf("%s %v: timed out; stderr: %s", name, args, stderr.String())
	}
	if err != nil {
		return stdout.String(), fmt.Errorf("%s %v: %w; stderr: %s", name, args, err, stderr.String())
	}
	return stdout.String(), nil
}

func mustRun(t *testing.T, s *tmuxtest.Server, pane, stdin, name string, args ...string) string {
	t.Helper()
	out, err := run(t, s, pane, stdin, name, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
