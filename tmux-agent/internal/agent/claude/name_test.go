package claude

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNamed(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--name", "proj-a"}, "proj-a"},
		{[]string{"-n", "proj-a", "--resume", "x"}, "proj-a"},
		{[]string{"--name=proj-a"}, "proj-a"},
		{[]string{"--name", "a", "-n", "b"}, "b"},
		{[]string{"--", "--name", "proj-a"}, ""},
	}
	for _, tt := range tests {
		if got := (Claude{}).Named(tt.args); got != tt.want {
			t.Errorf("Named(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestWithName(t *testing.T) {
	tests := []struct {
		args, want []string
	}{
		{nil, []string{"--name", "proj-a"}},
		{[]string{"--resume", "x"}, []string{"--resume", "x", "--name", "proj-a"}},
		{[]string{"-n", "old", "--name=older", "--model", "m"}, []string{"--model", "m", "--name", "proj-a"}},
		{[]string{"fix it", "--", "-n"}, []string{"fix it", "--name", "proj-a", "--", "-n"}},
	}
	for _, tt := range tests {
		if got := (Claude{}).WithName(tt.args, "proj-a"); !slices.Equal(got, tt.want) {
			t.Errorf("WithName(%q) = %q, want %q", tt.args, got, tt.want)
		}
	}
}

// screen is the bottom of Claude Code's screen as capture-pane -e gives
// it, with input as the input box's line after the ❯ and status the line
// below the box.
func screen(input, status string) string {
	rule := "\x1b[38;5;244m" + strings.Repeat("─", 40)
	return strings.Join([]string{
		"",
		"                    \x1b[38;5;246m● high · /effort\x1b[39m",
		rule + " proj-a-claude ─",
		"\x1b[39m❯ " + input,
		rule,
		"\x1b[39m  " + status,
	}, "\n") + "\n"
}

const insert = "\x1b[38;5;246m-- INSERT --\x1b[39m"

func TestInputEmpty(t *testing.T) {
	tests := []struct {
		name, screen string
		want         bool
	}{
		{"empty", screen("", insert), true},
		{"placeholder", screen("\x1b[2mTry \"fix typecheck errors\"\x1b[0m", insert), true},
		{"no vim mode", screen("", "⏵⏵ auto mode on"), true},
		{"draft", screen("hello draft", insert), false},
		{"draft after a faint part", screen("\x1b[2mTry\x1b[0m hello", insert), false},
		{"vim normal", screen("", "\x1b[38;5;246m-- NORMAL --\x1b[39m"), false},
		{"vim visual", screen("", "-- VISUAL LINE --"), false},
		{"two lines of draft", strings.Replace(screen("first", insert), "❯ first", "❯ first\nsecond", 1), false},
		{"no input box", "Do you want to proceed?\n❯ 1. Yes\n  2. No\n", false},
		{"nothing", "", false},
	}
	for _, tt := range tests {
		if got := inputEmpty(tt.screen); got != tt.want {
			t.Errorf("%s: inputEmpty = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Ready needs the process idle, or with no status yet, and the input box
// empty.
func TestReady(t *testing.T) {
	home := t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const session = "11111111-1111-1111-1111-111111111111"
	empty := screen("", insert)
	tests := []struct {
		name, body, screen string
		want               bool
	}{
		{"idle", `{"sessionId":"` + session + `","status":"idle"}`, empty, true},
		{"no status yet", `{"sessionId":"` + session + `","status":null}`, empty, true},
		{"background shell", `{"sessionId":"` + session + `","status":"shell"}`, empty, true},
		{"busy", `{"sessionId":"` + session + `","status":"busy"}`, empty, false},
		{"dialog", `{"sessionId":"` + session + `","status":"waiting"}`, empty, false},
		{"another session", `{"sessionId":"22222222-2222-2222-2222-222222222222","status":"idle"}`, empty, false},
		{"draft", `{"sessionId":"` + session + `","status":"idle"}`, screen("hello", insert), false},
		{"no file", "", empty, false},
	}
	for i, tt := range tests {
		pid := 100 + i
		if tt.body != "" {
			if err := os.WriteFile(Claude{}.StatusFile(pid, getenv), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := (Claude{}).Ready(pid, session, tt.screen, getenv); got != tt.want {
			t.Errorf("%s: Ready = %v, want %v", tt.name, got, tt.want)
		}
	}
}
