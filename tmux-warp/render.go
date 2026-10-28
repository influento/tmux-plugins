package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// Catppuccin Mocha palette.
const (
	colorDim = "\x1b[0m\x1b[38;2;88;91;112m" // surface2 #585b70
	colorHit = "\x1b[1;38;2;166;227;161m"    // green #a6e3a1 bold
	colorRst = "\x1b[0m"
)

// Rainbow label colors (Catppuccin Mocha).
var labelColors = []string{
	"\x1b[1;38;2;243;139;168m", // red
	"\x1b[1;38;2;250;179;135m", // peach
	"\x1b[1;38;2;249;226;175m", // yellow
	"\x1b[1;38;2;166;227;161m", // green
	"\x1b[1;38;2;137;220;235m", // sky
	"\x1b[1;38;2;203;166;247m", // mauve
	"\x1b[1;38;2;245;194;231m", // pink
}

// Renderer writes ANSI-rendered pane content to a TTY.
type Renderer struct {
	ttyFile *os.File
}

func newRenderer(ttyPath string) (*Renderer, error) {
	f, err := os.OpenFile(ttyPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return nil, fmt.Errorf("open tty %s: %w", ttyPath, err)
	}
	return &Renderer{ttyFile: f}, nil
}

func (r *Renderer) Close() error {
	return r.ttyFile.Close()
}

// write draws on the pane's terminal. A failed write leaves a stale overlay
// at worst, so it is logged, not returned.
func (r *Renderer) write(s string) {
	if _, err := r.ttyFile.WriteString(s); err != nil {
		debugLog("write tty: %v", err)
	}
}

func (r *Renderer) EnterAltScreen() {
	r.write("\x1b[?1049h")
}

func (r *Renderer) ExitAltScreen() {
	r.write("\x1b[?1049l")
}

// RenderOverlay renders pane content with matches highlighted and labels overlaid.
// This follows tmux-jump's approach: write directly to TTY with \n\r line endings.
func (r *Renderer) RenderOverlay(content []string, matches []Match, queryLen int, height int) {
	r.write("\x1b[H") // cursor home
	r.write(renderOverlayBuffer(content, matches, queryLen, height))
	r.write(colorRst)
}

// renderOverlayBuffer builds the ANSI overlay string: content dimmed, matches
// highlighted, and labels drawn on top. Every column of a row is drawn once,
// so the text keeps its place: a label replaces the head of its match and the
// rest of the match stays. Label characters that extend past the end of their
// line are still emitted, so multi-char labels on matches near the right edge
// stay fully visible.
func renderOverlayBuffer(content []string, matches []Match, queryLen, height int) string {
	// Label characters at match positions, plus the furthest column a label
	// reaches on each row (so we can render past end-of-line).
	type labelCell struct {
		ch    byte
		color string
	}
	labelOverlay := make(map[Position]labelCell)
	labelRowEnd := make(map[int]int) // row -> exclusive max column reached by a label
	for i, m := range matches {
		if m.Label == "" {
			continue
		}
		color := labelColors[i%len(labelColors)]
		for j := 0; j < len(m.Label); j++ {
			pos := Position{Row: m.Pos.Row, Col: m.Pos.Col + j}
			labelOverlay[pos] = labelCell{ch: m.Label[j], color: color}
			if pos.Col+1 > labelRowEnd[pos.Row] {
				labelRowEnd[pos.Row] = pos.Col + 1
			}
		}
	}

	// Cells covered by a match, its first cell included.
	inMatch := make(map[Position]bool)
	for _, m := range matches {
		for c := range queryLen {
			inMatch[Position{Row: m.Pos.Row, Col: m.Pos.Col + c}] = true
		}
	}

	var buf strings.Builder
	color := "" // the colour last written, so runs of one colour share it
	setColor := func(c string) {
		if c != color {
			buf.WriteString(c)
			color = c
		}
	}

	for row := range height {
		line := ""
		if row < len(content) {
			line = content[row]
		}
		runes := []rune(line)

		// Render past end-of-line if a label's tail spills over.
		maxCol := max(len(runes), labelRowEnd[row])

		for col := range maxCol {
			pos := Position{Row: row, Col: col}
			lc, isLabel := labelOverlay[pos]
			switch {
			case isLabel:
				setColor(lc.color)
				buf.WriteByte(lc.ch)
			case col >= len(runes):
				// Past end-of-line but no label here: pad so columns stay aligned.
				buf.WriteByte(' ')
			case inMatch[pos]:
				setColor(colorHit)
				buf.WriteRune(runes[col])
			default:
				setColor(colorDim)
				buf.WriteRune(runes[col])
			}
		}

		// Use \n\r for proper TTY line wrapping (like tmux-jump).
		if row < height-1 {
			buf.WriteString("\n\r")
		}
	}

	return buf.String()
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}
