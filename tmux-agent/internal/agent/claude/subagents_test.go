package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

// jsonLines writes v as one JSON line each.
func jsonLines(t *testing.T, vs ...any) string {
	t.Helper()
	var b strings.Builder
	for _, v := range vs {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

func userLine(at, content string) map[string]any {
	return map[string]any{"type": "user", "timestamp": at, "isSidechain": true,
		"message": map[string]any{"role": "user", "content": content}}
}

func assistantLine(at, id string, usage map[string]any, blocks ...map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "timestamp": at, "isSidechain": true,
		"message": map[string]any{"id": id, "model": "model-a", "role": "assistant", "content": blocks, "usage": usage}}
}

func usage(in, write, read, out int) map[string]any {
	return map[string]any{"input_tokens": in, "cache_creation_input_tokens": write, "cache_read_input_tokens": read, "output_tokens": out}
}

// A history: the prompt first, one model call written as two lines (its
// usage counts once), a request blob without a time, a handback.
func sampleHistory(t *testing.T) string {
	return jsonLines(t,
		userLine("2026-11-12T03:15:37.545Z", "Check the build in proj-a."),
		map[string]any{"type": "api-request-blob", "message": map[string]any{"content": "big"}},
		assistantLine("2026-11-12T03:15:38.000Z", "m1", usage(2, 100, 1000, 10),
			map[string]any{"type": "text", "text": "Looking."}),
		assistantLine("2026-11-12T03:15:38.100Z", "m1", usage(2, 100, 1000, 10),
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "make"}}),
		userLine("2026-11-12T03:15:40.000Z", "tool result"),
		assistantLine("2026-11-12T03:15:44.643Z", "m2", usage(1, 50, 2000, 20),
			map[string]any{"type": "tool_use", "name": "SubagentHandback", "input": map[string]any{"message": "The build passes."}}),
	)
}

func TestReadHistory(t *testing.T) {
	h, err := readHistory(strings.NewReader(sampleHistory(t)))
	if err != nil {
		t.Fatal(err)
	}
	if h.prompt != "Check the build in proj-a." || h.result != "The build passes." || h.model != "model-a" {
		t.Errorf("prompt %q, result %q, model %q", h.prompt, h.result, h.model)
	}
	if want := (agent.Tokens{Input: 3, CacheWrite: 150, CacheRead: 3000, Output: 30}); h.tokens != want {
		t.Errorf("tokens = %+v, want %+v (each message once)", h.tokens, want)
	}
	if got := h.last.Sub(h.first); got != 7098*time.Millisecond {
		t.Errorf("time = %v, want 7.098s", got)
	}
}

// Before SubagentHandback, a subagent's result was its last text.
func TestReadHistoryResultIsLastText(t *testing.T) {
	h, err := readHistory(strings.NewReader(jsonLines(t,
		userLine("2026-11-12T03:15:37Z", "Look."),
		assistantLine("2026-11-12T03:15:38Z", "m1", usage(1, 1, 1, 1), map[string]any{"type": "text", "text": "First."}),
		assistantLine("2026-11-12T03:15:39Z", "m2", usage(1, 1, 1, 1), map[string]any{"type": "text", "text": "Found it."},
			map[string]any{"type": "text", "text": "  "}),
	)))
	if err != nil || h.result != "Found it." {
		t.Errorf("result = %q (%v), want the last text", h.result, err)
	}
}

// A line past maxLine is skipped whole; the lines around it are read.
func TestEachLineSkipsLongLines(t *testing.T) {
	long := strings.Repeat("x", maxLine+10)
	in := "one\n" + long + "\ntwo\n" + strings.Repeat("y", 100) + "\nlast"
	var got []string
	err := eachLine(bufio.NewReaderSize(strings.NewReader(in), 16), func(l []byte) {
		got = append(got, strings.TrimSuffix(string(l), "\n"))
	})
	want := []string{"one", "two", strings.Repeat("y", 100), "last"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("lines = %d %.40q (%v), want %d", len(got), got, err, len(want))
	}
}

// subagentFiles writes a session's subagent folder: a meta file and,
// unless history is "", a history.
func subagentFiles(t *testing.T, transcript, id string, m map[string]any, history string) string {
	t.Helper()
	dir := subagentsDir(transcript)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "agent-"+id+".meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "agent-"+id+".jsonl")
	if history != "" {
		if err := os.WriteFile(p, []byte(history), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestReadSubagentOutcomes(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "projects", "-src-proj-a", "11111111-2222-4333-8444-555555555555.jsonl")
	hist := sampleHistory(t)
	base := map[string]any{"agentType": "Explore", "description": "probe", "spawnDepth": 2}
	with := func(k string) map[string]any {
		m := map[string]any{"agentType": "Explore", "description": "probe", "spawnDepth": 2}
		if k != "" {
			m[k] = true
		}
		return m
	}
	subagentFiles(t, transcript, "a1", with("finished"), hist)
	subagentFiles(t, transcript, "a2", with("stoppedByUser"), hist)
	subagentFiles(t, transcript, "a3", base, hist)
	stale := subagentFiles(t, transcript, "a4", base, hist)
	old := time.Now().Add(-2 * staleAfter)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	subagentFiles(t, transcript, "a5", with("finished"), "") // its history deleted

	ids, err := Claude{}.SubagentIDs(transcript)
	if err != nil || !slices.Equal(ids, []string{"a1", "a2", "a3", "a4", "a5"}) {
		t.Fatalf("ids = %v (%v)", ids, err)
	}
	for _, c := range []struct {
		id      string
		outcome agent.Outcome
		read    bool // the history was read
	}{
		{"a1", agent.OutcomeDone, true},
		{"a2", agent.OutcomeStopped, true},
		{"a3", "", false}, // it runs
		{"a4", agent.OutcomeUnknown, true},
		{"a5", agent.OutcomeDone, false},
	} {
		t.Run(c.id, func(t *testing.T) {
			sub, err := Claude{}.ReadSubagent(transcript, c.id)
			if err != nil {
				t.Fatal(err)
			}
			if sub.Outcome != c.outcome || sub.Type != "Explore" || sub.Description != "probe" || sub.Depth != 2 {
				t.Errorf("subagent = %+v, want outcome %q", sub, c.outcome)
			}
			if read := sub.Result != "" && sub.Tokens != nil; read != c.read {
				t.Errorf("history read = %v, want %v", read, c.read)
			}
		})
	}
}

func TestSubagentSessions(t *testing.T) {
	home := t.TempDir()
	const sid = "11111111-2222-4333-8444-555555555555"
	transcript := filepath.Join(home, ".claude", "projects", "-src-proj-a", sid+".jsonl")
	subagentFiles(t, transcript, "a1", map[string]any{"finished": true}, "")
	env := map[string]string{"HOME": home}
	got, err := Claude{}.SubagentSessions(func(k string) string { return env[k] })
	if err != nil || len(got) != 1 || got[0] != (agent.SessionFiles{Session: sid, Transcript: transcript}) {
		t.Errorf("sessions = %+v (%v)", got, err)
	}
	env["CLAUDE_CONFIG_DIR"] = filepath.Join(home, "elsewhere")
	if got, err := (Claude{}).SubagentSessions(func(k string) string { return env[k] }); err != nil || len(got) != 0 {
		t.Errorf("with CLAUDE_CONFIG_DIR elsewhere: %+v (%v)", got, err)
	}
}
