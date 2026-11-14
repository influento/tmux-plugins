package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

// Claude Code keeps each subagent of a session next to the session's
// transcript: <session>/subagents/agent-<id>.jsonl, its full history, and
// agent-<id>.meta.json, its type, description, depth and whether it
// finished or the user stopped it. The history can run to gigabytes (it
// holds every request), so it is read a line at a time, and only the
// lines that matter are parsed.

var _ agent.SubagentReader = Claude{}

const (
	// maxLine is the longest history line read; longer ones (a request
	// with a whole file in it) carry nothing a record keeps.
	maxLine = 16 << 20
	// maxText bounds the prompt and the result a record keeps.
	maxText = 64 << 10
	// staleAfter is how long the files of a subagent that never said it
	// ended stay unwritten before it counts as over.
	staleAfter = time.Hour
)

// subagentsDir is the folder of a session's subagents.
func subagentsDir(transcript string) string {
	return filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents")
}

// SubagentIDs lists the subagents in a session's folder, by their meta
// files; Claude Code's hidden helpers have none.
func (Claude) SubagentIDs(transcript string) ([]string, error) {
	if transcript == "" {
		return nil, nil
	}
	names, err := filepath.Glob(filepath.Join(subagentsDir(transcript), "agent-*.meta.json"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(names))
	for _, n := range names {
		ids = append(ids, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(n), "agent-"), ".meta.json"))
	}
	return ids, nil
}

// SubagentSessions lists the sessions with a subagents folder, in Claude
// Code's projects folder ($CLAUDE_CONFIG_DIR, else ~/.claude).
func (Claude) SubagentSessions(getenv func(string) string) ([]agent.SessionFiles, error) {
	base := getenv("CLAUDE_CONFIG_DIR")
	if !filepath.IsAbs(base) {
		base = filepath.Join(getenv("HOME"), ".claude")
	}
	dirs, err := filepath.Glob(filepath.Join(base, "projects", "*", "*", "subagents"))
	if err != nil {
		return nil, err
	}
	out := make([]agent.SessionFiles, 0, len(dirs))
	for _, d := range dirs {
		session := filepath.Dir(d)
		out = append(out, agent.SessionFiles{Session: filepath.Base(session), Transcript: session + ".jsonl"})
	}
	return out, nil
}

// WatchedFiles are the session's subagent files: their meta files say
// when one ends.
func (Claude) WatchedFiles(transcript string) []string {
	return []string{subagentsDir(transcript)}
}

// SubagentEnded reads how a subagent ended from its meta file alone.
func (Claude) SubagentEnded(transcript, id string) agent.Outcome {
	data, err := os.ReadFile(filepath.Join(subagentsDir(transcript), "agent-"+id+".meta.json"))
	if err != nil {
		return ""
	}
	var m meta
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	switch {
	case m.Finished:
		return agent.OutcomeDone
	case m.StoppedByUser:
		return agent.OutcomeStopped
	}
	return ""
}

// meta is a subagent's meta file.
type meta struct {
	AgentType     string `json:"agentType"`
	Description   string `json:"description"`
	SpawnDepth    int    `json:"spawnDepth"`
	Finished      bool   `json:"finished"`
	StoppedByUser bool   `json:"stoppedByUser"`
}

// ReadSubagent reads a subagent's meta file and, once it ended, its
// history. One whose meta never says it ended counts as over, its outcome
// unknown, when its history has not been written for staleAfter.
func (c Claude) ReadSubagent(transcript, id string) (agent.Subagent, error) {
	base := filepath.Join(subagentsDir(transcript), "agent-"+id)
	data, err := os.ReadFile(base + ".meta.json")
	if err != nil {
		return agent.Subagent{}, err
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return agent.Subagent{}, err
	}
	sub := agent.Subagent{ID: id, Type: m.AgentType, Description: m.Description, Depth: m.SpawnDepth, Transcript: base + ".jsonl"}
	switch {
	case m.Finished:
		sub.Outcome = agent.OutcomeDone
	case m.StoppedByUser:
		sub.Outcome = agent.OutcomeStopped
	default:
		fi, err := os.Stat(sub.Transcript)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return sub, err
		}
		if err == nil && time.Since(fi.ModTime()) < staleAfter {
			return sub, nil // it still runs
		}
		sub.Outcome = agent.OutcomeUnknown
	}
	f, err := os.Open(sub.Transcript)
	if errors.Is(err, os.ErrNotExist) {
		return sub, nil // Claude Code deleted its history
	}
	if err != nil {
		return sub, err
	}
	defer f.Close()
	h, err := readHistory(f)
	if err != nil {
		return sub, err
	}
	sub.Prompt, sub.Result, sub.Model, sub.Tokens = h.prompt, h.result, h.model, &h.tokens
	sub.Started, sub.Ended = h.first, h.last
	return sub, nil
}

