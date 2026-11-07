package attention

import (
	"regexp"
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
		lines = append(lines, r.Session+"|"+r.Target+"|"+strings.TrimRight(escape.ReplaceAllString(r.Display, ""), " "))
	}
	want := []string{
		"proj-b|=proj-b:|proj-b",
		"|=proj-b:1|     1 beta                      idle · tests",
		"|=proj-b:2|  ?  2 gamma                     Bash(make) · 4m",
		"|=proj-b:10|    10 a window name longer tha  zsh",
		"proj-c|=proj-c:|proj-c",
		"|=proj-c:1|  ✗  1 delta                     failed: exit 1 · 1m",
		"proj-a|=proj-a:|proj-a",
		"|=proj-a:0|     0 alpha                     zsh",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}
