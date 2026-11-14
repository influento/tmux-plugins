package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	bareComp   = `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"manual","preTokens":368624}}`
	bigCall    = `{"type":"assistant","isSidechain":false,"message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":1000,"cache_read_input_tokens":299998,"output_tokens":500}}}`
	toolResult = `{"type":"user","message":{"role":"user","content":"a tool result that quotes \"type\":\"assistant\""}}`
)

// The tokens are the main agent's last model call, or what a compaction
// after it left; a subagent's lines and other lines do not count.
func TestContextUsed(t *testing.T) {
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
			used, limit, ok := Claude{}.Context(transcript, dir, "", func(k string) string { return env[k] })
			if used != tc.want || ok != tc.ok {
				t.Errorf("got %d, %v; want %d, %v", used, ok, tc.want, tc.ok)
			}
			if ok && limit != windowDefault {
				t.Errorf("limit %d, want %d", limit, windowDefault)
			}
		})
	}
}

// The limit is where Claude Code compacts (autoCompactWindow), capped by
// the model's window, read from the strongest settings file that sets it.
// The project is the folder Claude Code started in, not the hook's cwd.
func TestContextLimit(t *testing.T) {
	saved := managedSettings
	t.Cleanup(func() { managedSettings = saved })
	for _, tc := range []struct {
		name  string
		files map[string]string // relative to the home folder
		env   map[string]string // folders relative to the home folder
		cwd   string
		used  int64
		want  int64
	}{
		{"nothing set", nil, nil, "src/proj-a", 1000, 200_000},
		{"long model", map[string]string{".claude/settings.json": `{"model":"opus[1m]"}`}, nil, "src/proj-a", 1000, 1_000_000},
		{"compacts early", map[string]string{".claude/settings.json": `{"model":"opus[1m]","autoCompactWindow":400000}`}, nil, "src/proj-a", 1000, 400_000},
		{"capped by the window", map[string]string{".claude/settings.json": `{"autoCompactWindow":400000}`}, nil, "src/proj-a", 1000, 200_000},
		{"a call past the short window", map[string]string{".claude/settings.json": `{"autoCompactWindow":400000}`}, nil, "src/proj-a", 300_000, 400_000},
		{"a call past the short window, no setting", nil, nil, "src/proj-a", 300_000, 1_000_000},
		{"project wins", map[string]string{
			".claude/settings.json":            `{"model":"opus[1m]","autoCompactWindow":400000}`,
			"src/proj-a/.claude/settings.json": `{"autoCompactWindow":300000}`,
		}, nil, "src/proj-a", 1000, 300_000},
		{"local wins over shared", map[string]string{
			".claude/settings.json":                  `{"model":"opus[1m]"}`,
			"src/proj-a/.claude/settings.json":       `{"autoCompactWindow":300000}`,
			"src/proj-a/.claude/settings.local.json": `{"autoCompactWindow":500000}`,
		}, nil, "src/proj-a", 1000, 500_000},
		{"the project folder, not the cwd", map[string]string{
			".claude/settings.json":                    `{"model":"opus[1m]"}`,
			"src/proj-a/.claude/settings.json":         `{"autoCompactWindow":300000}`,
			"src/proj-a/repos/b/.claude/settings.json": `{"autoCompactWindow":700000}`,
		}, map[string]string{"CLAUDE_PROJECT_DIR": "src/proj-a"}, "src/proj-a/repos/b", 1000, 300_000},
		{"managed wins", map[string]string{
			".claude/settings.json": `{"model":"opus[1m]","autoCompactWindow":400000}`,
			"managed.json":          `{"autoCompactWindow":600000}`,
		}, nil, "src/proj-a", 1000, 600_000},
		{"model from the environment", map[string]string{".claude/settings.json": `{"model":"opus[1m]"}`},
			map[string]string{"ANTHROPIC_MODEL": "sonnet"}, "src/proj-a", 1000, 200_000},
		{"config folder", map[string]string{"cfg/settings.json": `{"model":"opus[1m]"}`},
			map[string]string{"CLAUDE_CONFIG_DIR": "cfg"}, "src/proj-a", 1000, 1_000_000},
		{"unreadable file", map[string]string{".claude/settings.json": `{"model":`}, nil, "src/proj-a", 1000, 200_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			managedSettings = filepath.Join(home, "managed.json")
			for rel, data := range tc.files {
				writeFile(t, filepath.Join(home, rel), data)
			}
			env := map[string]string{"HOME": home}
			for k, v := range tc.env {
				env[k] = v
				if k != "ANTHROPIC_MODEL" {
					env[k] = filepath.Join(home, v)
				}
			}
			getenv := func(k string) string { return env[k] }
			if got := contextLimit(projectOf(filepath.Join(home, tc.cwd), "", getenv), getenv, tc.used); got != tc.want {
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
