package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
)

// PaneState holds the captured state of the tmux pane.
type PaneState struct {
	PaneID          string
	TTYPath         string
	CursorX         int
	CursorY         int
	ScrollPosition  int
	PaneWidth       int
	PaneHeight      int
	AlternateScreen bool
	InCopyMode      bool
}

func capturePaneState() (*PaneState, error) {
	format := "#{pane_id}|#{pane_tty}|#{cursor_x}|#{cursor_y}|#{scroll_position}|#{pane_width}|#{pane_height}|#{alternate_on}|#{pane_in_mode}"
	out, err := tmuxCmd("display-message", "-p", format)
	if err != nil {
		return nil, fmt.Errorf("display-message: %w", err)
	}

	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 9 {
		return nil, fmt.Errorf("unexpected display-message output: %q", out)
	}

	ps := &PaneState{
		PaneID:  parts[0],
		TTYPath: parts[1],
	}

	ps.CursorX, err = strconv.Atoi(parts[2])
	if err != nil {
		return nil, fmt.Errorf("parse cursor_x: %w", err)
	}
	ps.CursorY, err = strconv.Atoi(parts[3])
	if err != nil {
		return nil, fmt.Errorf("parse cursor_y: %w", err)
	}
	if parts[4] != "" {
		ps.ScrollPosition, err = strconv.Atoi(parts[4])
		if err != nil {
			return nil, fmt.Errorf("parse scroll_position: %w", err)
		}
	}
	ps.PaneWidth, err = strconv.Atoi(parts[5])
	if err != nil {
		return nil, fmt.Errorf("parse pane_width: %w", err)
	}
	ps.PaneHeight, err = strconv.Atoi(parts[6])
	if err != nil {
		return nil, fmt.Errorf("parse pane_height: %w", err)
	}
	ps.AlternateScreen = parts[7] == "1"
	ps.InCopyMode = parts[8] == "1"

	return ps, nil
}

func tmuxCmd(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("tmux %s: %s", strings.Join(args, " "), string(exitErr.Stderr))
		}
		return "", err
	}
	return string(out), nil
}

func jumpToPosition(ps *PaneState, content []string, targetX, targetY int) error {
	debugLog("jumpToPosition: entering copy mode and navigating to %d,%d", targetX, targetY)

	if _, err := tmuxCmd("copy-mode", "-t", ps.PaneID); err != nil {
		return err
	}

	// Navigate: top-left first, then move to target.
	cmds := [][]string{
		{"send-keys", "-X", "-t", ps.PaneID, "start-of-line"},
		{"send-keys", "-X", "-t", ps.PaneID, "top-line"},
	}

	// Workaround for tmux quirk when first line is empty (from tmux-jump).
	cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "-N", "200", "cursor-right"})
	cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "start-of-line"})
	cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "top-line"})

	if ps.ScrollPosition > 0 {
		cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "-N", fmt.Sprintf("%d", ps.ScrollPosition), "cursor-up"})
	}

	// Compute flat offset from actual line lengths (like tmux-jump).
	// Each line in capture-pane output corresponds to one screen line,
	// and cursor-right wraps at end of content, not at PaneWidth.
	flatOffset := computeFlatOffset(content, targetX, targetY)

	debugLog("jumpToPosition: flatOffset=%d (from content lines)", flatOffset)

	if flatOffset > 0 {
		cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "-N", fmt.Sprintf("%d", flatOffset), "cursor-right"})
	}

	for _, args := range cmds {
		if _, err := tmuxCmd(args...); err != nil {
			return fmt.Errorf("tmux %v: %w", args, err)
		}
	}

	return nil
}

// computeFlatOffset returns the number of cursor-right steps to reach
// (targetX, targetY) from the top-left of the captured content. Copy-mode
// navigation moves one step per grid cell and one per line break (it wraps at
// the end of content, not at the pane width). A grid cell holds one base rune
// plus any combining marks, so zero-width/combining runes share the previous
// cell and must not be counted. Wide CJK/emoji runes occupy a single grid cell,
// so they count as one — matching plain rune counting; only decomposed
// combining sequences differ.
func computeFlatOffset(content []string, targetX, targetY int) int {
	offset := 0
	for i := 0; i < targetY && i < len(content); i++ {
		offset += cellCount(content[i]) + 1 // +1 for the newline
	}
	if targetY < len(content) {
		offset += cellCountPrefix(content[targetY], targetX)
	} else {
		offset += targetX
	}
	return offset
}

// cellCount counts grid cells in s (runes minus zero-width/combining marks).
func cellCount(s string) int {
	return cellCountPrefix(s, -1)
}

// cellCountPrefix counts grid cells among the first n runes of s. A negative n
// counts the whole string.
func cellCountPrefix(s string, n int) int {
	cells, i := 0, 0
	for _, r := range s {
		if n >= 0 && i >= n {
			break
		}
		if !isZeroWidth(r) {
			cells++
		}
		i++
	}
	return cells
}

// isZeroWidth reports whether r occupies no grid column of its own — combining
// marks (Unicode Mn/Me) and explicit zero-width characters merge into the
// preceding cell.
func isZeroWidth(r rune) bool {
	switch r {
	case '\u200b', '\u200c', '\u200d', '\ufeff': // ZWSP, ZWNJ, ZWJ, BOM
		return true
	}
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r)
}
