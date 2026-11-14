package attention

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

var escape = regexp.MustCompile("\x1b\\[[0-9;]*m")

var testMarks = map[string]AgentMark{"agent-a": {Glyph: "✳", Colour: "#d97757"}}

// panelPanes is a server's panes as List returns them: best first.
func panelPanes() []Row {
	return []Row{
		{Target: "proj-b:2.1", Session: "proj-b", Window: "gamma", State: "needs-you", Rank: 0, Detail: "permission", Age: "4m", Command: "agent-a", IsAgent: true},
		{Target: "host-c:1.0", Session: "host-c", Window: "delta", State: "done", Rank: 1, Detail: "failed: exit 1", Age: "1m", Failed: true, Command: "zsh"},
		{Target: "proj-b:2.0", Session: "proj-b", Window: "gamma", State: "working", Rank: 2, Detail: "working", Age: "5m", Command: "agent-a", IsAgent: true},
		{Target: "proj-b:3.0", Session: "proj-b", Window: "omega", Rank: 3, Detail: "idle · tests", Age: "-", Command: "other", Agent: "other", IsAgent: true, IdleAge: "3h"},
		{Target: "proj-b:10.0", Session: "proj-b", Window: "notes", Rank: 4, Detail: "nvim", Age: "-", Command: "nvim"},
		{Target: "proj-a:0.0", Session: "proj-a", Window: "alpha", Rank: 4, Detail: "zsh", Age: "-", Command: "zsh"},
		{Target: "proj-b:1.0", Session: "proj-b", Window: "zsh", Rank: 4, Detail: "zsh", Age: "-", Command: "zsh"},
	}
}

var testSessions = []Session{{ID: "$1", Name: "proj-a"}, {ID: "$2", Name: "proj-b"}, {ID: "$3", Name: "host-c"}}

func render(p Panel) []string {
	if p.Width == 0 {
		p.Width = 39
	}
	p.Marks = testMarks
	var out []string
	for _, r := range RenderPickRows(panelPanes(), testSessions, p) {
		out = append(out, r.Target+"|"+escape.ReplaceAllString(r.Display(), ""))
	}
	return out
}

