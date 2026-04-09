package main

import (
	"fmt"
	"os"
	"strings"
)

func run(tmpFile string) error {
	ps, err := capturePaneState()
	if err != nil {
		return fmt.Errorf("capture pane: %w", err)
	}

	debugLog("paneID=%s tty=%s alt=%v copyMode=%v %dx%d cursor=%d,%d",
		ps.PaneID, ps.TTYPath, ps.AlternateScreen, ps.InCopyMode,
		ps.PaneWidth, ps.PaneHeight, ps.CursorX, ps.CursorY)

	// Read search query from the temp file (written by the shell wrapper's command-prompt).
	debugLog("waiting for query from %s", tmpFile)
	query, ok := readStringFromFile(tmpFile)
	if !ok || query == "" {
		return nil
	}
	os.Remove(tmpFile)

	// Cancel existing copy mode if active.
	if ps.InCopyMode {
		tmuxCmd("send-keys", "-X", "-t", ps.PaneID, "cancel")
	}

	// Capture pane content.
	content, err := captureContent(ps)
	if err != nil {
		return err
	}
	positions := FindMatches(content, query)

	debugLog("first char=%q matches=%d", query, len(positions))

	if len(positions) == 0 {
		return nil
	}
	if len(positions) == 1 {
		return jumpToPosition(ps, content, positions[0].Col, positions[0].Row)
	}

	// Show overlay with labels and prompt for selection.
	renderer, err := newRenderer(ps.TTYPath)
	if err != nil {
		return err
	}
	defer renderer.Close()

	return runOverlayLoop(renderer, ps, content, query, positions)
}

func runOverlayLoop(renderer *Renderer, ps *PaneState, content []string, query string, positions []Position) error {
	// Save/restore screen.
	var savedScreen string
	if ps.AlternateScreen {
		// Capture with ANSI colors for proper restore.
		out, err := tmuxCmd("capture-pane", "-ep", "-t", ps.PaneID)
		if err != nil {
			return fmt.Errorf("capture-pane colors: %w", err)
		}
		savedScreen = strings.TrimSuffix(out, "\n")
		renderer.write("\x1b[2J\x1b[H") // clear screen
	} else {
		renderer.EnterAltScreen()
		renderer.write("\x1b[H")
	}

	cleanup := func() {
		if ps.AlternateScreen {
			renderer.write(colorRst + "\x1b[2J")
			renderer.write(savedScreen)
			renderer.write(fmt.Sprintf("\x1b[%d;%dH", ps.CursorY+1, ps.CursorX+1))
			renderer.write(colorRst)
		} else {
			renderer.ExitAltScreen()
		}
	}

	// Render with labels and wait for label key.
	matches := AssignLabels(positions, ps.CursorX, ps.CursorY, "")
	renderer.RenderOverlay(content, matches, runeLen(query), ps.PaneHeight)

	maxLabelLen := 0
	for _, m := range matches {
		if len(m.Label) > maxLabelLen {
			maxLabelLen = len(m.Label)
		}
	}

	debugLog("showing %d labels (maxLen=%d), waiting for label key", len(matches), maxLabelLen)

	// Collect label chars one at a time, up to the max label length.
	var label string
	for len(label) < maxLabelLen {
		ch, ok := promptChar("label:")
		if !ok {
			cleanup()
			debugLog("label prompt timeout/cancel")
			return nil
		}
		label += string(ch)
		debugLog("label input so far: %q", label)

		// Exact match — jump immediately.
		if target := findMatchByLabel(matches, label); target != nil {
			cleanup()
			debugLog("jumping to row=%d col=%d", target.Pos.Row, target.Pos.Col)
			return jumpToPosition(ps, content, target.Pos.Col, target.Pos.Row)
		}

		// Check if any labels still have this prefix — if not, bail.
		hasPrefix := false
		for _, m := range matches {
			if len(m.Label) > len(label) && m.Label[:len(label)] == label {
				hasPrefix = true
				break
			}
		}
		if !hasPrefix {
			break
		}
	}

	cleanup()
	debugLog("no match for label %q", label)
	return nil
}

func findMatchByLabel(matches []Match, label string) *Match {
	for i := range matches {
		if matches[i].Label == label {
			return &matches[i]
		}
	}
	return nil
}

func captureContent(ps *PaneState) ([]string, error) {
	start := -ps.ScrollPosition
	end := start + ps.PaneHeight - 1
	out, err := tmuxCmd("capture-pane", "-p", "-t", ps.PaneID,
		"-S", fmt.Sprintf("%d", start), "-E", fmt.Sprintf("%d", end))
	if err != nil {
		return nil, fmt.Errorf("capture-pane: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	return lines, nil
}
