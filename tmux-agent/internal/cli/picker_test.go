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

// The popup stays until the user closes it, however long that takes, and
// tmux-agent popup then exits 0 without a message: the binding runs it
// with run-shell -b, which shows any error.
func TestPopupWaitsForTheUser(t *testing.T) {
	tmuxtest.Require(t, "tmux")
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	gate := gatedFzf(t, sb, "")
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	s.Attach(t, "proj-a", 100, 30)
	client := s.MustTmux(t, "list-clients", "-F", "#{client_name}")
	pane := s.Display(t, "proj-a:0.0", "#{pane_id}")

	cmd := s.Command(t, pane, "tmux-agent", "popup", client)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Longer than the timeout of any one tmux call.
	select {
	case err := <-done:
		t.Fatalf("tmux-agent popup ended with the picker open: %v; stderr: %s", err, stderr.String())
	case <-time.After(3 * time.Second):
	}
	open(t, gate)
	select {
	case err := <-done:
		if err != nil || stderr.Len() > 0 {
			t.Fatalf("tmux-agent popup = %v, stderr %q; want exit 0 and no message", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tmux-agent popup still running after the picker closed")
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
// done flag there as seen; the picker lists each session's windows.
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
	for _, want := range []string{"proj-b\t=proj-b:\t", "\t=proj-b:1\t", "\t=proj-a:0\t"} {
		if !strings.Contains(string(in), want) {
			t.Errorf("picker lines lack %q:\n%s", want, in)
		}
	}
}
