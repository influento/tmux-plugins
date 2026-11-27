package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

// contextTail is how much of a transcript's end Context reads: the last
// model call is there unless a tool result past this size came after it.
const contextTail = 1 << 20

// contextWindow is the window of a model no catalog lists.
const contextWindow int64 = 1_000_000

// Claude Code caches the model catalog its server sends, one file per
// account, in this folder below its own; a file with this ending is the
// catalog for Claude Code itself.
const (
	catalogDir    = "cache/model-catalog"
	catalogSuffix = "-cc.json"
)

// Claude Code compacts below the window: it keeps room for the answer
// (the model's output limit, at most 20,000 tokens; every model's limit is
// higher) and a buffer of 13,000 more. Its "% until auto-compact" counts
// down to that point.
const (
	outputReserve int64 = 20_000
	compactBuffer int64 = 13_000
)

// managedSettings is the settings file an administrator sets; it wins over
// every other.
var managedSettings = "/etc/claude-code/managed-settings.json"

// Context reads the tokens of the main agent's last model call (input,
// cache written and cache read: what the next call sends again) or, after
// a compaction, the tokens it left. The limit is where Claude Code
// compacts: its autoCompactWindow setting, capped by the context window,
// less the output reserve and the buffer. The window is the one Claude Code
// gave its status line command, else what the model catalog gives that
// call's model.
func (Claude) Context(transcript, cwd, start string, window int64, getenv func(string) string) (used, limit int64, ok bool) {
	used, model, ok := lastContext(tailLines(transcript, contextTail))
	if !ok {
		return 0, 0, false
	}
	if window <= 0 {
		window = modelWindow(model, getenv)
	}
	return used, contextLimit(projectOf(cwd, start, getenv), window, getenv), true
}

// ReadStatus reads the input Claude Code gives its status line command:
// context_window_size is the window it counts the session's model with, an
// opt-in to the long window and what it holds the account to included.
func (Claude) ReadStatus(input []byte) (agent.Status, bool) {
	var in struct {
		Session string `json:"session_id"`
		Context struct {
			Size int64 `json:"context_window_size"`
		} `json:"context_window"`
		Workspace struct {
			Project string `json:"project_dir"`
		} `json:"workspace"`
	}
	if json.Unmarshal(input, &in) != nil || in.Session == "" || in.Context.Size <= 0 {
		return agent.Status{}, false
	}
	return agent.Status{Session: in.Session, Window: in.Context.Size, Project: in.Workspace.Project}, true
}

// projectOf is the folder Claude Code started in, which it gives its hooks
// as CLAUDE_PROJECT_DIR; a watcher knows it as the folder of the agent's
// process, and the session's cwd, which follows cd and worktrees, stands
// in only without either.
func projectOf(cwd, start string, getenv func(string) string) string {
	return or(getenv("CLAUDE_PROJECT_DIR"), or(start, cwd))
}

// lastContext finds, from the end, the main agent's last model call or
// compaction, and the model of that call; after a compaction, of the last
// call before it.
func lastContext(lines [][]byte) (used int64, model string, ok bool) {
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
				if !ok {
					used = n
				}
				return used, e.Message.Model, true
			}
			// A message Claude Code wrote itself (an API error, an
			// interrupt): no model call, so look further back.
		case e.Type == "system" && e.Subtype == "compact_boundary" && !ok:
			if e.Compact == nil || e.Compact.Post <= 0 {
				// What was before the compaction no longer counts.
				return 0, "", false
			}
			used, ok = e.Compact.Post, true
		}
	}
	return used, "", ok
}

// contextLimit reads the limit from Claude Code's settings, the first file
// that sets a value winning: managed, the project's local and shared
// files, the user's.
func contextLimit(project string, window int64, getenv func(string) string) int64 {
	for _, path := range settingsFiles(project, getenv) {
		var s struct {
			AutoCompactWindow *int64 `json:"autoCompactWindow"`
		}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &s) != nil {
			continue // missing or unreadable: the next file decides
		}
		if s.AutoCompactWindow != nil && *s.AutoCompactWindow > 0 {
			window = min(window, *s.AutoCompactWindow)
			break
		}
	}
	return window - outputReserve - compactBuffer
}

// modelWindow is the window Claude Code's model catalog gives a model, from
// the newest catalog that lists it. The catalog says the most the model
// takes: Claude Code can count with less (a model whose long window is an
// opt-in, an account it holds to less), which only its status line tells.
func modelWindow(model string, getenv func(string) string) int64 {
	dir := filepath.Join(configDir(getenv), catalogDir)
	if !filepath.IsAbs(dir) {
		return contextWindow
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return contextWindow // no catalog: an older Claude Code, or none fetched yet
	}
	window, newest := contextWindow, int64(-1)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), catalogSuffix) {
			continue
		}
		var c struct {
			FetchedAt int64 `json:"fetchedAt"`
			Catalog   struct {
				Config struct {
					Models []struct {
						ID      string `json:"id"`
						Runtime struct {
							MaxInput int64 `json:"max_input_tokens"`
						} `json:"runtime"`
					} `json:"models"`
				} `json:"config"`
			} `json:"catalog"`
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || json.Unmarshal(data, &c) != nil || c.FetchedAt <= newest {
			continue // unreadable or older: another catalog decides
		}
		for _, m := range c.Catalog.Config.Models {
			if m.ID == model && m.Runtime.MaxInput > 0 {
				window, newest = m.Runtime.MaxInput, c.FetchedAt
			}
		}
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
