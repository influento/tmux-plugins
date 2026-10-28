package main

import (
	"strings"
	"testing"
	"unicode/utf8"
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

// cells decodes an overlay row into its visible runes and the colour each is
// drawn in: colorHit, colorDim, or "label" for any label colour.
func cells(t *testing.T, out string) (text string, styles []string) {
	t.Helper()
	// colorHit is also the green label colour; it decodes as "hit". The
	// cases use labels in other colours.
	known := map[string]string{}
	for _, c := range labelColors {
		known[c] = "label"
	}
	known[colorDim], known[colorHit], known[colorRst] = "dim", "hit", "rst"
	style := ""
	var b strings.Builder
	for i := 0; i < len(out); {
		if out[i] == '\x1b' {
			best := ""
			for seq := range known {
				if strings.HasPrefix(out[i:], seq) && len(seq) > len(best) {
					best = seq
				}
			}
			if best == "" {
				t.Fatalf("unknown escape at %d in %q", i, out)
			}
			style = known[best]
			i += len(best)
			continue
		}
		r, n := utf8.DecodeRuneInString(out[i:])
		b.WriteRune(r)
		styles = append(styles, style)
		i += n
	}
	return b.String(), styles
}

// The overlay keeps every column of the row: labels replace the head of
// their match, the rest of the match stays, highlighted, and the text after
// it stays in place.
func TestRenderOverlayKeepsEveryColumn(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		matches    []Match
		queryLen   int
		wantText   string
		wantStyles string // one letter per cell: l(abel), h(it), d(im)
	}{
		{"word query", "say hello world", []Match{{Pos: Position{Col: 4}, Label: "j"}}, 5,
			"say jello world", "ddddlhhhhdddddd"},
		{"two matches, two-char labels", "foo bar foo", []Match{
			{Pos: Position{Col: 0}, Label: "jj"}, {Pos: Position{Col: 8}, Label: "jf"}}, 3,
			"jjo bar jfo", "llhdddddllh"},
		{"overlapping matches", "aaa", []Match{
			{Pos: Position{Col: 0}, Label: "j"}, {Pos: Position{Col: 1}, Label: "f"}}, 2,
			"jfa", "llh"},
	}
	letter := map[string]byte{"label": 'l', "hit": 'h', "dim": 'd'}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, styles := cells(t, renderOverlayBuffer([]string{tc.line}, tc.matches, tc.queryLen, 1))
			var got []byte
			for _, s := range styles {
				got = append(got, letter[s])
			}
			if text != tc.wantText || string(got) != tc.wantStyles {
				t.Errorf("overlay = %q styles %q, want %q styles %q", text, got, tc.wantText, tc.wantStyles)
			}
		})
	}
}
