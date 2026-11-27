package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A session's start sets the pane's context share: the tokens of the
// transcript's last call over where Claude Code compacts, which is its
// autoCompactWindow, capped by the window its model catalog gives the
// call's model, less the 33,000 tokens it keeps free. The window Claude
// Code gives its status line command for the pane's session comes before
// the catalog's.
func TestContextShare(t *testing.T) {
	const catalog = `{"fetchedAt":1,"catalog":{"config":{"models":[` +
		`{"id":"model-long","runtime":{"max_input_tokens":1000000}},` +
		`{"id":"model-short","runtime":{"max_input_tokens":200000}}]}}}`
	for _, c := range []struct {
		name, model string
		tokens      int
		status      string // the status line input's session, with a 200,000 window
		first       bool   // the status line runs before the session's start hook
		want        string
	}{
		// 199,535 of 400,000 - 33,000: a pane showed 119 here.
		{"a long window", "model-long", 199_535, "", false, "54"},
		// 100,200 of 200,000 - 33,000.
		{"a short window", "model-short", 100_200, "", false, "60"},
		{"the status line's window", "model-long", 100_200, sessionA, false, "60"},
		{"the status line before the session's start", "model-long", 100_200, sessionA, true, "60"},
		// 100,200 of 400,000 - 33,000.
		{"the status line of another session", "model-long", 100_200, "22222222-2222-4333-8444-555555555555", false, "27"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			transcript := filepath.Join(e.dir, "s.jsonl")
			for path, data := range map[string]string{
				filepath.Join(e.Home, ".claude", "settings.json"):                           `{"autoCompactWindow":400000}`,
				filepath.Join(e.Home, ".claude", "cache", "model-catalog", "org-a-cc.json"): catalog,
				transcript: fmt.Sprintf(`{"type":"assistant","isSidechain":false,"message":{"model":%q,"usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`+"\n", c.model, c.tokens),
			} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			hooks := e.hooks(t, "start.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home, "transcript_path", transcript))
			status := ""
			if c.status != "" {
				path := filepath.Join(e.dir, "status.json")
				input := fmt.Sprintf(`{"session_id":%q,"context_window":{"context_window_size":200000},"workspace":{"project_dir":%q}}`, c.status, e.Home)
				if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
					t.Fatal(err)
				}
				status = "STUB_STATUS=" + shellQuote(path) + " "
				if c.first {
					status = "STUB_STATUS_FIRST=" + shellQuote(path) + " "
				}
			}
			e.launch(t, "rc", status+"STUB_MODE=wait STUB_HOOK="+shellQuote(hooks)+" tmux-agent run claude")
			e.file(t, "agent.pid")
			e.wantOpt(t, "@agent_context", c.want)
		})
	}
}
