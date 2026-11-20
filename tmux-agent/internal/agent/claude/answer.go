package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

// answerTail is how much of a transcript's end Answers reads first; the
// whole transcript only when that holds fewer answers than asked for.
const answerTail = 8 << 20

// Answers are the texts of the last n messages that ended a turn: Claude
// Code writes each content block of a message as a line of its own, all
// with the message's id and stop reason. A message that ends a turn stops
// for any reason but a tool call. A subagent's lines are left out.
func (Claude) Answers(transcript string, n int) []agent.Answer {
	if transcript == "" || n < 1 {
		return nil
	}
	f, err := os.Open(transcript)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	if off := st.Size() - answerTail; off > 0 {
		r := bufio.NewReader(io.NewSectionReader(f, off, answerTail))
		_, _ = r.ReadBytes('\n') // the first line is cut
		if as := answers(r, n); len(as) == n {
			return as
		}
	}
	return answers(bufio.NewReader(io.NewSectionReader(f, 0, st.Size())), n)
}

// answers reads the last n answers from r.
func answers(r *bufio.Reader, n int) []agent.Answer {
	var (
		out  []agent.Answer
		id   string
		stop string
		at   time.Time
		text []string
	)
	flush := func() {
		if id != "" && stop != "" && stop != "tool_use" && len(text) > 0 {
			out = append(out, agent.Answer{Text: strings.Join(text, "\n\n"), At: at})
			if len(out) > n {
				out = out[1:]
			}
		}
		id, stop, text = "", "", nil
	}
	for {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"assistant"`)) {
			var e struct {
				Type      string    `json:"type"`
				Sidechain bool      `json:"isSidechain"`
				Timestamp time.Time `json:"timestamp"`
				Message   struct {
					ID         string `json:"id"`
					StopReason string `json:"stop_reason"`
					Content    []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &e) == nil && e.Type == "assistant" && !e.Sidechain && e.Message.ID != "" {
				if e.Message.ID != id {
					flush()
					id = e.Message.ID
				}
				stop, at = e.Message.StopReason, e.Timestamp
				for _, c := range e.Message.Content {
					if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
						text = append(text, c.Text)
					}
				}
			}
		}
		if err != nil {
			flush()
			return out
		}
	}
}
