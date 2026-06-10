package main

import (
	"strings"
	"testing"
)

func TestRenderOverlayBufferLabelPastEOL(t *testing.T) {
	// "abcde" is 5 runes (cols 0-4). A match at the last column with a 2-char
	// label "jf" occupies cols 4 and 5 — col 5 is past end-of-line. Both label
	// characters must still appear (the pre-fix loop stopped at the line end and
	// dropped the tail). Content has no 'j'/'f' and the ANSI color codes are
	// digits-only, so these letters can only come from the label.
	content := []string{"abcde"}
	matches := []Match{{Pos: Position{Row: 0, Col: 4}, Label: "jf"}}

	out := renderOverlayBuffer(content, matches, 1, 1)

	if !strings.Contains(out, "j") {
		t.Errorf("label head 'j' missing: %q", out)
	}
	if !strings.Contains(out, "f") {
		t.Errorf("label tail 'f' (past end-of-line) missing: %q", out)
	}
}

func TestRenderOverlayBufferBasics(t *testing.T) {
	content := []string{"abcde"}
	matches := []Match{{Pos: Position{Row: 0, Col: 2}, Label: "j"}}

	out := renderOverlayBuffer(content, matches, 1, 1)

	if !strings.Contains(out, colorDim) {
		t.Errorf("expected dimmed content, got %q", out)
	}
	if !strings.Contains(out, "j") {
		t.Errorf("expected label 'j', got %q", out)
	}
	if strings.Contains(out, "\n") {
		t.Errorf("single-row overlay should have no line break: %q", out)
	}
}
