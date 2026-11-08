package attention

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

var escape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// The picker: each session's name, then its windows in index order with
// the flag of their best pane; flagged sessions first, the rest in tmux's
// order.
func TestRenderPickRows(t *testing.T) {
	rows := []Row{ // as List returns them: best first
		{Target: "proj-b:2.1", Window: "gamma", State: "needs-you", Rank: 0, Detail: "Bash(make)", Age: "4m"},
		{Target: "proj-c:1.0", Window: "delta", State: "done", Rank: 1, Detail: "failed: exit 1", Age: "1m", Failed: true},
		{Target: "proj-b:2.0", Window: "gamma", State: "working", Rank: 2, Detail: "working", Age: "5m"},
		{Target: "proj-b:10.0", Window: "a window name longer than the column", Rank: 4, Detail: "zsh", Age: "-"},
		{Target: "proj-a:0.0", Window: "alpha", Rank: 4, Detail: "zsh", Age: "-"},
		{Target: "proj-b:1.0", Window: "beta", Rank: 3, Detail: "idle · tests", Age: "-"},
	}
	got := RenderPickRows(rows, []string{"proj-a", "proj-b", "proj-c"}, Colours{})
	var lines []string
	for _, r := range got {
		lines = append(lines, r.Session+"|"+r.Target+"|"+escape.ReplaceAllString(r.Display(nil)+"|"+r.Detail, ""))
	}
	want := []string{
		"proj-b|=proj-b:|proj-b|",
		"proj-b|=proj-b:1|   1 beta|idle · tests",
		"proj-b|=proj-b:2| ? 2 gamma|Bash(make) · 4m",
		"proj-b|=proj-b:2.0|   ● .0|working · 5m",
		"proj-b|=proj-b:2.1|   ? .1|Bash(make) · 4m",
		"proj-b|=proj-b:10|   10 a window name longer tha|zsh",
		"proj-c|=proj-c:|proj-c|",
		"proj-c|=proj-c:1| ✗ 1 delta|failed: exit 1 · 1m",
		"proj-a|=proj-a:|proj-a|",
		"proj-a|=proj-a:0|   0 alpha|zsh",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// A split window lists its panes under it, in index order, each with its
// own flag and what it runs or waits on; a window of one pane lists none.
func TestRenderPickRowsSplitWindow(t *testing.T) {
	rows := []Row{ // best first
		{Target: "proj-a:1.2", Window: "gamma", State: "needs-you", Rank: 0, Detail: "Bash(make)", Age: "2m"},
		{Target: "proj-a:1.0", Window: "gamma", Rank: 4, Detail: "nvim", Age: "-"},
		{Target: "proj-a:1.1", Window: "gamma", Rank: 4, Detail: "zsh", Age: "-"},
		{Target: "proj-a:2.0", Window: "delta", Rank: 4, Detail: "zsh", Age: "-"},
	}
	got := RenderPickRows(rows, []string{"proj-a"}, Colours{})
	var lines []string
	for _, r := range got {
		lines = append(lines, r.Target+"|"+escape.ReplaceAllString(r.Display(nil)+"|"+r.Detail, ""))
	}
	want := []string{
		"=proj-a:|proj-a|",
		"=proj-a:1| ? 1 gamma|Bash(make) · 2m",
		"=proj-a:1.0|     .0|nvim",
		"=proj-a:1.1|     .1|zsh",
		"=proj-a:1.2|   ? .2|Bash(make) · 2m",
		"=proj-a:2|   2 delta|zsh",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// Search keeps the windows whose session or window name holds every word,
// case aside, under their session's name line; a session's name keeps all
// its windows, and a window keeps its panes.
func TestFilterPickRows(t *testing.T) {
	rows := RenderPickRows([]Row{
		{Target: "proj-a:1.0", Window: "Alpha", Rank: 4, Detail: "zsh", Age: "-"},
		{Target: "proj-a:1.1", Window: "Alpha", Rank: 4, Detail: "nvim", Age: "-"},
		{Target: "proj-a:2.0", Window: "beta", Rank: 4, Detail: "alpha", Age: "-"},
		{Target: "host-b:1.0", Window: "gamma", Rank: 4, Detail: "zsh", Age: "-"},
		{Target: "host-b:2.0", Window: "alphabet", Rank: 4, Detail: "zsh", Age: "-"},
	}, []string{"proj-a", "host-b"}, Colours{})
	tests := []struct {
		query string
		want  []string // targets
	}{
		{"", []string{"=proj-a:", "=proj-a:1", "=proj-a:1.0", "=proj-a:1.1", "=proj-a:2", "=host-b:", "=host-b:1", "=host-b:2"}},
		{"alpha", []string{"=proj-a:", "=proj-a:1", "=proj-a:1.0", "=proj-a:1.1", "=host-b:", "=host-b:2"}}, // not by detail
		{"PROJ", []string{"=proj-a:", "=proj-a:1", "=proj-a:1.0", "=proj-a:1.1", "=proj-a:2"}},
		{"host gam", []string{"=host-b:", "=host-b:1"}},
		{"nothing", nil},
	}
	for _, tt := range tests {
		var got []string
		for _, r := range FilterPickRows(rows, QueryWords(tt.query)) {
			got = append(got, r.Target)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("query %q: %v, want %v", tt.query, got, tt.want)
		}
	}
}

// The name underlines each match of each word.
func TestDisplayHighlights(t *testing.T) {
	r := PickRow{Session: "proj-a", Window: "dot dotfiles", Target: "=proj-a:1", Lead: "   1", Name: "dot dotfiles"}
	want := "   1 \x1b[4mdot\x1b[24m \x1b[4mdot\x1b[24mfil\x1b[4mes\x1b[24m"
	if got := r.Display([]string{"DOT", "es"}); got != want {
		t.Errorf("Display = %q, want %q", got, want)
	}
	s := PickRow{Session: "proj-a", Target: "=proj-a:", Name: "proj-a"}
	if got := s.Display([]string{"j-a"}); got != "\x1b[1mpro\x1b[4mj-a\x1b[24m\x1b[0m" {
		t.Errorf("session Display = %q", got)
	}
}
