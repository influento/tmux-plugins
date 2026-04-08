package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// Catppuccin Mocha palette.
const (
	colorDim = "\x1b[38;2;88;91;112m"     // surface2 #585b70
	colorHit = "\x1b[1;38;2;166;227;161m" // green #a6e3a1 bold
	colorRst = "\x1b[0m"
)

// Rainbow label colors (Catppuccin Mocha).
var labelColors = []string{
	"\x1b[1;38;2;243;139;168m", // red #f38ba8
	"\x1b[1;38;2;250;179;135m", // peach #fab387
	"\x1b[1;38;2;249;226;175m", // yellow #f9e2af
	"\x1b[1;38;2;166;227;161m", // green #a6e3a1
	"\x1b[1;38;2;137;220;235m", // sky #89dceb
	"\x1b[1;38;2;203;166;247m", // mauve #cba6f7
	"\x1b[1;38;2;245;194;231m", // pink #f5c2e7
}

// Renderer writes ANSI-rendered pane content to a TTY.
type Renderer struct {
	ttyFile *os.File
}

func newRenderer(ttyPath string) (*Renderer, error) {
	f, err := os.OpenFile(ttyPath, os.O_WRONLY, 0)
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

// EnterAltScreen switches to the alternate screen buffer.
func (r *Renderer) EnterAltScreen() {
	r.write("\x1b[?1049h")
}

// ExitAltScreen restores the original screen buffer.
func (r *Renderer) ExitAltScreen() {
	r.write("\x1b[?1049l")
}

// HideCursor hides the terminal cursor.
func (r *Renderer) HideCursor() {
	r.write("\x1b[?25l")
}

// ShowCursor shows the terminal cursor.
func (r *Renderer) ShowCursor() {
	r.write("\x1b[?25h")
}

// SaveScreen captures the current screen content for later restoration.
func SaveScreen(ps *PaneState) string {
	return strings.Join(ps.Content, "\n")
}

// RestoreScreen writes back saved content to restore the display.
func (r *Renderer) RestoreScreen(saved string, width, height int) {
	r.write("\x1b[H")
	lines := strings.Split(saved, "\n")
	for i := 0; i < height; i++ {
		r.write("\x1b[K")
		if i < len(lines) {
			r.write(lines[i])
		}
		if i < height-1 {
			r.write("\n")
		}
	}
}

// RenderPane renders the pane content with matches highlighted and labels overlaid.
// queryLen is the rune-length of the search query.
func (r *Renderer) RenderPane(content []string, matches []Match, queryLen int, width, height int) {
	mmap := MatchMap(matches)

	matchCover := make(map[Position]bool)
	for _, m := range matches {
		for offset := 0; offset < queryLen; offset++ {
			matchCover[Position{Row: m.Pos.Row, Col: m.Pos.Col + offset}] = true
		}
	}

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

	r.write("\x1b[H")

	for row := 0; row < height; row++ {
		r.write("\x1b[K")
		line := ""
		if row < len(content) {
			line = content[row]
		}
		runes := []rune(line)
		runeCount := len(runes)

		col := 0
		for col < width {
			pos := Position{Row: row, Col: col}

			if lc, ok := labelOverlay[pos]; ok {
				r.write(lc.color + string(lc.ch) + colorRst)
				col++
				continue
			}

			if _, isStart := mmap[pos]; isStart {
				r.write(colorHit)
				end := pos.Col + queryLen
				if end > runeCount {
					end = runeCount
				}
				for col < end && col < width {
					if _, hasLabel := labelOverlay[Position{Row: row, Col: col}]; hasLabel {
						break
					}
					if col < runeCount {
						r.write(string(runes[col]))
					} else {
						r.write(" ")
					}
					col++
				}
				r.write(colorRst)
				continue
			}

			if matchCover[pos] {
				col++
				continue
			}

			if col < runeCount {
				ch := runes[col]
				if ch != ' ' {
					r.write(colorDim + string(ch) + colorRst)
				} else {
					r.write(" ")
				}
			}
			col++
		}

		if row < height-1 {
			r.write("\r\n")
		}
	}
}

// RenderStatus writes a status line at the bottom of the screen.
func (r *Renderer) RenderStatus(query string, matchCount int, height int) {
	r.write(fmt.Sprintf("\x1b[%d;1H\x1b[K", height))
	r.write(colorHit + " warp: " + colorRst)
	if query != "" {
		r.write(query)
	}
	r.write(colorDim + fmt.Sprintf("  [%d matches]", matchCount) + colorRst)
}

// runeLen returns the number of runes in s (used for query length).
func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}
