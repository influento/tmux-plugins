package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	callA      = `{"type":"assistant","isSidechain":false,"message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":1000,"cache_read_input_tokens":90998,"output_tokens":500}}}`
	callSide   = `{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":5,"output_tokens":1}}}`
	compact    = `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto","preTokens":368624,"postTokens":29656}}`
	synthetic  = `{"type":"assistant","isSidechain":false,"message":{"model":"<synthetic>","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`
	zeroCall   = `{"type":"assistant","isSidechain":false,"message":{"model":"claude-x","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`
	compactOld = `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto","preTokens":368624,"postTokens":11111}}`
	bareComp   = `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":368624}}`
	bigCall    = `{"type":"assistant","isSidechain":false,"message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":1000,"cache_read_input_tokens":299998,"output_tokens":500}}}`
	callShort  = `{"type":"assistant","isSidechain":false,"message":{"model":"model-short","usage":{"input_tokens":2,"cache_creation_input_tokens":1000,"cache_read_input_tokens":90998,"output_tokens":500}}}`
	// catalogA is a model catalog as Claude Code caches it.
	catalogA   = `{"fetchedAt":10,"catalog":{"config":{"models":[{"id":"model-long","runtime":{"max_input_tokens":1000000}},{"id":"model-short","runtime":{"max_input_tokens":200000}}]}}}`
	toolResult = `{"type":"user","message":{"role":"user","content":"a tool result that quotes \"type\":\"assistant\""}}`
)

// The tokens are the main agent's last model call, or what a compaction
// after it left; a subagent's lines and other lines do not count.
func TestContextUsed(t *testing.T) {
	saved := managedSettings
	t.Cleanup(func() { managedSettings = saved })
	managedSettings = filepath.Join(t.TempDir(), "managed.json")
	for _, tc := range []struct {
		name  string
		lines []string
		want  int64
		ok    bool
	}{
		{"last call", []string{callA, toolResult}, 92000, true},
		{"subagent line after it", []string{callA, callSide}, 92000, true},
		{"compaction after it", []string{callA, compact, toolResult}, 29656, true},
		{"call after the compaction", []string{compact, callA}, 92000, true},
		{"an API error after it", []string{callA, synthetic}, 92000, true},
		{"an empty call after it", []string{callA, zeroCall}, 92000, true},
		{"a compaction that says nothing", []string{callA, bareComp}, 0, false},
		{"two compactions", []string{callA, compactOld, compact}, 29656, true},
		{"a compaction after one that says nothing", []string{callA, bareComp, compact}, 29656, true},
		{"no call yet", []string{toolResult}, 0, false},
		{"no transcript", nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			transcript := ""
			if tc.lines != nil {
				transcript = filepath.Join(dir, "s.jsonl")
				writeFile(t, transcript, strings.Join(tc.lines, "\n")+"\n")
			}
			env := map[string]string{"HOME": dir}
			used, limit, ok := Claude{}.Context(transcript, dir, "", 0, func(k string) string { return env[k] })
			if used != tc.want || ok != tc.ok {
				t.Errorf("got %d, %v; want %d, %v", used, ok, tc.want, tc.ok)
			}
			if ok && limit != 967_000 {
				t.Errorf("limit %d, want 967000", limit)
			}
		})
	}
}

// The share is what Claude Code's own indicator shows: a pane whose last
// call held 354,938 tokens, with autoCompactWindow 400000, showed "3% until
// auto-compact", so the panel shows 97.
func TestContextShareMatchesClaudeCode(t *testing.T) {
	saved := managedSettings
	t.Cleanup(func() { managedSettings = saved })
	home := t.TempDir()
	managedSettings = filepath.Join(home, "managed.json")
	writeFile(t, filepath.Join(home, ".claude/settings.json"), `{"autoCompactWindow":400000}`)
	transcript := filepath.Join(home, "s.jsonl")
	writeFile(t, transcript, `{"type":"assistant","isSidechain":false,"message":{"model":"claude-x","usage":{"input_tokens":1,"cache_creation_input_tokens":938,"cache_read_input_tokens":353999,"output_tokens":90}}}`+"\n")
	env := map[string]string{"HOME": home}
	used, limit, ok := Claude{}.Context(transcript, filepath.Join(home, "src/proj-a"), "", 0, func(k string) string { return env[k] })
	if !ok {
		t.Fatal("no context read")
	}
	if share := (used*100 + limit/2) / limit; share != 97 {
		t.Errorf("share %d%% (%d of %d), want 97%%: Claude Code shows 3%% until auto-compact", share, used, limit)
	}
}

