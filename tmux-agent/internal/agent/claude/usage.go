package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

// A transcript holds each model call of the main agent as one or more
// assistant lines with the call's message id and the same usage: one line
// per block of its answer, and a tool's result can come between them.
// Lines Claude Code writes itself carry the model <synthetic>. Subagents'
// calls are in their own files.

var _ agent.UsageReader = Claude{}

// recentIDs is how many call ids a read hands to the next: enough for the
// lines of a call that a read's end split.
const recentIDs = 64

// Transcripts lists every session's transcript in Claude Code's projects
// folder ($CLAUDE_CONFIG_DIR, else ~/.claude).
func (Claude) Transcripts(getenv func(string) string) ([]agent.SessionFiles, error) {
	names, err := filepath.Glob(filepath.Join(configDir(getenv), "projects", "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	out := make([]agent.SessionFiles, 0, len(names))
	for _, n := range names {
		out = append(out, agent.SessionFiles{Session: strings.TrimSuffix(filepath.Base(n), ".jsonl"), Transcript: n})
	}
	return out, nil
}

// configDir is Claude Code's folder: $CLAUDE_CONFIG_DIR, else ~/.claude.
func configDir(getenv func(string) string) string {
	if d := getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(getenv("HOME"), ".claude")
}

// usageLine is the part of a transcript line a tally reads.
type usageLine struct {
	Type      string `json:"type"`
	Sidechain bool   `json:"isSidechain"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input      int64 `json:"input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			Output     int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var cwdMark = []byte(`"cwd":"`)

// ReadUsage reads the main agent's model calls from byte offset from up to
// the transcript's last whole line; a line still being written waits for
// the next read. Each call counts once, by its message id.
func (Claude) ReadUsage(transcript string, from int64, recent []string) (agent.UsageRead, error) {
	read := agent.UsageRead{To: from, Recent: recent}
	f, err := os.Open(transcript)
	if err != nil {
		return read, err
	}
	defer f.Close()
	end, err := lastLineEnd(f, from)
	if err != nil || end <= from {
		return read, err
	}
	seen := map[string]bool{}
	for _, id := range recent {
		seen[id] = true
	}
	ids := slices.Clone(recent) // the caller's tally keeps its own
	r := bufio.NewReaderSize(io.NewSectionReader(f, from, end-from), 64<<10)
	err = eachLine(r, func(line []byte) {
		if read.Cwd == "" && bytes.Contains(line, cwdMark) {
			var c struct {
				Cwd        string `json:"cwd"`
				Entrypoint string `json:"entrypoint"`
			}
			if json.Unmarshal(line, &c) == nil {
				read.Cwd, read.OneShot = c.Cwd, c.Entrypoint == "sdk-cli"
			}
		}
		if !bytes.Contains(line, assistantMark) {
			return
		}
		var l usageLine
		if json.Unmarshal(line, &l) != nil || l.Type != "assistant" || l.Sidechain {
			return
		}
		u := l.Message.Usage
		if u == nil || l.Message.Model == "<synthetic>" {
			return
		}
		if id := l.Message.ID; id != "" {
			if seen[id] {
				return
			}
			seen[id] = true
			ids = append(ids, id)
		}
		at, err := time.Parse(time.RFC3339Nano, l.Timestamp)
		if err != nil {
			return
		}
		read.Calls = append(read.Calls, agent.Call{At: at, Model: l.Message.Model, Tokens: agent.Tokens{
			Input: u.Input, CacheWrite: u.CacheWrite, CacheRead: u.CacheRead, Output: u.Output,
		}})
	})
	if err != nil {
		return agent.UsageRead{To: from, Recent: recent}, err
	}
	if len(ids) > recentIDs {
		ids = ids[len(ids)-recentIDs:]
	}
	read.To, read.Recent = end, ids
	return read, nil
}

// scratchpad matches the folder Claude Code gives a session for its own
// files: <base>/<project>/<session id>/scratchpad, the project being the
// session's folder with each / as -.
var scratchpad = regexp.MustCompile(`/-[^/]*/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/scratchpad(?:/|$)`)

// RunOwner is the session whose scratchpad holds the folder, the
// innermost when one holds another.
func (Claude) RunOwner(cwd string) string {
	m := scratchpad.FindAllStringSubmatch(cwd, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

var _ agent.RunOwner = Claude{}

// lastLineEnd finds the offset just past the file's last newline at or
// after from; from when there is none.
func lastLineEnd(f *os.File, from int64) (int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return from, err
	}
	const block = 64 << 10
	buf := make([]byte, block)
	for end := fi.Size(); end > from; {
		start := max(end-block, from)
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && err != io.EOF {
			return from, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return from, nil
}
