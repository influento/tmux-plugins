package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/lockfile"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

const tokensSession = "aaaaaaaa-1111-4222-8333-444444444444"

// tokensApp is an app whose HOME holds one Claude Code transcript in
// /work/proj-a, with two model calls, and whose store is empty.
func tokensApp(t *testing.T) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home, state := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-work-proj-a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","cwd":"/work/proj-a","message":{"content":"fix the build"}}`,
		`{"type":"assistant","timestamp":"2026-01-05T10:00:00Z","message":{"id":"m1","model":"model-a","usage":{"input_tokens":3,"cache_creation_input_tokens":1000,"cache_read_input_tokens":20000,"output_tokens":400}}}`,
		`{"type":"assistant","timestamp":"2026-01-05T10:01:00Z","message":{"id":"m2","model":"model-a","usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":21000,"output_tokens":100}}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, tokensSession+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "XDG_STATE_HOME": state, "XDG_RUNTIME_DIR": t.TempDir()}
	var stdout, stderr bytes.Buffer
	return newApp("test", &stdout, &stderr, func(k string) string { return env[k] }), &stdout, &stderr
}

var tokensNow = time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC)

func TestTokens(t *testing.T) {
	a, stdout, stderr := tokensApp(t)
	if code := a.tokenReport([]string{"--by", "path", "--since", "7d"}, tokensNow); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := "path           fresh in  cache read    output  subagents   runs\n" +
		"/work/proj-a       1.0k       41.0k       500          –      –\n" +
		"total              1.0k       41.0k       500          –      –\n"
	if stdout.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", stdout, want)
	}
}

func TestTokensPath(t *testing.T) {
	for path, want := range map[string]string{"/work/proj-a": "500", "/work/proj-b": "0"} {
		a, stdout, stderr := tokensApp(t)
		if code := a.tokenReport([]string{"--by", "agent", "--since", "all", "--path", path}, tokensNow); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		if f := strings.Fields(lines[len(lines)-1]); f[3] != want {
			t.Errorf("--path %s: total line %q, want output %s", path, lines[len(lines)-1], want)
		}
	}
}

func TestTokensJSON(t *testing.T) {
	a, stdout, stderr := tokensApp(t)
	if code := a.tokenReport([]string{"--by", "session", "--since", "all", "--json"}, tokensNow); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var r struct {
		Rows []struct {
			Key, Session string
			Output       int64
		}
	}
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 1 || r.Rows[0].Session != tokensSession || r.Rows[0].Output != 500 || r.Rows[0].Key != "aaaaaaaa proj-a · fix the build" {
		t.Errorf("rows = %+v", r.Rows)
	}
}

func TestTokensScanPrintsNothing(t *testing.T) {
	a, stdout, stderr := tokensApp(t)
	if code := a.tokenReport([]string{"scan"}, tokensNow); code != 0 || stdout.Len() != 0 {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	u, ok, err := a.env.Store.LoadUsage("claude", tokensSession)
	if err != nil || !ok || u.Hours["2026-01-05T10"]["model-a"].Output != 500 {
		t.Errorf("tally = %+v, %v, %v", u, ok, err)
	}
}

// One pass reads at a time: a background scan leaves the work to the pass
// that runs, a report waits for it.
func TestTokensOnePassAtATime(t *testing.T) {
	a, stdout, stderr := tokensApp(t)
	unlock, err := lockfile.Lock(t.Context(), filepath.Join(store.RuntimeDir(a.getenv), "tokens.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if code := a.tokenReport([]string{"scan"}, tokensNow); code != 0 {
		t.Fatalf("scan: exit %d: %s", code, stderr)
	}
	if _, ok, _ := a.env.Store.LoadUsage("claude", tokensSession); ok {
		t.Fatal("scan read while another pass ran")
	}
	done := make(chan int)
	go func() { done <- a.tokenReport([]string{"--by", "agent"}, tokensNow) }()
	select {
	case <-done:
		t.Fatal("report ran while another pass ran")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	if code := <-done; code != 0 || !strings.Contains(stdout.String(), "500") {
		t.Errorf("report: exit %d, %q, %q", code, stdout, stderr)
	}
}

func TestTokensRefusesBadArguments(t *testing.T) {
	for _, args := range [][]string{{"--by", "project"}, {"--since", "soon"}, {"--sort", "price"}, {"extra"}, {"--top", "-1"}} {
		a, _, _ := tokensApp(t)
		if code := a.tokenReport(args, tokensNow); code == 0 {
			t.Errorf("tokens %v: exit 0", args)
		}
	}
}
