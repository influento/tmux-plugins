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
	SelectionActive bool
	InCopyMode      bool
	Content         []string // lines of visible pane text
}

func capturePaneState() (*PaneState, error) {
	// Get pane info in one tmux display-message call.
	// Format: pane_id|pane_tty|cursor_x|cursor_y|scroll_position|pane_width|pane_height|alternate_on|selection_active|pane_in_mode
	format := "#{pane_id}|#{pane_tty}|#{cursor_x}|#{cursor_y}|#{scroll_position}|#{pane_width}|#{pane_height}|#{alternate_on}|#{selection_active}|#{pane_in_mode}"
	out, err := tmuxCmd("display-message", "-p", format)
	if err != nil {
		return nil, fmt.Errorf("display-message: %w", err)
	}

	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 10 {
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
	ps.SelectionActive = parts[8] == "1"
	ps.InCopyMode = parts[9] == "1"

	// Capture visible pane content (plain text, no ANSI).
	content, err := tmuxCmd("capture-pane", "-p", "-t", ps.PaneID)
	if err != nil {
		return nil, fmt.Errorf("capture-pane: %w", err)
	}

	ps.Content = strings.Split(content, "\n")
	// Trim to pane height — capture-pane may include trailing empty lines.
	if len(ps.Content) > ps.PaneHeight {
		ps.Content = ps.Content[:ps.PaneHeight]
	}

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

func enterCopyMode(paneID string) error {
	_, err := tmuxCmd("copy-mode", "-t", paneID)
	return err
}

func sendKeys(paneID string, keys ...string) error {
	for _, key := range keys {
		args := []string{"send-keys", "-X", "-t", paneID, key}
		if _, err := tmuxCmd(args...); err != nil {
			return fmt.Errorf("send-keys %s: %w", key, err)
		}
	}
	return nil
}

func jumpToPosition(ps *PaneState, targetX, targetY int) error {
	if !ps.InCopyMode {
		if err := enterCopyMode(ps.PaneID); err != nil {
			return err
		}
	}

	// Navigate to target: first go to top-left, then move to target position.
	keys := []string{"top-line", "start-of-line"}

	// Move down to target row.
	for i := 0; i < targetY; i++ {
		keys = append(keys, "cursor-down")
	}

	// Move right to target column.
	for i := 0; i < targetX; i++ {
		keys = append(keys, "cursor-right")
	}

	return sendKeys(ps.PaneID, keys...)
}
