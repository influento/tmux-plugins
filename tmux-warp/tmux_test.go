package main

import "testing"

func TestComputeFlatOffset(t *testing.T) {
	content := []string{"abc", "de", "fghi"}
	tests := []struct {
		name    string
		targetX int
		targetY int
		content []string
		want    int
	}{
		{"origin", 0, 0, content, 0},
		// row 2, col 1 ('g'): (3+1) + (2+1) + 1
		{"mid", 1, 2, content, 8},
		// targetY past the end is clamped to len(content): (3+1)+(2+1)+(4+1)
		{"row past end", 0, 99, content, 12},
		// runes, not bytes: "é" is 2 bytes but 1 rune, +1 newline
		{"multibyte line", 0, 1, []string{"é", "x"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeFlatOffset(tc.content, tc.targetX, tc.targetY); got != tc.want {
				t.Errorf("computeFlatOffset(%q, %d, %d) = %d, want %d",
					tc.content, tc.targetX, tc.targetY, got, tc.want)
			}
		})
	}
}
