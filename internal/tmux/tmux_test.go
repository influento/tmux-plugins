package tmux_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

func TestOption(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{})
	ctx := context.Background()
	c := srv.Client

	if _, ok, err := c.Option(ctx, "-g", "", "@unset_option"); err != nil || ok {
		t.Fatalf("unset option: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	srv.MustTmux(t, "set-option", "-g", "@x", "a b; $c")
	v, ok, err := c.Option(ctx, "-g", "", "@x")
	if err != nil || !ok || v != "a b; $c" {
		t.Fatalf("Option = %q, %v, %v; want %q, true, nil", v, ok, err, "a b; $c")
	}
	pane := srv.Display(t, "main", "#{pane_id}")
	srv.MustTmux(t, "set-option", "-p", "-t", pane, "@p", "pv")
	if v, ok, _ := c.Option(ctx, "-p", pane, "@p"); !ok || v != "pv" {
		t.Fatalf("pane option = %q, %v", v, ok)
	}
}

func TestDisplay(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{Session: "s1"})
	got, err := srv.Client.Display(context.Background(), "s1", "#{session_name}:#{window_index}")
	if err != nil || got != "s1:0" {
		t.Fatalf("Display = %q, %v", got, err)
	}
}

func TestErrorCarriesStderr(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{})
	_, err := srv.Client.Run(context.Background(), "select-window", "-t", "no-such-session:9")
	var te *tmux.Error
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *tmux.Error", err)
	}
	if !strings.Contains(err.Error(), "no-such-session") {
		t.Errorf("error %q does not name the target", err)
	}
}

func TestNoServerIsAnError(t *testing.T) {
	tmuxtest.Require(t, "tmux")
	sb := tmuxtest.NewSandbox(t)
	c := &tmux.Client{Socket: sb.RuntimeDir + "/none", Env: sb.Environ()}
	if _, _, err := c.Option(context.Background(), "-g", "", "@x"); err == nil {
		t.Fatal("Option on a missing server: want an error, not an unset option")
	}
}

// A hung server is an error, not an unset option. The fake tmux never
// answers.
func TestOptionTimeoutIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := &tmux.Client{Timeout: 200 * time.Millisecond}
	_, ok, err := c.Option(context.Background(), "-g", "", "@x")
	if ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Option on a hung server: ok=%v err=%v; want ok=false and a deadline error", ok, err)
	}
}

func TestOptionWithoutTmuxIsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, ok, err := (&tmux.Client{}).Option(context.Background(), "-g", "", "@x"); ok || err == nil {
		t.Fatalf("Option without tmux: ok=%v err=%v; want an error", ok, err)
	}
}

// Built-in options always have a value, so an unknown one is a mistake.
func TestOptionUnknownBuiltinIsAnError(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{})
	if _, ok, err := srv.Client.Option(context.Background(), "-g", "", "no-such-option"); ok || err == nil {
		t.Fatalf("Option for an unknown built-in: ok=%v err=%v; want an error", ok, err)
	}
}

func TestTimeout(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{})
	c := *srv.Client
	c.Timeout = 200 * time.Millisecond
	start := time.Now()
	_, err := c.Run(context.Background(), "run-shell", "sleep 5")
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Run returned after %v, want about 200ms", d)
	}
}

func TestSandboxStubsRecordCalls(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{})
	pane := srv.Display(t, "main", "#{pane_id}")
	cmd := srv.Command(t, pane, "sh", "-c", `printf 'in' | notify-send -a tmux "a title" body`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	calls := srv.Calls("notify-send")
	if len(calls) != 1 || strings.Join(calls[0].Args, "|") != "-a|tmux|a title|body" || calls[0].Stdin != "in" {
		t.Fatalf("calls = %+v", calls)
	}
}

