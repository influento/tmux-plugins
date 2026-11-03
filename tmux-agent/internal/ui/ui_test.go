package ui

import (
	"testing"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

func TestPopupSize(t *testing.T) {
	rows := []attention.SessionRow{
		{Display: "\x1b[38;2;1;2;3m? \x1b[0m" + "proj-a         alpha   beta"},
		{Display: "proj-b         gamma"},
	}
	cases := []struct {
		cw, ch, w, h int
	}{
		{200, 50, 40, 9}, // narrow rows: at least 40 wide
		{30, 5, 26, 3},   // capped to the client
	}
	for _, c := range cases {
		if w, h := PopupSize(rows, c.cw, c.ch); w != c.w || h != c.h {
			t.Errorf("PopupSize in %dx%d = %dx%d, want %dx%d", c.cw, c.ch, w, h, c.w, c.h)
		}
	}
	wide := []attention.SessionRow{{Display: "proj-a         " + string(make([]rune, 60))}}
	if w, _ := PopupSize(wide, 200, 50); w != 85 {
		t.Errorf("wide row: width %d, want 85", w)
	}
}
