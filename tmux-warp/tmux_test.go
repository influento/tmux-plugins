package main

import "testing"

func TestJumpSteps(t *testing.T) {
	content := []string{"abc", "", "fghi"}
	tests := []struct {
		name      string
		content   []string
		x, y      int
		wantDown  int
		wantRight int
	}{
		{"origin", content, 0, 0, 0, 0},
		{"after an empty row", content, 1, 2, 2, 1},
		{"row past end keeps x", content, 3, 99, 99, 3},
		// runes, not bytes: "é" is 2 bytes but 1 rune
		{"multibyte", []string{"éx"}, 1, 0, 0, 1},
		// a combining mark shares a cell: "e" + U+0301 + "x" is 3 runes but
		// 2 grid cells; 'x' (rune col 2) is 1 step right
		{"combining mark", []string{"éx"}, 2, 0, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			down, right := jumpSteps(tc.content, tc.x, tc.y)
			if down != tc.wantDown || right != tc.wantRight {
				t.Errorf("jumpSteps(%q, %d, %d) = %d, %d; want %d, %d",
					tc.content, tc.x, tc.y, down, right, tc.wantDown, tc.wantRight)
			}
		})
	}
}
