package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/shell"
	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/cli"
	"github.com/influento/tmux-plugins/tmux-agent/internal/selftest"
)

func TestMain(m *testing.M) { selftest.Main(m) }

// gatedFzf puts an fzf into the sandbox that stays open until the test
// creates the file gate, as a user who takes their time. Then it picks the
// line whose target is choice, or leaves with Esc (exit 130) when choice is
// "". What it was given is in the file fzfInput(sb).
func gatedFzf(t *testing.T, sb *tmuxtest.Sandbox, choice string) (gate string) {
	t.Helper()
	gate = filepath.Join(sb.Dir, "fzf-gate")
	answer := "exit 130"
	if choice != "" {
		answer = "printf '\\t%s\\t\\n' " + shell.Quote(choice)
	}
	sb.Stub(t, "fzf", "#!/bin/sh\ncat >"+shell.Quote(fzfInput(sb))+"\nwhile [ ! -e "+shell.Quote(gate)+" ]; do sleep 0.05; done\n"+answer+"\n")
	return gate
}

func fzfInput(sb *tmuxtest.Sandbox) string { return filepath.Join(sb.Dir, "fzf-input") }

func open(t *testing.T, gate string) {
	t.Helper()
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A choice made after the command's deadline still switches the client:
// the deadline bounds tmux, not the user.
func TestPickSwitchesAfterASlowChoice(t *testing.T) {
	tmuxtest.Require(t, "tmux")
	sb := tmuxtest.NewSandbox(t)
	gate := gatedFzf(t, sb, "=proj-b:")
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	s.MustTmux(t, "new-session", "-d", "-s", "proj-b")
	s.Attach(t, "proj-a", 100, 30)
	s.InPane(t, s.Display(t, "proj-a:0.0", "#{pane_id}"))
	t.Setenv("PATH", sb.Bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		<-ctx.Done()
		open(t, gate)
	}()
	if err := cli.Pick(ctx); err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got := s.MustTmux(t, "list-clients", "-F", "#{client_session}"); got != "proj-b" {
		t.Errorf("client session = %q, want proj-b", got)
	}
}

// Picking a window line switches the client to that window, and marks a
// done flag there as seen. The picker lists each session's flagged and
// agent windows, and folds the others into one row that opens the session.
func TestPickSwitchesToAWindow(t *testing.T) {
	tmuxtest.Require(t, "tmux")
	sb := tmuxtest.NewSandbox(t)
	gate := gatedFzf(t, sb, "=proj-b:1")
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	s.MustTmux(t, "new-session", "-d", "-s", "proj-b", ";", "new-window", "-d", "-t", "=proj-b:1", "-n", "w-done")
	pane := s.Display(t, "=proj-b:1", "#{pane_id}")
	s.MustTmux(t, "set-option", "-p", "-t", pane, "@attention", "done", ";", "set-option", "-w", "-t", pane, "@attention_win", "done")
	s.Attach(t, "proj-a", 100, 30)
	s.InPane(t, s.Display(t, "proj-a:0.0", "#{pane_id}"))
	t.Setenv("PATH", sb.Bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	open(t, gate)
	if err := cli.Pick(context.Background()); err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got := s.MustTmux(t, "list-clients", "-F", "#{client_session}:#{window_index}"); got != "proj-b:1" {
		t.Errorf("client on %q, want proj-b:1", got)
	}
	if got := s.Display(t, pane, "#{@attention}"); got != "" {
		t.Errorf("flag = %q after the pick, want it seen", got)
	}
	in, err := os.ReadFile(fzfInput(sb))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proj-b\t=proj-b:\t", "\t=proj-b:1\t", "\t=proj-a:\t"} {
		if !strings.Contains(string(in), want) {
			t.Errorf("picker lines lack %q:\n%s", want, in)
		}
	}
}

// Picking a pane line of a split window goes to that pane.
func TestPickSwitchesToAPane(t *testing.T) {
	tmuxtest.Require(t, "tmux")
	sb := tmuxtest.NewSandbox(t)
	gate := gatedFzf(t, sb, "=proj-b:0.1")
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	s.MustTmux(t, "new-session", "-d", "-s", "proj-b", ";", "split-window", "-d", "-t", "=proj-b:0")
	s.Attach(t, "proj-a", 100, 30)
	s.InPane(t, s.Display(t, "proj-a:0.0", "#{pane_id}"))
	t.Setenv("PATH", sb.Bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	open(t, gate)
	if err := cli.Pick(context.Background()); err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got := s.MustTmux(t, "list-clients", "-F", "#{client_session}:#{window_index}.#{pane_index}"); got != "proj-b:0.1" {
		t.Errorf("client on %q, want proj-b:0.1", got)
	}
}

// The panel shows each row as the cursor moves to it; Esc puts the client
// back where it was when the panel opened.
func TestPickEscGoesBack(t *testing.T) {
	tmuxtest.Require(t, "tmux")
	sb := tmuxtest.NewSandbox(t)
	// An fzf whose cursor moved to proj-b before the user pressed Esc.
	sb.Stub(t, "fzf", "#!/bin/sh\ncat >/dev/null\ntmux switch-client -t =proj-b:\nexit 130\n")
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	s.MustTmux(t, "new-session", "-d", "-s", "proj-b", ";", "split-window", "-d", "-t", "=proj-a:0")
	s.MustTmux(t, "select-pane", "-t", "=proj-a:0.1")
	s.Attach(t, "proj-a", 100, 30)
	s.InPane(t, s.Display(t, "proj-a:0.0", "#{pane_id}"))
	t.Setenv("PATH", sb.Bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := cli.Pick(context.Background()); err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got := s.MustTmux(t, "list-clients", "-F", "#{client_session}:#{window_index}.#{pane_index}"); got != "proj-a:0.1" {
		t.Errorf("client on %q after Esc, want proj-a:0.1, where it was", got)
	}
}

// In the panel's real fzf, the arrows step over the session lines, and
// the client follows the cursor: a fold row shows its session. Enter on a
// fold opens it; Tab switches to the agents by state and back. A search
// for a session's name lists all its windows; Enter stays on the chosen
// one.
func TestPanelSkipsSessionLines(t *testing.T) {
	tmuxtest.Require(t, "tmux", "fzf")
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	s.MustTmux(t, "new-session", "-d", "-s", "proj-b", "-n", "w-one", ";", "new-window", "-d", "-t", "=proj-b:1", "-n", "w-two")
	s.Attach(t, "proj-a", 100, 30)
	client := s.MustTmux(t, "list-clients", "-F", "#{client_name}")
	where := func() string {
		return s.MustTmux(t, "list-clients", "-F", "#{client_session}:#{window_index}")
	}
	wantAt := func(step, want string) {
		t.Helper()
		tmuxtest.WaitFor(t, 5*time.Second, step+": client on "+want, func() bool { return where() == want })
	}
	// The panel runs in a session @skip_sessions leaves out of its rows,
	// as in its own terminal. Rows: proj-a, its fold, proj-b, its fold.
	s.MustTmux(t, "new-session", "-d", "-s", "ov-panel", "-x", "40", "-y", "20", "tmux-agent pick "+shell.Quote(client))
	s.WaitForContent(t, "=ov-panel:", "proj-b")
	keys := func(k ...string) { s.MustTmux(t, append([]string{"send-keys", "-t", "=ov-panel:"}, k...)...) }
	shows := func(step, text string) {
		t.Helper()
		tmuxtest.WaitFor(t, 5*time.Second, step+": the panel shows "+text, func() bool {
			out, err := s.Tmux("capture-pane", "-p", "-t", "=ov-panel:")
			return err == nil && strings.Contains(out, text)
		})
	}

	keys("Down")
	wantAt("down over proj-b's line, onto its fold", "proj-b:0")
	keys("Enter")                             // opens the fold: proj-b's windows, the cursor on the first
	shows("enter on the fold", "      w-two") // a row of its own, under the marks' column
	keys("Down")
	wantAt("down", "proj-b:1")
	keys("Up", "Up", "Up")
	wantAt("up over proj-b's line, onto proj-a's fold", "proj-a:0")
	keys("Tab")
	shows("tab", "no agents")
	keys("BTab")
	shows("tab back", "[tree]")
	keys("-l", "PROJ-B")
	wantAt("search for a session", "proj-b:0")
	keys("Down", "Enter")
	tmuxtest.WaitFor(t, 5*time.Second, "the panel to close", func() bool {
		_, err := s.Tmux("has-session", "-t", "=ov-panel")
		return err != nil
	})
	if got := where(); got != "proj-b:1" {
		t.Errorf("client on %q after Enter, want proj-b:1", got)
	}
}

// @sidebar_cursor_style colours the panel's cursor row: fzf gets its bg
// and fg after FZF_DEFAULT_OPTS, so they win there.
func TestPickCursorStyle(t *testing.T) {
	tmuxtest.Require(t, "tmux")
	sb := tmuxtest.NewSandbox(t)
	args := filepath.Join(sb.Dir, "fzf-args")
	sb.Stub(t, "fzf", "#!/bin/sh\nprintf '%s\\n' \"$@\" >"+shell.Quote(args)+"\ncat >/dev/null\nexit 130\n")
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	s.MustTmux(t, "set-option", "-g", "@sidebar_cursor_style", "bg=#45475a,fg=#b4befe")
	s.Attach(t, "proj-a", 100, 30)
	s.InPane(t, s.Display(t, "proj-a:0.0", "#{pane_id}"))
	t.Setenv("PATH", sb.Bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := cli.Pick(context.Background()); err != nil {
		t.Fatalf("pick: %v", err)
	}
	got, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "\n--color=bg+:#45475a,fg+:#b4befe\n") {
		t.Errorf("fzf arguments lack the cursor row's colours:\n%s", got)
	}
}
