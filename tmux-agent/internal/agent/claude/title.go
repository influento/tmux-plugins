package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// A window title is the title Claude Code gives the session (its ai-title
// line), cut at a word. Until there is one, the first words of the first
// prompt stand in.
const (
	titleWords   = 3
	titleChars   = 15
	aiTitleChars = 24
)

// openers carry no meaning in three words: "can you fix" says less than
// "fix the waybar".
var openers = regexp.MustCompile(`(?i)^(?:(?:hey|hi|hello|ok|okay|so|now|please|pls|also|and|can you|could you|would you|will you|let'?s|i want you to|i need you to|i'd like you to)[ ,:]+)+`)

// maxTranscript bounds how much of a transcript the title reader reads; the
// first prompt and the ai-title come early.
const maxTranscript = 16 << 20

// Title names a window from a transcript: Claude Code's own title for the
// session, which is sure; else the first prompt's first words, a stand-in.
// Slash commands, the harness's own <tagged> messages and tool results are
// not prompts. It returns "" when there is no prompt yet, or no transcript.
func (Claude) Title(transcript string) (title string, sure bool) {
	if transcript == "" {
		return "", false
	}
	f, err := os.Open(transcript)
	if err != nil {
		return "", false
	}
	defer f.Close()
	r := bufio.NewReader(io.LimitReader(f, maxTranscript))
	stand := ""
	for {
		line, err := r.ReadBytes('\n')
		if t, ok := aiTitle(line); ok {
			return t, true
		}
		if stand == "" {
			if text, ok := promptText(line); ok {
				stand = titleFrom(text)
			}
		}
		if err != nil { // the end, or an unreadable transcript
			return stand, false
		}
	}
}

// aiTitle returns the title of an ai-title line, cut at a word.
func aiTitle(line []byte) (string, bool) {
	if !bytes.Contains(line, []byte(`"ai-title"`)) { // most lines: no parse
		return "", false
	}
	var entry struct {
		Type    string `json:"type"`
		AITitle string `json:"aiTitle"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "ai-title" {
		return "", false
	}
	t := cutAtWord(entry.AITitle, aiTitleChars)
	return t, t != ""
}

// cutAtWord cuts s to n characters at the last space that fits, or hard
// when its first word is longer, and trims trailing punctuation.
func cutAtWord(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	cut := n
	if r[n] != ' ' {
		for cut > 0 && r[cut] != ' ' {
			cut--
		}
		if cut == 0 {
			cut = n
		}
	}
	return strings.TrimRight(string(r[:cut]), " .,;:!?-")
}

// promptText returns the text of a transcript line that holds a prompt the
// user typed. Text Claude Code adds as the user's (a skill's body) is
// marked isMeta.
func promptText(line []byte) (string, bool) {
	var entry struct {
		Type    string `json:"type"`
		Meta    bool   `json:"isMeta"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "user" || entry.Meta {
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