// history is what a subagent's history tells.
type history struct {
	prompt, result, model string
	first, last           time.Time
	tokens                agent.Tokens
}

// historyLine is the part of a history line a record keeps.
type historyLine struct {
	Type    string `json:"type"`
	Message struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input      int64 `json:"input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			Output     int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// block is one block of a message's content.
type block struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	Input struct {
		Message string `json:"message"`
	} `json:"input"`
}

var (
	assistantMark = []byte(`"type":"assistant"`)
	userMark      = []byte(`"type":"user"`)
	timeMark      = []byte(`"timestamp":"`)
)

// readHistory reads a subagent's history: its prompt (the first user
// message), its result (the message of its SubagentHandback call, else
// its last text), its model, its first and last times, and its tokens.
// Claude Code writes one model call as several lines with one message id
// and the same usage, so each id counts once.
func readHistory(r io.Reader) (history, error) {
	var h history
	seen := map[string]bool{}
	lastText := ""
	err := eachLine(bufio.NewReaderSize(r, 64<<10), func(line []byte) {
		if t, ok := lineTime(line); ok {
			if h.first.IsZero() {
				h.first = t
			}
			h.last = t
		}
		isAssistant := bytes.Contains(line, assistantMark)
		if !isAssistant && (h.prompt != "" || !bytes.Contains(line, userMark)) {
			return
		}
		var l historyLine
		if json.Unmarshal(line, &l) != nil {
			return
		}
		switch l.Type {
		case "user":
			if h.prompt == "" {
				h.prompt = cut(contentText(l.Message.Content), maxText)
			}
		case "assistant":
			if l.Message.Model != "" && !strings.HasPrefix(l.Message.Model, "<") {
				h.model = l.Message.Model
			}
			if u := l.Message.Usage; u != nil && !seen[l.Message.ID] {
				seen[l.Message.ID] = l.Message.ID != ""
				h.tokens.Input += u.Input
				h.tokens.CacheWrite += u.CacheWrite
				h.tokens.CacheRead += u.CacheRead
				h.tokens.Output += u.Output
			}
			var blocks []block
			_ = json.Unmarshal(l.Message.Content, &blocks) // no blocks: nothing to read
			for _, b := range blocks {
				switch {
				case b.Type == "tool_use" && b.Name == "SubagentHandback":
					h.result = cut(b.Input.Message, maxText)
				case b.Type == "text" && strings.TrimSpace(b.Text) != "":
					lastText = b.Text
				}
			}
		}
	})
	if h.result == "" {
		h.result = cut(lastText, maxText)
	}
	return h, err
}

// contentText is a message's text: the content when it is a string, else
// its text blocks.
func contentText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []block
	_ = json.Unmarshal(content, &blocks) // neither: no text
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// lineTime reads a line's timestamp without parsing the line.
func lineTime(line []byte) (time.Time, bool) {
	i := bytes.Index(line, timeMark)
	if i < 0 {
		return time.Time{}, false
	}
	rest := line[i+len(timeMark):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, string(rest[:j]))
	return t, err == nil
}

// eachLine calls fn with each line of r up to maxLine bytes; longer lines
// are skipped whole.
func eachLine(r *bufio.Reader, fn func(line []byte)) error {
	var long []byte
	skipping := false
	for {
		chunk, err := r.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			if !skipping {
				long = append(long, chunk...)
				if len(long) > maxLine {
					long, skipping = nil, true
				}
			}
			continue
		case skipping:
			skipping = false
		case len(long) > 0:
			if len(long)+len(chunk) <= maxLine {
				fn(append(long, chunk...))
			}
			long = long[:0]
		case len(chunk) > 0:
			fn(chunk)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// cut bounds a text to n bytes at a rune, with an ellipsis when it cuts.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
