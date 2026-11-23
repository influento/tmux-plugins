package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/selftest"
)

// The hook logs each event it gets, a dropped one too, only with
// TMUX_AGENT_DEBUG set; its errors always.
func TestHookLog(t *testing.T) {
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	pane := s.Display(t, "=proj-a:0", "#{pane_id}")
	path := filepath.Join(sb.StateHome, "tmux-agent", "log")
	log := func() string {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}
	hook := func(payload string, env ...string) {
		t.Helper()
		cmd := s.Command(t, pane, "tmux-agent", "hook", "claude")
		cmd.Env = append(cmd.Env, env...)
		cmd.Stdin = strings.NewReader(payload)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook: %v: %s", err, out)
		}
	}
	const prompt = `{"hook_event_name":"UserPromptSubmit","session_id":"11111111-2222-4333-8444-555555555555"}`

	hook(prompt)
	if got := log(); got != "" {
		t.Fatalf("log without TMUX_AGENT_DEBUG = %q, want none", got)
	}
	hook(prompt, "TMUX_AGENT_DEBUG=1")
	hook(`{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, "TMUX_AGENT_DEBUG=1")
	got := log()
	for _, want := range []string{
		"hook " + pane + " claude UserPromptSubmit: working session 11111111-2222-4333-8444-555555555555\n",
		"hook " + pane + " claude Notification:idle_prompt: dropped\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log = %q, want a line ending %q", got, want)
		}
	}

	// A tmux it cannot reach is an error: logged with no TMUX_AGENT_DEBUG.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	hook(prompt, "TMUX="+filepath.Join(sb.RuntimeDir, "none")+",1,0")
	if got := log(); !strings.Contains(got, "hook claude: ") || strings.Contains(got, "UserPromptSubmit") {
		t.Errorf("log of a failed hook = %q, want its error alone", got)
	}
}

// A hook's job that fails shows nothing in tmux, which prints a background
// job's output or failure into its client's current pane (and tmux 3.8
// crashes when that client's session has no window): its error goes to the
// log alone.
func TestTmuxHookFailureShowsNothing(t *testing.T) {
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	s := tmuxtest.StartIn(t, sb, tmuxtest.Options{Session: "proj-a"})
	pane := s.Display(t, "=proj-a:0", "#{pane_id}")
	if out, err := s.Command(t, pane, "tmux-agent", "tmux-init").CombinedOutput(); err != nil {
		t.Fatalf("tmux-init: %v: %s", err, out)
	}
	// No lock can be taken: sidebar follow fails.
	locks := filepath.Join(sb.RuntimeDir, "tmux-agent")
	if err := os.RemoveAll(locks); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(locks, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sb.StateHome, "tmux-agent", "log")
	s.MustTmux(t, "set-option", "-g", "@sidebar_open", "1")
	s.MustTmux(t, "new-window", "-t", "=proj-a:") // session-window-changed runs sidebar follow
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(path) // none yet until the hook logs
		for l := range strings.SplitSeq(s.MustTmux(t, "list-panes", "-a", "-F", "#{pane_in_mode} #{pane_id}"), "\n") {
			if p, ok := strings.CutPrefix(l, "1 "); ok {
				t.Fatalf("pane %s shows the hook's output: %q", p, s.MustTmux(t, "capture-pane", "-p", "-M", "-t", p))
			}
		}
		if strings.Contains(string(data), "sidebar follow") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 10s: no error of sidebar follow in the log %q, and no pane shows anything", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
