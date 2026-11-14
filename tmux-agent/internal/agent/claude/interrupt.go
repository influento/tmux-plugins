package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// interruptTail is how much of a transcript's end Interrupted reads: the
// last turn's closing lines are there.
const interruptTail = 256 << 10

// interruptMark starts the line Claude Code writes as the user's when the
// user cuts a turn short: "[Request interrupted by user]" for Esc, "… for
// tool use]" for a declined permission.
const interruptMark = "[Request interrupted by user"

// Interrupted reports when the transcript's last turn was cut short by the
// user: the last message, a subagent's left out, is the interrupt mark.
func (Claude) Interrupted(transcript string) (time.Time, bool) {
	lines := tailLines(transcript, interruptTail)
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Type      string    `json:"type"`
			Sidechain bool      `json:"isSidechain"`
			Meta      bool      `json:"isMeta"`
			Timestamp time.Time `json:"timestamp"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &e) != nil || (e.Type != "user" && e.Type != "assistant") || e.Sidechain || e.Meta {
			continue
		}
		if e.Type == "user" && strings.HasPrefix(firstText(e.Message.Content), interruptMark) && !e.Timestamp.IsZero() {
			return e.Timestamp, true
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// tailLines are the whole lines in the last n bytes of a transcript; none
// when it cannot be read.
func tailLines(transcript string, n int64) [][]byte {
	if transcript == "" {
		return nil
	}
	f, err := os.Open(transcript)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := max(0, st.Size()-n)
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	if off > 0 { // the first line is cut
		_, buf, _ = bytes.Cut(buf, []byte("\n"))
	}
	return bytes.Split(buf, []byte("\n"))
}

// firstText is a message's text: the content when it is a string, else its
// first text part.
func firstText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	for _, p := range parts {
		if p.Type == "text" {
			return p.Text
		}
	}
	return ""
}
