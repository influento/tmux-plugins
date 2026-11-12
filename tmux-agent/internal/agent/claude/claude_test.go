package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
		{"stop in a worktree", map[string]any{"hook_event_name": "Stop", "cwd": "/src/proj-a/.claude/worktrees/wt-a"},
			true, agent.Event{Kind: agent.TurnEnd, Cwd: "/src/proj-a/.claude/worktrees/wt-a"}},
		{"stop with subagents", map[string]any{"hook_event_name": "Stop", "background_tasks": []any{
			map[string]any{"id": "a1", "type": "subagent", "status": "running"},
			map[string]any{"id": "b2", "type": "shell", "status": "running"},
			map[string]any{"id": "a3", "type": "subagent", "status": "completed"},
			map[string]any{"id": "a4", "type": "subagent", "status": "running"},
		}}, true, agent.Event{Kind: agent.TurnEnd, Subagents: []string{"a1", "a4"}}},
		{"stop with odd tasks", map[string]any{"hook_event_name": "Stop", "background_tasks": "none"},
			true, agent.Event{Kind: agent.TurnEnd}},
		{"subagent tool", map[string]any{"hook_event_name": "PostToolUse", "agent_id": "a1", "tool_name": "Bash"},
			true, agent.Event{Kind: agent.Working, ToolEnd: true, By: "a1", Tool: toolKey(fields{"tool_name": json.RawMessage(`"Bash"`)})}},
		{"unknown", map[string]any{"hook_event_name": "SubagentStop"}, false, agent.Event{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, ok, err := Claude{}.ReadHook(payload(t, c.in))
			if err != nil || ok != c.ok || !reflect.DeepEqual(ev, c.want) {
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
	ai := func(title string) string {
		b, _ := json.Marshal(map[string]any{"type": "ai-title", "aiTitle": title, "sessionId": "s"})
		return string(b)
	}
	cases := []struct {
		name  string
		lines []string
		want  string
		sure  bool
	}{
		{"opener", []string{user("can you fix the waybar clock please")}, "fix the waybar", false},
		{"cut at 15", []string{user("Hello, please refactor the parser module")}, "refactor the pa", false},
		{"trailing punctuation", []string{user("fix: the thing! now")}, "fix: the thing", false},
		{"several openers", []string{user("ok so let's add tests for jump")}, "add tests for", false},
		{"hashes dropped", []string{user("## fix  the   build")}, "fix the build", false},
		{"unicode cut", []string{user("überprüfe die Übersetzungsdatei")}, "überprüfe die Ü", false},
		{"skips non-prompts", []string{
			user("/clear"),
			user("<command-name>/model</command-name>"),
			`{"type":"user","message":{"content":[{"type":"tool_result","content":"done"}]}}`,
			`not json`,
			`{"type":"assistant","message":{"content":"hi"}}`,
			`{"type":"user","message":{"content":[{"type":"text","text":"update the"},{"type":"text","text":"readme now"}]}}`,
		}, "update the read", false},
		{"no prompt", []string{user("/clear")}, "", false},
		{"ai-title wins", []string{user("Write a 300-word story about a lighthouse"), ai("Lighthouse short story")}, "Lighthouse short story", true},
		{"ai-title cut at a word", []string{user("go"), ai("Poteto-vs-workshop card review S9")}, "Poteto-vs-workshop card", true},
		{"ai-title one long word", []string{ai("Supercalifragilisticexpialidocious")}, "Supercalifragilisticexpi", true},
		{"first ai-title", []string{ai("first title"), ai("second title")}, "first title", true},
		{"empty ai-title", []string{user("fix the build"), ai("  ")}, "fix the build", false},
		{"text naming ai-title", []string{user(`what is "ai-title" for`)}, `what is "ai-tit`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, sure := (Claude{}).Title(writeTranscript(t, c.lines...)); got != c.want || sure != c.sure {
				t.Errorf("Title = %q, %v; want %q, %v", got, sure, c.want, c.sure)
			}
		})
	}
	if got, _ := (Claude{}).Title(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("Title of a missing transcript = %q", got)
	}
}

func TestInterrupted(t *testing.T) {
	at := time.Date(2026, 11, 6, 20, 12, 5, 0, time.UTC)
	line := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return string(b)
	}
	msg := func(typ string, content any, extra map[string]any) string {
		m := map[string]any{"type": typ, "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"content": content}}
		for k, v := range extra {
			m[k] = v
		}
		return line(m)
	}
	text := func(s string) []any { return []any{map[string]any{"type": "text", "text": s}} }
	toolUse := msg("assistant", []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Bash"}}, nil)
	rejected := msg("user", []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "The user doesn't want to proceed"}}, nil)
	turnEnd := line(map[string]any{"type": "system", "subtype": "turn_duration"})
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"declined permission", []string{toolUse, rejected, msg("user", text("[Request interrupted by user for tool use]"), nil), turnEnd}, true},
		{"esc", []string{msg("assistant", text("Once upon"), nil), msg("user", text("[Request interrupted by user]"), nil)}, true},
		{"string content", []string{msg("user", "[Request interrupted by user]", nil)}, true},
		{"turn ended normally", []string{msg("user", "write a story", nil), msg("assistant", text("Done."), nil), turnEnd}, false},
		{"new prompt after the interrupt", []string{msg("user", text("[Request interrupted by user]"), nil), msg("user", "go on", nil)}, false},
		{"subagent's interrupt", []string{msg("assistant", text("working"), nil), msg("user", text("[Request interrupted by user]"), map[string]any{"isSidechain": true})}, false},
		{"meta line after it", []string{msg("user", text("[Request interrupted by user]"), nil), msg("user", "caveat", map[string]any{"isMeta": true})}, true},
		{"no timestamp", []string{line(map[string]any{"type": "user", "message": map[string]any{"content": "[Request interrupted by user]"}})}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := (Claude{}).Interrupted(writeTranscript(t, c.lines...))
			if ok != c.want || (ok && !got.Equal(at)) {
				t.Errorf("Interrupted = %v, %v; want %v at %v", got, ok, c.want, at)
			}
		})
	}
	// Only the end of a long transcript is read.
	long := []string{msg("user", strings.Repeat("x", interruptTail), nil), msg("user", text("[Request interrupted by user]"), nil)}
	if _, ok := (Claude{}).Interrupted(writeTranscript(t, long...)); !ok {
		t.Error("an interrupt after a line longer than the tail was missed")
	}
	if _, ok := (Claude{}).Interrupted(filepath.Join(t.TempDir(), "missing")); ok {
		t.Error("a missing transcript reads as interrupted")
	}
}