// Hooks run in parallel, so stubs do too: no call may be lost.
func TestSandboxStubsRecordConcurrentCalls(t *testing.T) {
	sb := tmuxtest.NewSandbox(t)
	const n = 50
	cmds := make([]*exec.Cmd, n)
	for i := range cmds {
		cmds[i] = sb.Cmd(t, "notify-send", strconv.Itoa(i))
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, c := range sb.Calls("notify-send") {
		seen[strings.Join(c.Args, " ")] = true
	}
	if len(seen) != n {
		t.Fatalf("%d distinct calls recorded, want %d", len(seen), n)
	}
}

// Arguments come back exactly: a newline or an empty argument stays one
// argument, and no arguments are none.
func TestSandboxStubsKeepArguments(t *testing.T) {
	sb := tmuxtest.NewSandbox(t)
	want := []string{"two\nlines", "", "body"}
	for _, args := range [][]string{want, nil} {
		if out, err := sb.Cmd(t, "notify-send", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	calls := sb.Calls("notify-send")
	if len(calls) != 2 || !slices.Equal(calls[0].Args, want) || len(calls[1].Args) != 0 {
		t.Fatalf("calls = %q, want %q then no arguments", calls, want)
	}
}

func TestSandboxHidesDesktop(t *testing.T) {
	t.Setenv("SWAYSOCK", "/run/user/0/sway.sock")
	sb := tmuxtest.NewSandbox(t)
	for _, kv := range sb.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "SWAYSOCK", "TMUX", "TMUX_PANE", "DBUS_SESSION_BUS_ADDRESS", "WAYLAND_DISPLAY":
			t.Errorf("%s leaks into the sandbox", k)
		}
		if k == "HOME" && strings.TrimPrefix(kv, "HOME=") == os.Getenv("HOME") {
			t.Error("HOME is the real home")
		}
	}
}

func TestAttachedClient(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{Command: "cat"})
	c := srv.Attach(t, "main", 100, 30)
	size := srv.MustTmux(t, "list-clients", "-F", "#{client_width}x#{client_height}")
	if size != "100x30" {
		t.Errorf("client size = %q, want 100x30", size)
	}
	c.Type(t, "hello\r")
	srv.WaitForContent(t, "main", "hello")
}

func TestAttachedClientFocus(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{
		Config:  tmuxtest.DefaultConfig + "set -g focus-events on\n",
		Command: "cat",
	})
	c := srv.Attach(t, "main", 80, 24)
	// tmux 3.8 has no client_focused format; the flag is in client_flags.
	focused := func() bool {
		flags := srv.MustTmux(t, "list-clients", "-F", "#{client_flags}")
		return slices.Contains(strings.Split(flags, ","), "focused")
	}
	if !focused() {
		t.Fatal("a new client is not focused")
	}
	c.Blur(t)
	tmuxtest.WaitFor(t, 2*time.Second, "blur", func() bool { return !focused() })
	c.Focus(t)
	tmuxtest.WaitFor(t, 2*time.Second, "focus", focused)
}

// Programs installed in the user's home (~/.local/bin) are out of the
// sandbox's reach: there they would be the user's copies, not the ones under
// test.
func TestSandboxPathLeavesOutHome(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tmuxtest-home-only"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	sb := tmuxtest.NewSandbox(t)
	if p, err := sb.LookPath("tmuxtest-home-only"); err == nil {
		t.Fatalf("the sandbox found %s", p)
	}
	if _, err := sb.LookPath("sh"); err != nil {
		t.Fatalf("the sandbox lost the system programs: %v", err)
	}
}

// Commands resolve on the sandbox's PATH: a stub wins over an installed
// program, and a program only the sandbox has is found.
func TestCommandUsesSandboxPath(t *testing.T) {
	srv := tmuxtest.Start(t, tmuxtest.Options{})
	srv.Stub(t, "tmuxtest-only-here", "#!/bin/sh\necho sandbox\n")
	pane := srv.Display(t, "main", "#{pane_id}")
	out, err := srv.Command(t, pane, "tmuxtest-only-here").Output()
	if err != nil || strings.TrimSpace(string(out)) != "sandbox" {
		t.Fatalf("output %q, error %v", out, err)
	}
	if _, err := exec.LookPath("tmuxtest-only-here"); err == nil {
		t.Fatal("the test process found the stub on its own PATH")
	}
}
