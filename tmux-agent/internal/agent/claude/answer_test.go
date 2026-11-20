package claude

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An answer is the text of a message that ends a turn, its blocks joined;
// a message that calls a tool, a subagent's message and a message with no
// text are none.
func TestAnswers(t *testing.T) {
	lines := []string{
		`{"type":"user","timestamp":"2026-01-02T10:00:00.000Z","message":{"role":"user","content":"proj-a: first"}}`,
		`{"type":"assistant","timestamp":"2026-01-02T10:00:01.000Z","message":{"id":"m1","stop_reason":"tool_use","content":[{"type":"text","text":"Let me look."}]}}`,
		`{"type":"assistant","timestamp":"2026-01-02T10:00:01.000Z","message":{"id":"m1","stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash"}]}}`,
		`{"type":"user","timestamp":"2026-01-02T10:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","timestamp":"2026-01-02T10:00:03.000Z","message":{"id":"m2","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"hm"}]}}`,
		`{"type":"assistant","timestamp":"2026-01-02T10:00:03.500Z","message":{"id":"m2","stop_reason":"end_turn","content":[{"type":"text","text":"answer one"}]}}`,
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-01-02T10:00:04.000Z","message":{"id":"s1","stop_reason":"end_turn","content":[{"type":"text","text":"a subagent's"}]}}`,
		`{"type":"user","timestamp":"2026-01-02T10:01:00.000Z","message":{"role":"user","content":"second"}}`,
		`{"type":"assistant","timestamp":"2026-01-02T10:01:01.000Z","message":{"id":"m3","stop_reason":"end_turn","content":[{"type":"text","text":"two"},{"type":"text","text":"parts"}]}}`,
		`{"type":"assistant","timestamp":"2026-01-02T10:01:02.000Z","message":{"id":"m4","stop_reason":"end_turn","content":[{"type":"text","text":"  "}]}}`,
		`{"type":"system","subtype":"turn_duration"}`,
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	texts := func(n int) []string {
		var out []string
		for _, a := range (Claude{}).Answers(path, n) {
			out = append(out, a.Text)
		}
		return out
	}
	if got, want := texts(5), []string{"answer one", "two\n\nparts"}; !slices.Equal(got, want) {
		t.Errorf("Answers(5) = %q, want %q", got, want)
	}
	if got, want := texts(1), []string{"two\n\nparts"}; !slices.Equal(got, want) {
		t.Errorf("Answers(1) = %q, want %q", got, want)
	}
	if as := (Claude{}).Answers(path, 1); as[0].At.Format("15:04:05") != "10:01:01" {
		t.Errorf("At = %s, want the message's time", as[0].At)
	}
	if got := (Claude{}).Answers(filepath.Join(t.TempDir(), "none.jsonl"), 1); got != nil {
		t.Errorf("a missing transcript: %v", got)
	}
}

// Past answerTail only the tail is read first: an answer whose first
// lines lie before the tail's start still comes whole.
func TestAnswersAcrossTheTail(t *testing.T) {
	m1a := `{"type":"assistant","timestamp":"2026-01-02T10:00:01.000Z","message":{"id":"m1","stop_reason":"end_turn","content":[{"type":"text","text":"part one"}]}}` + "\n"
	m1b := `{"type":"assistant","timestamp":"2026-01-02T10:00:01.500Z","message":{"id":"m1","stop_reason":"end_turn","content":[{"type":"text","text":"part two"}]}}` + "\n"
	m2 := `{"type":"assistant","timestamp":"2026-01-02T10:01:01.000Z","message":{"id":"m2","stop_reason":"end_turn","content":[{"type":"text","text":"answer two"}]}}` + "\n"
	// The tail starts in the middle of m1a.
	cut := len(m1a) / 2
	head, end := `{"type":"user","message":{"role":"user","content":"`, `"}}`+"\n"
	fill := head + strings.Repeat("x", cut+answerTail-len(m1a)-len(m1b)-len(m2)-len(head)-len(end)) + end
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(m1a+m1b+fill+m2), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range (Claude{}).Answers(path, 2) {
		got = append(got, a.Text)
	}
	if want := []string{"part one\n\npart two", "answer two"}; !slices.Equal(got, want) {
		t.Errorf("Answers(2) = %q, want %q", got, want)
	}
}
