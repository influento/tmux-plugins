package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/influento/tmux-plugins/internal/tmux"
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

// tmuxCmd runs one tmux command on the server this pane belongs to ($TMUX),
// with internal/tmux's timeout, and returns its stdout.
func tmuxCmd(args ...string) (string, error) {
	var c tmux.Client
	return c.Run(context.Background(), args...)
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

	// Down to the target row, then right to the target cell. Moving row by
	// row keeps line ends out of the count: how many cursor-right steps a line
	// break costs depends on mode-keys (vi stops on the last cell, emacs one
	// past it), so a single flat offset lands in the wrong column.
	down, right := jumpSteps(content, targetX, targetY)
	debugLog("jumpToPosition: down=%d right=%d", down, right)

	if down > 0 {
		cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "-N", fmt.Sprintf("%d", down), "cursor-down"})
	}
	cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "start-of-line"})
	if right > 0 {
		cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "-N", fmt.Sprintf("%d", right), "cursor-right"})
	}

	for _, args := range cmds {
		if _, err := tmuxCmd(args...); err != nil {
			return err
		}
	}

	return nil
}

// jumpSteps returns the cursor-down and cursor-right steps that reach
// (targetX, targetY) from the top-left of the captured content. targetX is a
// rune index into the row; copy mode moves one grid cell per step, and a grid
// cell holds one base rune plus any combining marks, so zero-width and
// combining runes share the previous cell and are not counted. Wide CJK and
// emoji runes occupy a single grid cell, so they count as one.
func jumpSteps(content []string, targetX, targetY int) (down, right int) {
	if targetY < len(content) {
		return targetY, cellCountPrefix(content[targetY], targetX)
	}
	return targetY, targetX
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
