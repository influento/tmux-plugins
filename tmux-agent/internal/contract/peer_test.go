package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// list --json gives each agent pane its agent's address in the agent's
// own channel: for Claude Code, SendMessage and the session's name from
// the file Claude Code keeps for the agent process. A pane whose file is
// missing, or of another session, has none; nor has a pane with no agent.
func TestListNative(t *testing.T) {
	e := newEnv(t)
	paneA := e.startChat(t, "agent-a", sessionA)
	paneB := e.startChat(t, "agent-b", sessionB)
	dir := filepath.Join(e.Home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(tag, session, name string) {
		body := `{"pid":` + strconv.Itoa(e.number(t, tag+".pid")) + `,"sessionId":"` + session + `","name":"` + name + `","status":"idle"}`
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(e.number(t, tag+".pid"))+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("agent-a", sessionA, "proj-a-1f")
	write("agent-b", sessionA, "proj-a-2c") // a stale file: another session

	out, stderr, code := e.tmuxAgent(t, "list", "--json")
	if code != 0 {
		t.Fatalf("list --json: exit %d, %s", code, stderr)
	}
	var rows []struct {
		Pane   string `json:"pane"`
		Native *struct {
			Via string `json:"via"`
			To  string `json:"to"`
		} `json:"native"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("list --json: %v\n%s", err, out)
	}
	want := map[string]string{
		paneA:  "SendMessage proj-a-1f",
		paneB:  "",
		e.pane: "",
	}
	seen := map[string]bool{}
	for _, r := range rows {
		w, ok := want[r.Pane]
		if !ok {
			continue
		}
		seen[r.Pane] = true
		got := ""
		if r.Native != nil {
			got = r.Native.Via + " " + r.Native.To
		}
		if got != w {
			t.Errorf("pane %s: native = %q, want %q", r.Pane, got, w)
		}
	}
	for p := range want {
		if !seen[p] {
			t.Errorf("pane %s: no row in\n%s", p, out)
		}
	}
}
