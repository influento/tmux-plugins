package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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

func jumpToPosition(ps *PaneState, targetX, targetY int) error {
	debugLog("jumpToPosition: entering copy mode and navigating to %d,%d", targetX, targetY)

	if _, err := tmuxCmd("copy-mode", "-t", ps.PaneID); err != nil {
		return err
	}

	// Navigate: top-left first, then move to target.
	// Use -N for bulk moves instead of one-at-a-time.
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

	// Move to target position using cursor-right with -N.
	target := targetY*ps.PaneWidth + targetX
	if target > 0 {
		cmds = append(cmds, []string{"send-keys", "-X", "-t", ps.PaneID, "-N", fmt.Sprintf("%d", target), "cursor-right"})
	}

	for _, args := range cmds {
		if _, err := tmuxCmd(args...); err != nil {
			return fmt.Errorf("tmux %v: %w", args, err)
		}
	}

	return nil
}
