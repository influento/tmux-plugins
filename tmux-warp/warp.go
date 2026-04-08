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

	renderer.RenderPane(ps.Content, nil, 0, ps.PaneWidth, ps.PaneHeight)
	renderer.RenderStatus("", 0, ps.PaneHeight)

	for {
		ch, ok := term.ReadChar()
		if !ok {
			cleanup()
			return nil
		}

		if IsCancel(ch) {
			cleanup()
			return nil
		}

		if ch == 0x7f || ch == 0x08 {
			if len(query) > 0 {
				query = query[:len(query)-1]
			}
			if query == "" {
				renderer.RenderPane(ps.Content, nil, 0, ps.PaneWidth, ps.PaneHeight)
				renderer.RenderStatus("", 0, ps.PaneHeight)
				continue
			}
		} else if ch >= 0x20 && ch <= 0x7e {
			query += string(ch)
		} else {
			continue
		}

		positions := FindMatches(ps.Content, query)

		if len(positions) == 0 {
			renderer.RenderPane(ps.Content, nil, runeLen(query), ps.PaneWidth, ps.PaneHeight)
			renderer.RenderStatus(query, 0, ps.PaneHeight)
			continue
		}

		if len(positions) == 1 {
			cleanup()
			return jumpToPosition(ps, positions[0].Col, positions[0].Row)
		}

		matches := AssignLabels(positions, ps.CursorX, ps.CursorY, "")
		renderer.RenderPane(ps.Content, matches, runeLen(query), ps.PaneWidth, ps.PaneHeight)
		renderer.RenderStatus(query, len(matches), ps.PaneHeight)

		// When matches are few enough, switch to label selection.
		if len(matches) <= maxLabelsThreshold {
			target := readLabel(term, matches)
			if target != nil {
				cleanup()
				return jumpToPosition(ps, target.Pos.Col, target.Pos.Row)
			}
			cleanup()
			return nil
		}
	}
}

// readLabel reads one or two chars from the terminal and returns the matching
// Match, or nil if cancelled/timeout/no match.
func readLabel(term *RawTerminal, matches []Match) *Match {
	ch, ok := term.ReadChar()
	if !ok || IsCancel(ch) {
		return nil
	}

	// Try single-char label.
	if m := findMatchByLabel(matches, ch); m != nil {
		return m
	}

	// Try two-char label.
	ch2, ok := term.ReadChar()
	if !ok || IsCancel(ch2) {
		return nil
	}
	twoChar := string(ch) + string(ch2)
	for i := range matches {
		if matches[i].Label == twoChar {
			return &matches[i]
		}
	}

	return nil
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

	renderer.RenderPane(ps.Content, nil, 0, ps.PaneWidth, ps.PaneHeight)
	renderer.RenderStatus(fmt.Sprintf("[%s] type a char", mode), 0, ps.PaneHeight)

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
	renderer.RenderStatus(fmt.Sprintf("[%s] %c -- press label", mode, ch), len(matches), ps.PaneHeight)

	target := readLabel(term, matches)
	if target != nil {
		adjusted := adjustTarget(target.Pos)
		cleanup()
		return jumpToPosition(ps, adjusted.Col, adjusted.Row)
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
