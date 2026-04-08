package main

import (
	"fmt"
	"os"
)

// runSearch implements the incremental search mode (flash `s` behavior).
func runSearch(ps *PaneState) error {
	renderer, err := newRenderer(ps.TTYPath)
	if err != nil {
		return err
	}
	defer renderer.Close()

	term, err := openRawTerminal(ps.TTYPath)
	if err != nil {
		return err
	}
	defer term.Close()

	// Screen management: use alternate screen if not already in alternate mode.
	savedContent := ""
	if ps.AlternateScreen {
		savedContent = SaveScreen(ps)
	} else {
		renderer.EnterAltScreen()
	}
	renderer.HideCursor()

	cleanup := func() {
		renderer.ShowCursor()
		if ps.AlternateScreen {
			renderer.RestoreScreen(savedContent, ps.PaneWidth, ps.PaneHeight)
		} else {
			renderer.ExitAltScreen()
		}
	}

	query := ""
	labelMode := false
	var currentMatches []Match

	// Initial render: all text dimmed.
	renderer.RenderPane(ps.Content, nil, 0, ps.PaneWidth, ps.PaneHeight)
	renderer.RenderStatus("", 0, ps.PaneHeight)

	for {
		ch, ok := term.ReadChar()
		if !ok {
			// Timeout.
			cleanup()
			return nil
		}

		if IsCancel(ch) {
			cleanup()
			return nil
		}

		if labelMode {
			// User is pressing a label key to select a match.
			target := findMatchByLabel(currentMatches, ch)
			if target != nil {
				cleanup()
				return jumpToPosition(ps, target.Pos.Col, target.Pos.Row)
			}
			// Check for two-char label: accumulate and try again.
			firstChar := ch
			ch2, ok2 := term.ReadChar()
			if !ok2 || IsCancel(ch2) {
				cleanup()
				return nil
			}
			twoChar := string(firstChar) + string(ch2)
			for _, m := range currentMatches {
				if m.Label == twoChar {
					cleanup()
					return jumpToPosition(ps, m.Pos.Col, m.Pos.Row)
				}
			}
			// No match for label — cancel.
			cleanup()
			return nil
		}

		// Printable character: add to query.
		if ch >= 0x20 && ch <= 0x7e {
			query += string(ch)
		} else if ch == 0x7f || ch == 0x08 {
			// Backspace.
			if len(query) > 0 {
				query = query[:len(query)-1]
			}
			if query == "" {
				renderer.RenderPane(ps.Content, nil, 0, ps.PaneWidth, ps.PaneHeight)
				renderer.RenderStatus("", 0, ps.PaneHeight)
				continue
			}
		} else {
			continue
		}

		positions := FindMatches(ps.Content, query)

		if len(positions) == 0 {
			renderer.RenderPane(ps.Content, nil, len(query), ps.PaneWidth, ps.PaneHeight)
			renderer.RenderStatus(query, 0, ps.PaneHeight)
			continue
		}

		if len(positions) == 1 {
			// Auto-jump: only one match.
			cleanup()
			return jumpToPosition(ps, positions[0].Col, positions[0].Row)
		}

		// Assign labels. Skip the likely next search character to avoid misfires.
		matches := AssignLabels(positions, ps.CursorX, ps.CursorY, "")
		renderer.RenderPane(ps.Content, matches, len(query), ps.PaneWidth, ps.PaneHeight)
		renderer.RenderStatus(query, len(matches), ps.PaneHeight)

		// If few enough matches that all have labels, enter label mode.
		if len(matches) <= len(labelKeys) {
			labelMode = true
			currentMatches = matches
		}
	}
}

// runChar implements the char mode (flash f/F/t/T behavior).
func runChar(ps *PaneState, mode string) error {
	renderer, err := newRenderer(ps.TTYPath)
	if err != nil {
		return err
	}
	defer renderer.Close()

	term, err := openRawTerminal(ps.TTYPath)
	if err != nil {
		return err
	}
	defer term.Close()

	savedContent := ""
	if ps.AlternateScreen {
		savedContent = SaveScreen(ps)
	} else {
		renderer.EnterAltScreen()
	}
	renderer.HideCursor()

	cleanup := func() {
		renderer.ShowCursor()
		if ps.AlternateScreen {
			renderer.RestoreScreen(savedContent, ps.PaneWidth, ps.PaneHeight)
		} else {
			renderer.ExitAltScreen()
		}
	}

	// Show dimmed pane, waiting for the character to search for.
	renderer.RenderPane(ps.Content, nil, 0, ps.PaneWidth, ps.PaneHeight)
	renderer.RenderStatus(fmt.Sprintf("[%s] type a char", mode), 0, ps.PaneHeight)

	// Read the single search character.
	ch, ok := term.ReadChar()
	if !ok || IsCancel(ch) {
		cleanup()
		return nil
	}

	positions := FindCharMatches(ps.Content, ch)
	if len(positions) == 0 {
		cleanup()
		return nil
	}

	// Adjust for t/T modes.
	adjustTarget := func(pos Position) Position {
		switch mode {
		case "t":
			if pos.Col > 0 {
				pos.Col--
			}
		case "T":
			pos.Col++
		}
		return pos
	}

	if len(positions) == 1 {
		target := adjustTarget(positions[0])
		cleanup()
		return jumpToPosition(ps, target.Col, target.Row)
	}

	matches := AssignLabels(positions, ps.CursorX, ps.CursorY, "")
	renderer.RenderPane(ps.Content, matches, 1, ps.PaneWidth, ps.PaneHeight)
	renderer.RenderStatus(fmt.Sprintf("[%s] %c — press label", mode, ch), len(matches), ps.PaneHeight)

	// Read label key(s).
	lch, ok := term.ReadChar()
	if !ok || IsCancel(lch) {
		cleanup()
		return nil
	}

	target := findMatchByLabel(matches, lch)
	if target != nil {
		adjusted := adjustTarget(target.Pos)
		cleanup()
		return jumpToPosition(ps, adjusted.Col, adjusted.Row)
	}

	// Try two-char label.
	lch2, ok := term.ReadChar()
	if !ok || IsCancel(lch2) {
		cleanup()
		return nil
	}
	twoChar := string(lch) + string(lch2)
	for _, m := range matches {
		if m.Label == twoChar {
			adjusted := adjustTarget(m.Pos)
			cleanup()
			return jumpToPosition(ps, adjusted.Col, adjusted.Row)
		}
	}

	cleanup()
	return nil
}

func findMatchByLabel(matches []Match, ch byte) *Match {
	key := string(ch)
	for i := range matches {
		if matches[i].Label == key {
			return &matches[i]
		}
	}
	return nil
}

func run() error {
	mode, charMode := parseFlags()

	ps, err := capturePaneState()
	if err != nil {
		return fmt.Errorf("capture pane: %w", err)
	}

	switch mode {
	case "search":
		return runSearch(ps)
	case "char":
		return runChar(ps, charMode)
	default:
		return fmt.Errorf("unknown mode: %s", mode)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "tmux-warp: %v\n", err)
	os.Exit(1)
}
