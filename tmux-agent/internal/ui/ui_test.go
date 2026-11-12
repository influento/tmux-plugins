package ui_test

import (
	"testing"

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
