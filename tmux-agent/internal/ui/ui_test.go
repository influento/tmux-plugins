package ui_test

import (
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/ui"
)

// The cursor row's tmux style becomes fzf's bg+ and fg+; what fzf would
// refuse is left out.
func TestCursorColors(t *testing.T) {
	for _, c := range []struct{ style, want string }{
		{"bg=#45475a,fg=#b4befe", "bg+:#45475a,fg+:#b4befe"},
		{"bg=#45475a fg=#b4befe", "bg+:#45475a,fg+:#b4befe"},
		{"fg=default", "fg+:-1"},
		{"bg=#45475a,bold,fg=red", "bg+:#45475a"},
		{"bg=#45475a)+abort(", ""},
		{"", ""},
	} {
		if got := ui.CursorColors(c.style); got != c.want {
			t.Errorf("CursorColors(%q) = %q, want %q", c.style, got, c.want)
		}
	}
}

// Up and down step past the labels next to a row in one move, and do not
// move at the list's ends; each names the row it lands on by its key.
func TestPickLinesSteps(t *testing.T) {
	rows := []attention.PickRow{
		{Label: "proj-a", Target: "=proj-a:"},
		{Target: "=proj-a:1", Key: "%1"},
		{Label: "repo-x", Target: "=proj-a:"},
		{Label: "proj-b", Target: "=proj-b:"},
		{Target: "=proj-b:2", Key: "%2"},
		{Target: "=proj-b:", Fold: "$2", Key: "$2"},
	}
	want := [][4]string{
		{"up", "", "down", ""},
		{"ignore", "%1", "down+down+down", "%2"},
		{"up", "", "down", ""},
		{"up", "", "down", ""},
		{"up+up+up", "%1", "down", "$2"},
		{"up", "%2", "ignore", "$2"},
	}
	for i, l := range ui.PickLines(rows) {
		f := strings.Split(l, "\t")
		if got := [4]string{f[5], f[6], f[7], f[8]}; got != want[i] {
			t.Errorf("row %d: %q, want %q", i, got, want[i])
		}
	}
}
