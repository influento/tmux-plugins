// Package tmuxtest runs a private tmux server for integration tests.
//
// Each Server lives in a Sandbox: its own HOME, XDG directories and a bin
// directory first on PATH, where stubs stand in for programs a test must not
// reach for real (notify-send, claude). The rest of PATH leaves out the
// user's home, so installed copies in ~/.local/bin are out of reach. Nothing
// reads or writes the user's own tmux server, config or state.
//
// Tests that need a real tmux skip when tmux is not installed. Set
// TMUXTEST_REQUIRE=1 (CI does) to fail instead.
package tmuxtest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
)

// Require skips the test when a program it needs is missing, or fails it when
// TMUXTEST_REQUIRE is set.
func Require(t testing.TB, programs ...string) {
	t.Helper()
	for _, p := range programs {
		if _, err := exec.LookPath(p); err != nil {
			if os.Getenv("TMUXTEST_REQUIRE") != "" {
				t.Fatalf("%s is required (TMUXTEST_REQUIRE is set): %v", p, err)
			}
			t.Skipf("%s not installed", p)
		}
	}
}

// Sandbox is a private home for the programs under test.
type Sandbox struct {
	Dir        string // root of the sandbox
	Home       string
	ConfigHome string
	StateHome  string
	DataHome   string
	CacheHome  string
	RuntimeDir string // short path: sockets live here
	Bin        string // first on PATH; holds the stubs
	callsDir   string
	env        map[string]string
}

// RecordCall is the shell code a stub starts with to record its call for
// Calls. It claims the next numbered folder (mkdir is atomic, so parallel
// calls never share one), writes the arguments NUL-separated and renames
// them into place, so a call Calls sees has all its arguments, then saves
// stdin.
const RecordCall = `d="$TMUXTEST_CALLS/$(basename "$0")"
mkdir -p "$d"
n=0
while ! mkdir "$d/$(printf %06d "$n")" 2>/dev/null; do n=$((n+1)); done
f="$d/$(printf %06d "$n")"
for a in "$@"; do printf '%s\0' "$a"; done > "$f/args.tmp"
mv "$f/args.tmp" "$f/args"
if [ ! -t 0 ]; then cat > "$f/stdin"; fi
`

// StubScript is the default body of a stub: it records its call and exits 0.
const StubScript = "#!/bin/sh\n" + RecordCall + "exit 0\n"

// NewSandbox creates the directories and the default stubs: notify-send,
// claude and swaymsg. The runtime directory is short because a unix socket path must fit
// in 108 bytes, and t.TempDir paths do not.
func NewSandbox(t testing.TB) *Sandbox {
	t.Helper()
	dir := t.TempDir()
	rt, err := os.MkdirTemp(shortTempBase(), "tt")
	if err != nil {
		t.Fatalf("runtime dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	if err := os.Chmod(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Sandbox{
		Dir:        dir,
		Home:       filepath.Join(dir, "home"),
		ConfigHome: filepath.Join(dir, "home", ".config"),
		StateHome:  filepath.Join(dir, "home", ".local", "state"),
		DataHome:   filepath.Join(dir, "home", ".local", "share"),
		CacheHome:  filepath.Join(dir, "home", ".cache"),
		RuntimeDir: rt,
		Bin:        filepath.Join(dir, "bin"),
		callsDir:   filepath.Join(dir, "calls"),
	}
	for _, d := range []string{s.Home, s.ConfigHome, s.StateHome, s.DataHome, s.CacheHome, s.Bin, s.callsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s.env = map[string]string{
		"HOME":            s.Home,
		"XDG_CONFIG_HOME": s.ConfigHome,
		"XDG_STATE_HOME":  s.StateHome,
		"XDG_DATA_HOME":   s.DataHome,
		"XDG_CACHE_HOME":  s.CacheHome,
		"XDG_RUNTIME_DIR": s.RuntimeDir,
		"TMUX_TMPDIR":     s.RuntimeDir,
		"TMPDIR":          filepath.Join(dir, "tmp"),
		"PATH":            s.Bin + string(os.PathListSeparator) + systemPath(),
		"SHELL":           "/bin/sh",
		"TERM":            "xterm-256color",
		"LANG":            "C.UTF-8",
		"LC_ALL":          "C.UTF-8",
		"TMUXTEST_CALLS":  s.callsDir,
	}
	if err := os.MkdirAll(s.env["TMPDIR"], 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notify-send", "claude", "swaymsg"} {
		s.Stub(t, name, StubScript)
	}
	return s
}

// systemPath is the test process's PATH without relative entries and
// without the user's home, where installed copies of the programs under test
// live (~/.local/bin).
func systemPath() string {
	home := os.Getenv("HOME")
	var keep []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		inHome := home != "" && (dir == home || strings.HasPrefix(dir, home+string(filepath.Separator)))
		if !filepath.IsAbs(dir) || inHome {
			continue
		}
		keep = append(keep, dir)
	}
	return strings.Join(keep, string(os.PathListSeparator))
}

// shortTempBase picks a base directory whose paths stay well under the
// socket limit.
func shortTempBase() string {
	if d := os.TempDir(); len(d) <= 40 {
		return d
	}
	return "/tmp"
}

// Stub installs an executable named name into the sandbox's bin directory.
func (s *Sandbox) Stub(t testing.TB, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Bin, name), []byte(script), 0o755); err != nil {
		t.Fatalf("stub %s: %v", name, err)
	}
}