func check(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The tree: sessions with a flag first by their best rank. Under each,
// the agent windows in index order (no index shown) with their flag, their agent's mark
// and, at the right edge, a ? or ✓ row's reason and the age (a working
// row its age, an idle agent the time since its output); a split window's
// panes under it. The other windows fold into one row.
func TestRenderTree(t *testing.T) {
	check(t, render(Panel{View: TreeView}), []string{
		"=proj-b:|proj-b",
		"=proj-b:2| ? ✳  gamma             permission · 4m",
		"=proj-b:2.0|   ● .0 ✳  agent-a                   5m",
		"=proj-b:2.1|   ? .1 ✳  agent-a      permission · 4m",
		"=proj-b:3|   ◆  omega                          3h",
		"=proj-b:|   ┄  zsh · notes",
		"=host-c:|host-c",
		"=host-c:1| ✗    delta         failed: exit 1 · 1m",
		"=proj-a:|proj-a",
		"=proj-a:|   ┄  alpha",
	})
}

// A fold the user opened, and any search, list the session's other
// windows one row each, dim, after its agent windows. A search keeps the
// windows whose session or window name holds every word, case aside,
// under their session's name.
func TestRenderTreeUnfolds(t *testing.T) {
	check(t, render(Panel{View: TreeView, Open: []string{"$1"}})[8:], []string{
		"=proj-a:|proj-a",
		"=proj-a:0|      alpha                         zsh",
	})
	check(t, render(Panel{View: TreeView, Words: QueryWords("PROJ-B no")}), []string{
		"=proj-b:|proj-b",
		"=proj-b:10|      notes                        nvim",
	})
	check(t, render(Panel{View: TreeView, Words: QueryWords("alpha")}), []string{
		"=proj-a:|proj-a",
		"=proj-a:0|      alpha                         zsh",
	})
	if got := render(Panel{View: TreeView, Words: QueryWords("nothing")}); got != nil {
		t.Errorf("rows for no match: %q", got)
	}
}

// By state: only agents, under Needs you, Working, Done (a failed run
// with its ✗) and Idle, each with its session dim before the age.
func TestRenderState(t *testing.T) {
	check(t, render(Panel{View: StateView}), []string{
		"| NEEDS YOU · 1",
		"=proj-b:2.1| ? ✳  gamma      permission · proj-b 4m",
		"| WORKING · 1",
		"=proj-b:2.0| ● ✳  gamma                   proj-b 5m",
		"| DONE · 1",
		"=host-c:1.0| ✗    delta  failed: exit 1 · host-c 1m",
		"| IDLE · 1",
		"=proj-b:3.0|   ◆  omega                   proj-b 3h",
	})
	check(t, render(Panel{View: StateView, Words: QueryWords("delta")}), []string{
		"| DONE · 1",
		"=host-c:1.0| ✗    delta  failed: exit 1 · host-c 1m",
	})
	if got := RenderPickRows(nil, testSessions, Panel{View: StateView, Width: 39}); len(got) != 1 || !got[0].IsLabel() {
		t.Errorf("no agents: %+v, want one label", got)
	}
}

// A narrow row cuts the right part first, down to 14 characters of the
// name; then the name.
func TestLineCutsTheRightFirst(t *testing.T) {
	p := Panel{Width: 30}
	for _, tt := range []struct {
		name, right string
		context     int
		want        string
	}{
		{"a-long-window-name", "a reason too long · 4m", 0, " ? a-long-window… a reason to…"},
		{"short", "permission · 4m", 0, " ? short       permission · 4m"},
		{"a-very-long-window-name-indeed", "", 0, " ? a-very-long-window-name-in…"},
		{"short", "4m", 37, " ? short                37% 4m"},
		{"a-very-long-window-name-indeed", "", 96, " ? a-very-long-window-nam… 96%"},
	} {
		got := escape.ReplaceAllString(line(" ?", 2, tt.name, "", tt.right, "", Row{Context: tt.context}, p).Text, "")
		if got != tt.want {
			t.Errorf("line(%q, %q) = %q, want %q", tt.name, tt.right, got, tt.want)
		}
	}
}

// An agent's labels make a second line, under its name; with none the row
// keeps one line.
func TestLabelsLine(t *testing.T) {
	r := line(" ? ✳ ", 5, "gamma", "", "", "", Row{}, Panel{Width: 39})
	if strings.Contains(r.Display(), "\n") {
		t.Errorf("a row without labels has two lines: %q", r.Display())
	}
	r.Labels = []string{"⎇ main", "3/12"}
	got := escape.ReplaceAllString(r.Display(), "")
	if want := " ? ✳  gamma\n      ⎇ main · 3/12"; got != want {
		t.Errorf("Display = %q, want %q", got, want)
	}
}

// The name underlines each match of each word; a session's name is bold.
func TestDisplayHighlights(t *testing.T) {
	r := line("   1", 4, "dot dotfiles", "", "", "", Row{}, Panel{Width: 39, Words: []string{"DOT", "es"}})
	want := "   1 \x1b[4mdot\x1b[24m \x1b[4mdot\x1b[24mfil\x1b[4mes\x1b[24m"
	if r.Text != want {
		t.Errorf("Text = %q, want %q", r.Text, want)
	}
	rows := RenderPickRows(panelPanes(), testSessions, Panel{View: TreeView, Width: 39, Words: []string{"j-a"}})
	if got := rows[0].Text; got != "\x1b[1mpro\x1b[4mj-a\x1b[24m\x1b[0m" {
		t.Errorf("session Text = %q", got)
	}
}

// A pane's labels make its row's second line: on the window's row for a
// one-pane window, on the pane's row in a split one, and in the by-state
// view; cut to the row's width.
func TestRenderLabels(t *testing.T) {
	rows := panelPanes()
	rows[2].Labels = []Label{{Source: "git", Text: "⎇ main"}, {Source: "tool-a", Tokens: map[string]string{"stage": "review"}}}
	rows[3].Labels = []Label{{Source: "git", Text: "⎇ a-very-long-branch-name-for-a-test wt"}}
	p := Panel{View: TreeView, Width: 39, Marks: testMarks}
	var got []string
	for _, r := range RenderPickRows(rows, testSessions, p)[1:5] {
		got = append(got, escape.ReplaceAllString(r.Display(), ""))
	}
	check(t, got, []string{
		" ? ✳  gamma             permission · 4m",
		"   ● .0 ✳  agent-a                   5m\n           ⎇ main · review",
		"   ? .1 ✳  agent-a      permission · 4m",
		"   ◆  omega                          3h\n      ⎇ a-very-long-branch-name-for-a-…",
	})
	p.View = StateView
	for _, r := range RenderPickRows(rows, testSessions, p) {
		if r.Target == "=proj-b:2.0" {
			if got, want := escape.ReplaceAllString(r.Display(), ""), " ● ✳  gamma                   proj-b 5m\n      ⎇ main · review"; got != want {
				t.Errorf("by state: %q, want %q", got, want)
			}
		}
	}
}

// A split window with one agent pane and a shell is one row: it goes to
// the agent's pane and shows that pane's labels.
func TestOneAgentPaneIsOneRow(t *testing.T) {
	rows := []Row{
		{Target: "proj-a:1.1", Session: "proj-a", Window: "work", State: "working", Rank: 2, Age: "9s", Command: "agent-a", IsAgent: true,
			Labels: []Label{{Source: "git", Text: "⎇ main"}}},
		{Target: "proj-a:1.2", Session: "proj-a", Window: "work", Rank: 4, Detail: "zsh", Age: "-", Command: "zsh"},
	}
	var got []string
	for _, r := range RenderPickRows(rows, []Session{{ID: "$1", Name: "proj-a"}}, Panel{View: TreeView, Width: 39, Marks: testMarks}) {
		got = append(got, r.Target+"|"+escape.ReplaceAllString(r.Display(), ""))
	}
	check(t, got, []string{
		"=proj-a:|proj-a",
		"=proj-a:1.1| ● ✳  work                           9s\n      ⎇ main",
	})
}

// A row whose agent runs subagents shows how many after its name, in the
// working colour. The count is never cut: the right part and a long name
// give way to it, as they do to each other.
func TestRenderSubagentCount(t *testing.T) {
	rows := []Row{
		{Target: "proj-a:1.0", Session: "proj-a", Window: "build-fix", State: "working", Rank: 2, Age: "3m", Command: "agent-a", IsAgent: true, Subagents: 2},
		{Target: "proj-a:2.0", Session: "proj-a", Window: "a-very-long-window-name-here", State: "done", Rank: 1, Detail: "turn ended", Age: "1m", Command: "agent-a", IsAgent: true, Subagents: 12},
		{Target: "proj-a:3.0", Session: "proj-a", Window: "docs", Rank: 3, Age: "-", Command: "agent-a", IsAgent: true, IdleAge: "2h"},
	}
	p := Panel{Width: 39, Marks: testMarks, Col: Colours{Working: "#89b4fa"}}
	var got []string
	for _, r := range RenderPickRows(rows, []Session{{ID: "$1", Name: "proj-a"}}, p) {
		got = append(got, escape.ReplaceAllString(r.Display(), ""))
	}
	check(t, got, []string{
		"proj-a",
		" ● ✳  build-fix ⑂2                   3m",
		" ✓ ✳  a-very-long-w… ⑂12 turn ended · …",
		"   ✳  docs                           2h",
	})
	p.View = StateView
	var state []string
	for _, r := range RenderPickRows(rows, nil, p) {
		state = append(state, escape.ReplaceAllString(r.Display(), ""))
	}
	if !slices.Contains(state, " ● ✳  build-fix ⑂2            proj-a 3m") {
		t.Errorf("state view:\n%s", strings.Join(state, "\n"))
	}
	// Agents the agent started count apart, each kind by its mark.
	nested := Row{Target: "proj-a:4.0", Session: "proj-a", Window: "python", State: "working", Rank: 2, Age: "4m",
		Command: "agent-a", IsAgent: true, Subagents: 1, Nested: []string{"agent-a", "agent-a", "other"}}
	p.View = TreeView
	var withRuns []string
	for _, r := range RenderPickRows([]Row{nested}, []Session{{ID: "$1", Name: "proj-a"}}, p) {
		withRuns = append(withRuns, escape.ReplaceAllString(r.Display(), ""))
	}
	check(t, withRuns, []string{
		"proj-a",
		" ● ✳  python ⑂1 ✳2 ◆1                4m",
	})
	row := line(" ●", 2, "build-fix", "", "3m", "⑂2", Row{}, p)
	if !strings.Contains(row.Text, "\x1b[38;2;137;180;250m⑂2") {
		t.Errorf("count not in the working colour: %q", row.Text)
	}
}

// The context share is dim, then in the failed colour from 80%, in the
// needs-you colour from 95%; unknown shows nothing.
func TestGaugeOf(t *testing.T) {
	col := Colours{NeedsYou: "#f38ba8", Failed: "#fab387", Dim: "#6c7086"}
	for _, tt := range []struct {
		context      int
		text, colour string
	}{
		{0, "", ""}, {1, "1%", col.Dim}, {79, "79%", col.Dim}, {80, "80%", col.Failed},
		{94, "94%", col.Failed}, {95, "95%", col.NeedsYou}, {130, "130%", col.NeedsYou},
	} {
		if text, colour := gaugeOf(Row{Context: tt.context}, col); text != tt.text || colour != tt.colour {
			t.Errorf("gaugeOf(%d) = %q %q, want %q %q", tt.context, text, colour, tt.text, tt.colour)
		}
	}
}