// After a compaction the model is that of the last call before it.
func TestContextModelAfterCompaction(t *testing.T) {
	saved := managedSettings
	t.Cleanup(func() { managedSettings = saved })
	home := t.TempDir()
	managedSettings = filepath.Join(home, "managed.json")
	writeFile(t, filepath.Join(home, ".claude/cache/model-catalog/org-a-cc.json"), catalogA)
	transcript := filepath.Join(home, "s.jsonl")
	writeFile(t, transcript, strings.Join([]string{callA, callShort, compact, toolResult}, "\n")+"\n")
	env := map[string]string{"HOME": home}
	used, limit, ok := Claude{}.Context(transcript, home, "", 0, func(k string) string { return env[k] })
	if used != 29656 || limit != 167_000 || !ok {
		t.Errorf("got %d of %d, %v; want 29656 of 167000, true", used, limit, ok)
	}
}

// The limit is where Claude Code compacts: the autoCompactWindow of the
// strongest settings file that sets it, capped by the window the newest
// model catalog that lists the model gives it (1M for a model none lists),
// less 33,000 tokens it keeps free.
// The project is the folder Claude Code started in, not the hook's cwd.
func TestContextLimit(t *testing.T) {
	saved := managedSettings
	t.Cleanup(func() { managedSettings = saved })
	for _, tc := range []struct {
		name  string
		files map[string]string // relative to the home folder
		env   map[string]string // folders relative to the home folder
		cwd   string
		model string
		want  int64
	}{
		{"nothing set", nil, nil, "src/proj-a", "model-long", 967_000},
		{"compacts early", map[string]string{".claude/settings.json": `{"autoCompactWindow":400000}`}, nil, "src/proj-a", "model-long", 367_000},
		{"capped by the window", map[string]string{".claude/settings.json": `{"autoCompactWindow":2000000}`}, nil, "src/proj-a", "model-long", 967_000},
		{"a long model", map[string]string{
			".claude/settings.json":                            `{"autoCompactWindow":400000}`,
			".claude/cache/model-catalog/org-a-cc.json":        catalogA,
			".claude/cache/model-catalog/org-a-cc.badges.json": `{}`,
		}, nil, "src/proj-a", "model-long", 367_000},
		{"a short model", map[string]string{".claude/cache/model-catalog/org-a-cc.json": catalogA}, nil, "src/proj-a", "model-short", 167_000},
		{"a short model caps the setting", map[string]string{
			".claude/settings.json":                     `{"autoCompactWindow":400000}`,
			".claude/cache/model-catalog/org-a-cc.json": catalogA,
		}, nil, "src/proj-a", "model-short", 167_000},
		{"a model the catalog does not list", map[string]string{".claude/cache/model-catalog/org-a-cc.json": catalogA}, nil, "src/proj-a", "model-x", 967_000},
		{"no call names a model", map[string]string{".claude/cache/model-catalog/org-a-cc.json": catalogA}, nil, "src/proj-a", "", 967_000},
		{"the newest catalog that lists the model", map[string]string{
			".claude/cache/model-catalog/org-a-cc.json": `{"fetchedAt":20,"catalog":{"config":{"models":[{"id":"model-short","runtime":{"max_input_tokens":500000}}]}}}`,
			".claude/cache/model-catalog/org-b-cc.json": catalogA,
			".claude/cache/model-catalog/org-c-cc.json": `{"fetchedAt":30,"catalog":{"config":{"models":[{"id":"model-long","runtime":{"max_input_tokens":1000000}}]}}}`,
		}, nil, "src/proj-a", "model-short", 467_000},
		{"a catalog that gives the model no window", map[string]string{
			".claude/cache/model-catalog/org-a-cc.json": catalogA,
			".claude/cache/model-catalog/org-b-cc.json": `{"fetchedAt":20,"catalog":{"config":{"models":[{"id":"model-short"}]}}}`,
		}, nil, "src/proj-a", "model-short", 167_000},
		{"an unreadable catalog", map[string]string{
			".claude/cache/model-catalog/org-a-cc.json": catalogA,
			".claude/cache/model-catalog/org-b-cc.json": `{"fetchedAt":20,"catalog":`,
		}, nil, "src/proj-a", "model-short", 167_000},
		{"the catalog in the config folder", map[string]string{"cfg/cache/model-catalog/org-a-cc.json": catalogA},
			map[string]string{"CLAUDE_CONFIG_DIR": "cfg"}, "src/proj-a", "model-short", 167_000},
		{"project wins", map[string]string{
			".claude/settings.json":            `{"autoCompactWindow":400000}`,
			"src/proj-a/.claude/settings.json": `{"autoCompactWindow":300000}`,
		}, nil, "src/proj-a", "model-long", 267_000},
		{"local wins over shared", map[string]string{
			"src/proj-a/.claude/settings.json":       `{"autoCompactWindow":300000}`,
			"src/proj-a/.claude/settings.local.json": `{"autoCompactWindow":500000}`,
		}, nil, "src/proj-a", "model-long", 467_000},
		{"the project folder, not the cwd", map[string]string{
			"src/proj-a/.claude/settings.json":         `{"autoCompactWindow":300000}`,
			"src/proj-a/repos/b/.claude/settings.json": `{"autoCompactWindow":700000}`,
		}, map[string]string{"CLAUDE_PROJECT_DIR": "src/proj-a"}, "src/proj-a/repos/b", "model-long", 267_000},
		{"managed wins", map[string]string{
			".claude/settings.json": `{"autoCompactWindow":400000}`,
			"managed.json":          `{"autoCompactWindow":600000}`,
		}, nil, "src/proj-a", "model-long", 567_000},
		{"config folder", map[string]string{"cfg/settings.json": `{"autoCompactWindow":400000}`},
			map[string]string{"CLAUDE_CONFIG_DIR": "cfg"}, "src/proj-a", "model-long", 367_000},
		{"unreadable file", map[string]string{
			".claude/settings.json":            `{"autoCompactWindow":400000}`,
			"src/proj-a/.claude/settings.json": `{"autoCompactWindow":`,
		}, nil, "src/proj-a", "model-long", 367_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			managedSettings = filepath.Join(home, "managed.json")
			for rel, data := range tc.files {
				writeFile(t, filepath.Join(home, rel), data)
			}
			env := map[string]string{"HOME": home}
			for k, v := range tc.env {
				env[k] = filepath.Join(home, v)
			}
			getenv := func(k string) string { return env[k] }
			if got := contextLimit(projectOf(filepath.Join(home, tc.cwd), "", getenv), modelWindow(tc.model, getenv), getenv); got != tc.want {
				t.Errorf("limit %d, want %d", got, tc.want)
			}
		})
	}
}

