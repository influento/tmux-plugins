package ui

import "testing"

func TestPopupSize(t *testing.T) {
	rows := []string{"\x1b[38;2;1;2;3m? \x1b[0m" + "proj-a         alpha   beta", "proj-b         gamma"}
	cases := []struct {
		cw, ch, w, h int
	}{
		{200, 50, 67, 9}, // narrow rows: as wide as the header
		{30, 5, 26, 3},   // capped to the client
	}
	for _, c := range cases {
		if w, h := PopupSize(rows, c.cw, c.ch); w != c.w || h != c.h {
			t.Errorf("PopupSize in %dx%d = %dx%d, want %dx%d", c.cw, c.ch, w, h, c.w, c.h)
		}
	}
	wide := []string{"proj-a         " + string(make([]rune, 60))}
	if w, _ := PopupSize(wide, 200, 50); w != 85 {
		t.Errorf("wide row: width %d, want 85", w)
	}
}
