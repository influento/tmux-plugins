package main

import (
	"reflect"
	"testing"
)

func TestFindMatches(t *testing.T) {
	tests := []struct {
		name    string
		content []string
		query   string
		want    []Position
	}{
		{"single ascii", []string{"hello world"}, "world", []Position{{Row: 0, Col: 6}}},
		{"case insensitive", []string{"Hello HELLO hello"}, "hello",
			[]Position{{Row: 0, Col: 0}, {Row: 0, Col: 6}, {Row: 0, Col: 12}}},
		{"overlapping", []string{"aaa"}, "aa", []Position{{Row: 0, Col: 0}, {Row: 0, Col: 1}}},
		{"multi row", []string{"foo", "bar foo"}, "foo",
			[]Position{{Row: 0, Col: 0}, {Row: 1, Col: 4}}},
		{"no match", []string{"abc"}, "xyz", nil},
		{"empty query", []string{"abc"}, "", nil},
		// Regression: İ (U+0130) lower-cases to a shorter byte sequence, so the
		// byte offset must be counted against the lower-cased line. 'b' is at
		// rune column 2; the pre-fix code reported 1.
		{"unicode case-fold column", []string{"İab"}, "b", []Position{{Row: 0, Col: 2}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FindMatches(tc.content, tc.query)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("FindMatches(%q, %q) = %v, want %v", tc.content, tc.query, got, tc.want)
			}
		})
	}
}

func TestGenerateLabels(t *testing.T) {
	keys := []byte("jfhgkdlsa") // 9 keys
	cases := []struct {
		n       int
		wantLen int // expected uniform label length (0 = no labels)
	}{
		{0, 0}, {1, 1}, {9, 1}, {10, 2}, {81, 2}, {82, 3},
	}
	for _, c := range cases {
		labels := generateLabels(c.n, keys)
		if len(labels) != c.n {
			t.Errorf("n=%d: got %d labels, want %d", c.n, len(labels), c.n)
			continue
		}
		seen := map[string]bool{}
		for _, lab := range labels {
			if len(lab) != c.wantLen {
				t.Errorf("n=%d: label %q has len %d, want uniform %d", c.n, lab, len(lab), c.wantLen)
			}
			if seen[lab] {
				t.Errorf("n=%d: duplicate label %q", c.n, lab)
			}
			seen[lab] = true
			for i := 0; i < len(lab); i++ {
				if !containsByte(keys, lab[i]) {
					t.Errorf("n=%d: label %q uses non-key byte %q", c.n, lab, lab[i])
				}
			}
		}
	}
}

func TestGenerateLabelsNoKeys(t *testing.T) {
	if got := generateLabels(5, nil); got != nil {
		t.Errorf("generateLabels with no keys = %v, want nil", got)
	}
}

func TestAssignLabelsDistanceOrder(t *testing.T) {
	// Cursor at origin; matches given out of distance order.
	positions := []Position{
		{Row: 10, Col: 10}, // farthest (~14.1)
		{Row: 0, Col: 1},   // nearest (1)
		{Row: 0, Col: 5},   // middle (5)
	}
	matches := AssignLabels(positions, 0, 0)
	if len(matches) != 3 {
		t.Fatalf("got %d matches, want 3", len(matches))
	}
	if matches[0].Pos != (Position{Row: 0, Col: 1}) {
		t.Errorf("nearest = %v, want {0,1}", matches[0].Pos)
	}
	if matches[2].Pos != (Position{Row: 10, Col: 10}) {
		t.Errorf("farthest = %v, want {10,10}", matches[2].Pos)
	}
	// Nearest match gets the first label key.
	if matches[0].Label != string(labelKeys[0]) {
		t.Errorf("nearest label = %q, want %q", matches[0].Label, string(labelKeys[0]))
	}
}

func TestAssignLabelsEmpty(t *testing.T) {
	if got := AssignLabels(nil, 0, 0); got != nil {
		t.Errorf("AssignLabels(nil) = %v, want nil", got)
	}
}

func containsByte(b []byte, c byte) bool {
	for _, x := range b {
		if x == c {
			return true
		}
	}
	return false
}
