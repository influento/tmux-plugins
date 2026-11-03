package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
)

// A window title is the first words of the first prompt.
const (
	titleWords = 3
	titleChars = 15
)

// openers carry no meaning in three words: "can you fix" says less than
// "fix the waybar".
var openers = regexp.MustCompile(`(?i)^(?:(?:hey|hi|hello|ok|okay|so|now|please|pls|also|and|can you|could you|would you|will you|let'?s|i want you to|i need you to|i'd like you to)[ ,:]+)+`)

// maxTranscript bounds how much of a transcript the title reader reads; the
// first prompt comes early.
const maxTranscript = 64 << 20

// Title names a window from the first prompt in a transcript: slash
// commands, the harness's own <tagged> messages and tool results are not
// prompts. It returns "" when there is no prompt yet, or no transcript.
func (Claude) Title(transcript string) string {
	if transcript == "" {
		return ""
	}
	f, err := os.Open(transcript)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReader(io.LimitReader(f, maxTranscript))
	for {
		line, err := r.ReadBytes('\n')
		if text, ok := promptText(line); ok {
			return titleFrom(text)
		}
		if err != nil { // the end, or an unreadable transcript: no prompt
			return ""
		}
	}
}

// promptText returns the text of a transcript line that holds a prompt the
// user typed.
func promptText(line []byte) (string, bool) {
	var entry struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "user" {
		return "", false
	}
	var text string
	if err := json.Unmarshal(entry.Message.Content, &text); err != nil {
		var parts []map[string]any
		if json.Unmarshal(entry.Message.Content, &parts) != nil {
			return "", false
		}
		var texts []string
		for _, p := range parts {
			switch p["type"] {
			case "tool_result":
				return "", false
			case "text":
				s, _ := p["text"].(string) // a text part without text adds nothing
				texts = append(texts, s)
			}
		}
		text = strings.Join(texts, " ")
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" || text[0] == '<' || text[0] == '/' {
		return "", false
	}
	return text, true
}

// titleFrom cuts a prompt to a window name: openers dropped, three words,
// a hard cut at 15 characters, trailing punctuation trimmed.
func titleFrom(text string) string {
	text = openers.ReplaceAllString(text, "")
	// A heading's hashes go; the space after them must not start the name.
	words := strings.Split(strings.TrimLeft(strings.ReplaceAll(text, "#", ""), " "), " ")
	if len(words) > titleWords {
		words = words[:titleWords]
	}
	t := truncRunes(strings.Join(words, " "), titleChars)
	return strings.TrimRight(t, " .,;:!?-")
}
