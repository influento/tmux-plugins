package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

func payload(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReadHook(t *testing.T) {
	const sid = "11111111-2222-4333-8444-555555555555"
	cases := []struct {
		name string
		in   map[string]any
		ok   bool
		want agent.Event
	}{
		{"start", map[string]any{"hook_event_name": "SessionStart", "session_id": sid, "transcript_path": "/t", "cwd": "/src/proj-a", "source": "startup"},
			true, agent.Event{Kind: agent.SessionStart, SessionID: sid, Transcript: "/t", Cwd: "/src/proj-a"}},
		{"resume", map[string]any{"hook_event_name": "SessionStart", "session_id": sid, "source": "resume"},
			true, agent.Event{Kind: agent.SessionStart, SessionID: sid, Resume: true}},
		{"compact", map[string]any{"hook_event_name": "SessionStart", "session_id": sid, "source": "compact"}, false, agent.Event{}},
		{"end other", map[string]any{"hook_event_name": "SessionEnd", "reason": "other"},
			true, agent.Event{Kind: agent.SessionEnd, End: agent.EndUnknown}},
		{"end exit", map[string]any{"hook_event_name": "SessionEnd", "reason": "prompt_input_exit"},
			true, agent.Event{Kind: agent.SessionEnd, End: agent.EndExited}},
		{"prompt", map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "go"}, true, agent.Event{Kind: agent.Working}},
		{"notification dropped", map[string]any{"hook_event_name": "Notification", "notification_type": "idle_prompt"}, false, agent.Event{}},
		{"notification long", map[string]any{"hook_event_name": "Notification", "notification_type": "agent_needs_input", "message": strings.Repeat("y", 80)},
			true, agent.Event{Kind: agent.NeedsInput, Reason: strings.Repeat("y", 64)}},
		{"notification empty", map[string]any{"hook_event_name": "Notification", "notification_type": "elicitation_dialog"},
			true, agent.Event{Kind: agent.NeedsInput, Reason: "needs input"}},
		{"stop", map[string]any{"hook_event_name": "Stop", "transcript_path": "/t"}, true, agent.Event{Kind: agent.TurnEnd, Transcript: "/t"}},
		{"unknown", map[string]any{"hook_event_name": "SubagentStop"}, false, agent.Event{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, ok, err := Claude{}.ReadHook(payload(t, c.in))
			if err != nil || ok != c.ok || ev != c.want {
				t.Errorf("ReadHook = %+v, %v, %v; want %+v, %v", ev, ok, err, c.want, c.ok)
			}
		})
	}
}

func TestReadHookTools(t *testing.T) {
	ask := payload(t, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": []any{map[string]any{"question": "Which store?"}}}})
	ev, _, _ := Claude{}.ReadHook(ask)
	if ev.Kind != agent.NeedsInput || ev.Reason != "question: Which store?" || ev.Tool == "" {
		t.Errorf("question = %+v", ev)
	}
	bare, _, _ := Claude{}.ReadHook(payload(t, map[string]any{"hook_event_name": "PreToolUse", "tool_name": "AskUserQuestion"}))
	if bare.Reason != "question: Claude asks" {
		t.Errorf("question without input = %q", bare.Reason)
	}

	// A permission request and the end of the same call share a key; the
	// order of the input's keys does not matter.
	perm, _, _ := Claude{}.ReadHook([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"make\ntest","timeout":1.50}}`))
	post, _, _ := Claude{}.ReadHook([]byte(`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"timeout":1.50,"command":"make\ntest"},"tool_use_id":"x"}`))
	other, _, _ := Claude{}.ReadHook([]byte(`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`))
	if perm.Reason != "Bash(make test)" || perm.Kind != agent.NeedsInput {
		t.Errorf("permission = %+v", perm)
	}
	if !post.ToolEnd || post.Kind != agent.Working || post.Tool != perm.Tool {
		t.Errorf("post %+v does not match permission key %q", post, perm.Tool)
	}
	if other.Tool == perm.Tool {
		t.Error("another call has the same key")
	}
}

func TestLabel(t *testing.T) {
	long := strings.Repeat("x", 60)
	in := func(k, v string) fields { return fields{k: json.RawMessage(`"` + v + `"`)} }
	cases := []struct {
		tool  string
		input fields
		want  string
	}{
		{"Bash", in("command", "make test"), "Bash(make test)"},
		{"Bash", in("command", long), "Bash(" + long[:48] + ")"},
		{"Bash", in("command", strings.Repeat("é", 50)), "Bash(" + strings.Repeat("é", 48) + ")"},
		{"Edit", in("file_path", "/src/proj-a/main.go"), "Edit(main.go)"},
		{"Write", in("file_path", "notes.md"), "Write(notes.md)"},
		{"Grep", in("pattern", long), "Grep(" + long[:32] + ")"},
		{"WebFetch", in("url", "https://example.com"), "WebFetch"},
		{"Bash", fields{"command": json.RawMessage(`42`)}, "Bash(42)"},
		{"Bash", fields{"command": json.RawMessage(`{"a":1}`)}, "Bash()"},
	}
	for _, c := range cases {
		if got := label(c.tool, c.input); got != c.want {
			t.Errorf("label(%s, %v) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
}

func TestArgs(t *testing.T) {
	c := Claude{}
	for _, args := range [][]string{{"-p", "hi"}, {"--print"}, {"--model", "x", "-p"}, {"--print=true"}} {
		if !c.OneShot(args) {
			t.Errorf("OneShot(%q) = false", args)
		}
	}
	for _, args := range [][]string{nil, {"--resume", "x"}, {"--", "-p"}} {
		if c.OneShot(args) {
			t.Errorf("OneShot(%q) = true", args)
		}
	}
	resumes := map[string][]string{
		"abc": {"--resume", "abc"},
		"def": {"--model", "m", "--resume=def"},
		"ghi": {"-r", "ghi"},
		"":    {"--resume", "--model"},
	}
	for want, args := range resumes {
		if got := c.ResumedSession(args); got != want {
			t.Errorf("ResumedSession(%q) = %q, want %q", args, got, want)
		}
	}
	if got := c.ResumedSession(c.ResumeArgs("abc")); got != "abc" {
		t.Errorf("ResumeArgs does not round-trip: %q", got)
	}
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTitle(t *testing.T) {
	user := func(text string) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
		return string(b)
	}
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"opener", []string{user("can you fix the waybar clock please")}, "fix the waybar"},
		{"cut at 15", []string{user("Hello, please refactor the parser module")}, "refactor the pa"},
		{"trailing punctuation", []string{user("fix: the thing! now")}, "fix: the thing"},
		{"several openers", []string{user("ok so let's add tests for jump")}, "add tests for"},
		{"hashes dropped", []string{user("## fix  the   build")}, "fix the build"},
		{"unicode cut", []string{user("überprüfe die Übersetzungsdatei")}, "überprüfe die Ü"},
		{"skips non-prompts", []string{
			user("/clear"),
			user("<command-name>/model</command-name>"),
			`{"type":"user","message":{"content":[{"type":"tool_result","content":"done"}]}}`,
			`not json`,
			`{"type":"assistant","message":{"content":"hi"}}`,
			`{"type":"user","message":{"content":[{"type":"text","text":"update the"},{"type":"text","text":"readme now"}]}}`,
		}, "update the read"},
		{"no prompt", []string{user("/clear")}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (Claude{}).Title(writeTranscript(t, c.lines...)); got != c.want {
				t.Errorf("Title = %q, want %q", got, c.want)
			}
		})
	}
	if got := (Claude{}).Title(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("Title of a missing transcript = %q", got)
	}
}
