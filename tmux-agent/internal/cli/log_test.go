package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
