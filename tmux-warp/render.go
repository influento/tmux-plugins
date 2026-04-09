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

func (r *Renderer) write(s string) {
	r.ttyFile.WriteString(s)
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
	mmap := MatchMap(matches)

	// Build label overlay: label chars at match positions.
	type labelCell struct {
		ch    byte
		color string
	}
	labelOverlay := make(map[Position]labelCell)
	for i, m := range matches {
		if m.Label == "" {
			continue
		}
		color := labelColors[i%len(labelColors)]
		for j := 0; j < len(m.Label); j++ {
			pos := Position{Row: m.Pos.Row, Col: m.Pos.Col + j}
			labelOverlay[pos] = labelCell{ch: m.Label[j], color: color}
		}
	}

	var buf strings.Builder

	for row := 0; row < height; row++ {
		line := ""
		if row < len(content) {
			line = content[row]
		}
		runes := []rune(line)

		for col, ch := range runes {
			pos := Position{Row: row, Col: col}

			if lc, ok := labelOverlay[pos]; ok {
				buf.WriteString(lc.color)
				buf.WriteByte(lc.ch)
				continue
			}

			if m, isStart := mmap[pos]; isStart {
				buf.WriteString(colorHit)
				end := col + queryLen
				if end > len(runes) {
					end = len(runes)
				}
				for c := col; c < end; c++ {
					if _, hasLabel := labelOverlay[Position{Row: row, Col: c}]; hasLabel {
						break
					}
					buf.WriteRune(runes[c])
				}
				_ = m
				continue
			}

			// Check if this col is inside a match (not the start).
			insideMatch := false
			for _, m := range matches {
				if m.Pos.Row == row && col > m.Pos.Col && col < m.Pos.Col+queryLen {
					insideMatch = true
					break
				}
			}
			if insideMatch {
				continue
			}

			buf.WriteString(colorDim)
			buf.WriteRune(ch)
		}

		// Use \n\r for proper TTY line wrapping (like tmux-jump).
		if row < height-1 {
			buf.WriteString("\n\r")
		}
	}

	r.write("\x1b[H") // cursor home
	r.write(buf.String())
	r.write(colorRst)
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}
