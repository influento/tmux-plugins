package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

// call is an assistant line of model call id at minute min, with input,
// cache write, cache read and output tokens 1, 10, 100 and 1000 times n.
func call(id string, min, n int, extra string) string {
	return fmt.Sprintf(`{"type":"assistant",%s"timestamp":"2026-01-05T10:%02d:00.000Z","message":{"id":%q,"model":"model-a","content":[],"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}}`,
		extra, min, id, n, 10*n, 100*n, 1000*n)
}

func sum(calls []agent.Call) agent.Tokens {
	var t agent.Tokens
	for _, c := range calls {
		t = t.Plus(c.Tokens)
	}
	return t
}

func TestReadUsage(t *testing.T) {
	lines := []string{
		`{"type":"user","cwd":"/proj-a","message":{"content":"go"}}`,
		call("m1", 1, 1, ""),
		call("m1", 1, 1, ""), // a second block of m1
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		call("m1", 1, 1, ""), // m1 again after a tool's result
		call("m2", 2, 2, `"isSidechain":true,`),
		`{"type":"assistant","timestamp":"2026-01-05T10:03:00.000Z","message":{"id":"m3","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`,
		call("m4", 4, 3, ""),
		`not json`,
	}
	p := writeTranscript(t, lines...)
	read, err := (Claude{}).ReadUsage(p, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (agent.Tokens{Input: 4, CacheWrite: 40, CacheRead: 400, Output: 4000}); sum(read.Calls) != want {
		t.Errorf("tokens = %+v, want %+v", sum(read.Calls), want)
	}
	if len(read.Calls) != 2 || read.Calls[0].Model != "model-a" || !read.Calls[1].At.Equal(time.Date(2026, 1, 5, 10, 4, 0, 0, time.UTC)) {
		t.Errorf("calls = %+v", read.Calls)
	}
	if read.Cwd != "/proj-a" {
		t.Errorf("cwd = %q", read.Cwd)
	}
	fi, _ := os.Stat(p)
	if read.To != fi.Size() || !slices.Equal(read.Recent, []string{"m1", "m4"}) {
		t.Errorf("to = %d of %d, recent = %v", read.To, fi.Size(), read.Recent)
	}
}

// A read takes whole lines only, and the next read goes on from there:
// the lines of a call the first read counted do not count again.
func TestReadUsageGoesOn(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	first := call("m1", 1, 1, "") + "\n" + call("m2", 2, 1, "") + "\n"
	partial := call("m1", 1, 1, "")
	if err := os.WriteFile(p, []byte(first+partial[:40]), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := (Claude{}).ReadUsage(p, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.To != int64(len(first)) || len(a.Calls) != 2 {
		t.Fatalf("first read: to %d (want %d), %d calls", a.To, len(first), len(a.Calls))
	}
	rest := partial[40:] + "\n" + call("m3", 3, 5, "") + "\n"
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rest); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, err := (Claude{}).ReadUsage(p, a.To, a.Recent)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Calls) != 1 || b.Calls[0].Tokens.Input != 5 {
		t.Errorf("second read = %+v, want m3 alone", b.Calls)
	}
	if b.To != int64(len(first)+len(partial)+1+len(call("m3", 3, 5, ""))+1) {
		t.Errorf("second read to %d", b.To)
	}
	// Nothing new: nothing read.
	c, err := (Claude{}).ReadUsage(p, b.To, b.Recent)
	if err != nil || len(c.Calls) != 0 || c.To != b.To {
		t.Errorf("third read = %+v, %v", c, err)
	}
}

func TestReadUsageKeepsRecentBounded(t *testing.T) {
	var lines []string
	for i := range 100 {
		lines = append(lines, call(fmt.Sprintf("m%d", i), i%60, 1, ""))
	}
	read, err := (Claude{}).ReadUsage(writeTranscript(t, lines...), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Calls) != 100 || len(read.Recent) != recentIDs || read.Recent[recentIDs-1] != "m99" {
		t.Errorf("%d calls, recent %d ending %q", len(read.Calls), len(read.Recent), read.Recent[len(read.Recent)-1])
	}
}

func TestTranscripts(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-proj-a")
	if err := os.MkdirAll(filepath.Join(dir, "s1", "subagents"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"s1.jsonl", "s2.jsonl", "notes.txt", filepath.Join("s1", "subagents", "agent-x.jsonl")} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := (Claude{}).Transcripts(func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, sf := range got {
		names = append(names, sf.Session+"="+strings.TrimPrefix(sf.Transcript, dir+"/"))
	}
	if want := []string{"s1=s1.jsonl", "s2=s2.jsonl"}; !slices.Equal(names, want) {
		t.Errorf("Transcripts = %v, want %v", names, want)
	}
}

func TestReadUsageOneShot(t *testing.T) {
	for entry, want := range map[string]bool{"sdk-cli": true, "cli": false} {
		p := writeTranscript(t, `{"type":"user","cwd":"/proj-a","entrypoint":"`+entry+`","message":{"content":"go"}}`)
		read, err := (Claude{}).ReadUsage(p, 0, nil)
		if err != nil || read.OneShot != want {
			t.Errorf("entrypoint %s: one-shot %v, %v; want %v", entry, read.OneShot, err, want)
		}
	}
}

func TestRunOwner(t *testing.T) {
	const a, b = "11111111-2222-4333-8444-555555555555", "66666666-2222-4333-8444-555555555555"
	for cwd, want := range map[string]string{
		"/tmp/claude-1000/-work-proj-a/" + a + "/scratchpad":                                 a,
		"/tmp/claude-1000/-work-proj-a/" + a + "/scratchpad/runs/1/.worktrees/b-x":           a,
		"/tmp/claude-1000/-work-proj-a/" + a + "/scratchpad/x/-tmp-y/" + b + "/scratchpad/z": b,
		"/tmp/claude-1000/-work-proj-a/" + a + "/notes":                                      "",
		"/work/proj-a/" + a + "/scratchpad":                                                  "",
		"/work/proj-a":                                                                       "",
	} {
		if got := (Claude{}).RunOwner(cwd); got != want {
			t.Errorf("RunOwner(%q) = %q, want %q", cwd, got, want)
		}
	}
}

// A read leaves the ids it was handed as they were, also when their slice
// has room to grow.
func TestReadUsageLeavesRecent(t *testing.T) {
	recent := make([]string, 1, 8)
	recent[0] = "m0"
	read, err := (Claude{}).ReadUsage(writeTranscript(t, call("m1", 1, 1, "")), 0, recent)
	if err != nil {
		t.Fatal(err)
	}
	if got := recent[:2]; got[1] != "" || len(read.Recent) != 2 {
		t.Errorf("handed ids now %q, read's %q", got, read.Recent)
	}
}