// Link puts an existing program into the sandbox's bin directory under name.
func (s *Sandbox) Link(t testing.TB, name, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(s.Bin, name)); err != nil {
		t.Fatalf("link %s: %v", name, err)
	}
}

// Call is one recorded run of a stub.
type Call struct {
	Args  []string
	Stdin string
}

// Calls returns the recorded runs of the stub name, oldest first. A run
// still writing its arguments is left out.
func (s *Sandbox) Calls(name string) []Call {
	d := filepath.Join(s.callsDir, name)
	entries, _ := os.ReadDir(d) // none yet when the stub never ran
	var calls []Call
	for _, e := range entries {
		args, err := os.ReadFile(filepath.Join(d, e.Name(), "args"))
		if err != nil {
			continue
		}
		stdin, _ := os.ReadFile(filepath.Join(d, e.Name(), "stdin")) // none from a terminal
		var a []string
		if len(args) > 0 {
			a = strings.Split(strings.TrimSuffix(string(args), "\x00"), "\x00")
		}
		calls = append(calls, Call{Args: a, Stdin: string(stdin)})
	}
	return calls
}

// LookPath finds name on the sandbox's PATH. exec.Command searches the test
// process's own PATH, which would run an installed copy instead of the one
// under test, or miss a stub.
func (s *Sandbox) LookPath(name string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	for _, dir := range filepath.SplitList(s.env["PATH"]) {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// Cmd builds a command found on the sandbox's PATH, with the sandbox's
// environment and HOME as its folder.
func (s *Sandbox) Cmd(t testing.TB, name string, args ...string) *exec.Cmd {
	t.Helper()
	path, err := s.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, args...)
	cmd.Args[0] = name
	cmd.Env = s.Environ()
	cmd.Dir = s.Home
	return cmd
}

// Setenv sets a variable in the environment every program in the sandbox
// gets. Set it before Start: the tmux server keeps the environment it
// started with.
func (s *Sandbox) Setenv(key, value string) { s.env[key] = value }

// inherited lists the only variables taken from the test process. Everything
// else is left out, so nothing under test can reach the user's desktop
// (SWAYSOCK, WAYLAND_DISPLAY, DBUS_SESSION_BUS_ADDRESS), tmux server (TMUX)
// or agent session (CLAUDECODE).
var inherited = []string{"USER", "LOGNAME", "TZ"}

// Environ returns the sandbox environment, plus extra KEY=value entries.
func (s *Sandbox) Environ(extra ...string) []string {
	var env []string
	for _, k := range inherited {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range s.env {
		env = append(env, k+"="+v)
	}
	return append(env, extra...)
}

// DefaultConfig is the tmux config a Server starts with unless the test
// passes its own.
const DefaultConfig = `set -g default-shell /bin/sh
set -g history-limit 5000
set -g base-index 0
set -g mode-keys vi
`

// Options configure Start.
type Options struct {
	Config  string // tmux config; DefaultConfig when empty
	Session string // first session's name; "main" when empty
	Command string // first pane's command; the default shell when empty
	Width   int    // first session's size; 120x40 when zero
	Height  int
}

// Server is a private tmux server in its own Sandbox.
type Server struct {
	*Sandbox
	Socket string
	Config string
	Client *tmux.Client
}

// Start starts a server in a new sandbox and kills it when the test ends.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	Require(t, "tmux")
	sb := NewSandbox(t)
	return StartIn(t, sb, opts)
}

// StartIn starts a server in an existing sandbox.
func StartIn(t testing.TB, sb *Sandbox, opts Options) *Server {
	t.Helper()
	Require(t, "tmux")
	if opts.Config == "" {
		opts.Config = DefaultConfig
	}
	if opts.Session == "" {
		opts.Session = "main"
	}
	if opts.Width == 0 {
		opts.Width, opts.Height = 120, 40
	}
	conf := filepath.Join(sb.Dir, "tmux.conf")
	if err := os.WriteFile(conf, []byte(opts.Config), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Sandbox: sb,
		Socket:  filepath.Join(sb.RuntimeDir, "s"),
		Config:  conf,
	}
	s.Client = &tmux.Client{Socket: s.Socket, Config: conf, Env: sb.Environ()}
	args := []string{"new-session", "-d", "-s", opts.Session,
		"-x", fmt.Sprint(opts.Width), "-y", fmt.Sprint(opts.Height)}
	if opts.Command != "" {
		args = append(args, opts.Command)
	}
	if _, err := s.Client.Run(context.Background(), args...); err != nil {
		t.Fatalf("start tmux: %v", err)
	}
	t.Cleanup(func() { s.Client.Run(context.Background(), "kill-server") })
	return s
}

// Tmux runs a tmux command on the server.
func (s *Server) Tmux(args ...string) (string, error) {
	return s.Client.Output(context.Background(), args...)
}

// MustTmux runs a tmux command and fails the test on error.
func (s *Server) MustTmux(t testing.TB, args ...string) string {
	t.Helper()
	out, err := s.Tmux(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Display expands a format for a target.
func (s *Server) Display(t testing.TB, target, format string) string {
	t.Helper()
	return s.MustTmux(t, "display-message", "-p", "-t", target, format)
}

// PaneEnv is the environment of a program that runs inside pane on this
// server: the sandbox's, plus TMUX and TMUX_PANE as tmux sets them.
func (s *Server) PaneEnv(t testing.TB, pane string, extra ...string) []string {
	t.Helper()
	pid := s.MustTmux(t, "display-message", "-p", "#{pid}")
	return s.Environ(append([]string{
		"TMUX=" + s.Socket + "," + pid + ",0",
		"TMUX_PANE=" + pane,
	}, extra...)...)
}

// InPane sets TMUX and TMUX_PANE in the test process itself, so code under
// test that runs plain `tmux` reaches this server and sees pane as current.
func (s *Server) InPane(t *testing.T, pane string) {
	t.Helper()
	pid := s.MustTmux(t, "display-message", "-p", "#{pid}")
	t.Setenv("TMUX", s.Socket+","+pid+",0")
	t.Setenv("TMUX_PANE", pane)
}

// Command builds a command, found on the sandbox's PATH, that runs as if
// started inside pane.
func (s *Server) Command(t testing.TB, pane, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := s.Cmd(t, name, args...)
	cmd.Env = s.PaneEnv(t, pane)
	return cmd
}

// WaitFor polls cond until it returns true or timeout passes.
func WaitFor(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// WaitForContent waits until the pane shows text.
func (s *Server) WaitForContent(t testing.TB, pane, text string) {
	t.Helper()
	WaitFor(t, 5*time.Second, fmt.Sprintf("%q in pane %s", text, pane), func() bool {
		out, err := s.Tmux("capture-pane", "-p", "-t", pane)
		return err == nil && strings.Contains(out, text)
	})
}

// AttachedClient is a program running in a pseudo-terminal, usually a tmux
// client, for tests that need one: command-prompt, popups, client focus, the
// terminal's entry point.
type AttachedClient struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	out   strings.Builder
	done  chan struct{}
	err   error
}

// Attach attaches a client to session through script(1), which supplies the
// PTY: the standard library has none. Width and height size the terminal.
func (s *Server) Attach(t testing.TB, session string, width, height int) *AttachedClient {
	t.Helper()
	before := s.clientNames()
	c := s.Sandbox.Terminal(t, fmt.Sprintf("exec tmux -S %s attach-session -t %s",
		shellQuote(s.Socket), shellQuote("="+session)), width, height)
	WaitFor(t, 5*time.Second, "client to attach", func() bool {
		return len(s.clientNames()) > len(before)
	})
	return c
}

// Terminal runs a shell command in a new pseudo-terminal of the given size,
// with the sandbox environment, as a terminal emulator would. The program
// is killed when the test ends.
func (s *Sandbox) Terminal(t testing.TB, command string, width, height int) *AttachedClient {
	t.Helper()
	Require(t, "script")
	inner := fmt.Sprintf("stty rows %d cols %d; %s", height, width, command)
	cmd := exec.Command("script", "-qfec", inner, "/dev/null")
	cmd.Env = s.Environ()
	cmd.Dir = s.Home
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	c := &AttachedClient{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	go func() {
		r := bufio.NewReader(stdout)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			c.mu.Lock()
			c.out.Write(buf[:n])
			c.mu.Unlock()
			if err != nil {
				break
			}
		}
		c.err = cmd.Wait()
		close(c.done)
	}()
	t.Cleanup(c.Close)
	return c
}

// Exited reports whether the program has ended, and its error if so.
func (c *AttachedClient) Exited() (bool, error) {
	select {
	case <-c.done:
		return true, c.err
	default:
		return false, nil
	}
}

func (s *Server) clientNames() []string {
	out, err := s.Tmux("list-clients", "-F", "#{client_name}")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// Type sends raw bytes to the client's terminal, as keys typed by a user.
// The prefix key is whatever the config sets; C-b is "\x02".
func (c *AttachedClient) Type(t testing.TB, keys string) {
	t.Helper()
	if _, err := io.WriteString(c.stdin, keys); err != nil {
		t.Fatalf("type %q: %v", keys, err)
	}
}

// Focus reports focus-in from the client's terminal, as a terminal does when
// its window gains focus. The server needs focus-events on. A new client
// counts as focused until its terminal reports focus-out; tmux 3.8 shows it
// in #{client_flags} ("focused"), and has no client_focused format.
func (c *AttachedClient) Focus(t testing.TB) { c.Type(t, "\x1b[I") }

// Blur reports focus-out.
func (c *AttachedClient) Blur(t testing.TB) { c.Type(t, "\x1b[O") }

// Output returns everything the client's terminal has printed so far.
func (c *AttachedClient) Output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.String()
}

// Close detaches the client.
func (c *AttachedClient) Close() {
	c.stdin.Close()
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