// The project folder is the hooks' CLAUDE_PROJECT_DIR, else the agent
// process's folder, else the session's.
func TestProjectOf(t *testing.T) {
	env := func(dir string) func(string) string {
		return func(k string) string {
			if k == "CLAUDE_PROJECT_DIR" {
				return dir
			}
			return ""
		}
	}
	for _, tc := range []struct{ name, env, start, cwd, want string }{
		{"the hooks' variable", "/p/proj-a", "/p/proj-b", "/p/proj-c", "/p/proj-a"},
		{"the process's folder", "", "/p/proj-b", "/p/proj-c", "/p/proj-b"},
		{"the session's folder", "", "", "/p/proj-c", "/p/proj-c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectOf(tc.cwd, tc.start, env(tc.env)); got != tc.want {
				t.Errorf("project %q, want %q", got, tc.want)
			}
		})
	}
}

// Without a home folder, no settings file is read from a relative path.
func TestSettingsFilesWithoutHome(t *testing.T) {
	for _, f := range settingsFiles("", func(string) string { return "" }) {
		if !filepath.IsAbs(f) {
			t.Errorf("settings file %q is not absolute", f)
		}
	}
}

// The status line's input names the session, the window Claude Code counts
// with and the folder it started in; input without a session or a window
// says nothing.
func TestReadStatus(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        agent.Status
		ok          bool
	}{
		{"a session's status", `{"session_id":"s-1","model":{"id":"model-a"},"workspace":{"current_dir":"/p/proj-a/sub","project_dir":"/p/proj-a"},"context_window":{"total_input_tokens":0,"context_window_size":200000,"current_usage":null,"used_percentage":null}}`,
			agent.Status{Session: "s-1", Window: 200_000, Project: "/p/proj-a"}, true},
		{"no workspace", `{"session_id":"s-1","context_window":{"context_window_size":1000000}}`, agent.Status{Session: "s-1", Window: 1_000_000}, true},
		{"no session", `{"context_window":{"context_window_size":200000}}`, agent.Status{}, false},
		{"no window", `{"session_id":"s-1","context_window":null}`, agent.Status{}, false},
		{"a window of nothing", `{"session_id":"s-1","context_window":{"context_window_size":0}}`, agent.Status{}, false},
		{"not JSON", `{"session_id":`, agent.Status{}, false},
		{"empty", ``, agent.Status{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := (Claude{}).ReadStatus([]byte(tc.input)); got != tc.want || ok != tc.ok {
				t.Errorf("got %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The window Claude Code told comes before the catalog's.
func TestContextWindowTold(t *testing.T) {
	saved := managedSettings
	t.Cleanup(func() { managedSettings = saved })
	home := t.TempDir()
	managedSettings = filepath.Join(home, "managed.json")
	writeFile(t, filepath.Join(home, ".claude/cache/model-catalog/org-a-cc.json"), catalogA)
	writeFile(t, filepath.Join(home, ".claude/settings.json"), `{"autoCompactWindow":400000}`)
	transcript := filepath.Join(home, "s.jsonl")
	writeFile(t, transcript, callShort+"\n")
	env := map[string]string{"HOME": home}
	for _, tc := range []struct {
		name          string
		window, limit int64
	}{
		{"none told: the catalog's 200,000", 0, 167_000},
		{"a longer one than the catalog's", 1_000_000, 367_000},
		{"a shorter one than the setting", 300_000, 267_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, limit, ok := (Claude{}).Context(transcript, home, "", tc.window, func(k string) string { return env[k] }); limit != tc.limit || !ok {
				t.Errorf("limit %d, %v; want %d, true", limit, ok, tc.limit)
			}
		})
	}
}
