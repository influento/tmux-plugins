package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// contextTail is how much of a transcript's end Context reads: the last
// model call is there unless a tool result past this size came after it.
const contextTail = 1 << 20

// Context windows: a model named with [1m] has the long one.
const (
	windowDefault int64 = 200_000
	windowLong    int64 = 1_000_000
)

// managedSettings is the settings file an administrator sets; it wins over
// every other.
var managedSettings = "/etc/claude-code/managed-settings.json"

// Context reads the tokens of the main agent's last model call (input,
// cache written and cache read: what the next call sends again) or, after
// a compaction, the tokens it left. The limit is Claude Code's
// autoCompactWindow setting, where it compacts, capped by the model's
// window; without the setting, the window. The transcript does not say
// which window a model has: a model named with [1m] has the long one, and
// so has one whose call held more than the short one.
func (Claude) Context(transcript, cwd, start string, getenv func(string) string) (used, limit int64, ok bool) {
	used, ok = lastContext(tailLines(transcript, contextTail))
	if !ok {
		return 0, 0, false
	}
	return used, contextLimit(projectOf(cwd, start, getenv), getenv, used), true
}

// projectOf is the folder Claude Code started in, which it gives its hooks
// as CLAUDE_PROJECT_DIR; a watcher knows it as the folder of the agent's
// process, and the session's cwd, which follows cd and worktrees, stands
// in only without either.
func projectOf(cwd, start string, getenv func(string) string) string {
	return or(getenv("CLAUDE_PROJECT_DIR"), or(start, cwd))
}

// lastContext finds, from the end, the main agent's last model call or
// compaction.
func lastContext(lines [][]byte) (int64, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !bytes.Contains(line, []byte(`"assistant"`)) && !bytes.Contains(line, []byte(`"compact_boundary"`)) {
			continue
		}
		var e struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			Sidechain bool   `json:"isSidechain"`
			Compact   *struct {
				Post int64 `json:"postTokens"`
			} `json:"compactMetadata"`
			Message struct {
				Model string `json:"model"`
				Usage *struct {
					Input      int64 `json:"input_tokens"`
					CacheWrite int64 `json:"cache_creation_input_tokens"`
					CacheRead  int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.Sidechain {
			continue
		}
		switch {
		case e.Type == "assistant" && e.Message.Usage != nil:
			u := e.Message.Usage
			if n := u.Input + u.CacheWrite + u.CacheRead; n > 0 && e.Message.Model != "<synthetic>" {
				return n, true
			}
			// A message Claude Code wrote itself (an API error, an
			// interrupt): no model call, so look further back.
		case e.Type == "system" && e.Subtype == "compact_boundary":
			if e.Compact == nil || e.Compact.Post <= 0 {
				// What was before the compaction no longer counts.
				return 0, false
			}
			return e.Compact.Post, true
		}
	}
	return 0, false
}

// contextLimit reads the limit from Claude Code's settings, the first file
// that sets a value winning: managed, the project's local and shared
// files, the user's. ANTHROPIC_MODEL names the model before any file.
func contextLimit(project string, getenv func(string) string, used int64) int64 {
	var compactAt int64
	model := getenv("ANTHROPIC_MODEL")
	for _, path := range settingsFiles(project, getenv) {
		var s struct {
			AutoCompactWindow *int64  `json:"autoCompactWindow"`
			Model             *string `json:"model"`
		}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &s) != nil {
			continue // missing or unreadable: the next file decides
		}
		if compactAt == 0 && s.AutoCompactWindow != nil && *s.AutoCompactWindow > 0 {
			compactAt = *s.AutoCompactWindow
		}
		if model == "" && s.Model != nil {
			model = *s.Model
		}
	}
	window := windowDefault
	if strings.HasSuffix(strings.ToLower(model), "[1m]") || used > windowDefault {
		window = windowLong
	}
	if compactAt > 0 && compactAt < window {
		return compactAt
	}
	return window
}

// settingsFiles are Claude Code's settings files, the strongest first. A
// folder that is not absolute is left out.
func settingsFiles(project string, getenv func(string) string) []string {
	files := []string{managedSettings}
	if filepath.IsAbs(project) {
		files = append(files,
			filepath.Join(project, ".claude", "settings.local.json"),
			filepath.Join(project, ".claude", "settings.json"))
	}
	config := getenv("CLAUDE_CONFIG_DIR")
	if !filepath.IsAbs(config) {
		config = filepath.Join(getenv("HOME"), ".claude")
	}
	if filepath.IsAbs(config) {
		files = append(files, filepath.Join(config, "settings.json"))
	}
	return files
}
