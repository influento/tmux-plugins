package attention

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
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
// the agent windows in index order (no index shown) with their flag, their
// agent's mark and the age (an idle agent's since its last flag), no
// reason; a split window's panes under it. The other windows fold into one
// row that counts them.
func TestRenderTree(t *testing.T) {
	check(t, render(Panel{View: TreeView}), []string{
		"=proj-b:|proj-b",
		"=proj-b:2| ? ✳  gamma",
		"=proj-b:2.0|   ● .0 ✳  agent-a                 5m",
		"=proj-b:2.1|   ? .1 ✳  agent-a                 4m",
		"=proj-b:3|   ◆  omega                        3h",
		"=proj-b:|   2 more windows",
		"=host-c:|host-c",
		"=host-c:1| ✗    delta                        1m",
		"=proj-a:|proj-a",
		"=proj-a:|   1 window",
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
// with its ✗) and Idle; no reason, no session.
func TestRenderState(t *testing.T) {
	check(t, render(Panel{View: StateView}), []string{
		"| NEEDS YOU · 1",
		"=proj-b:2.1| ? ✳  gamma                        4m",
		"| WORKING · 1",
		"=proj-b:2.0| ● ✳  gamma                        5m",
		"| DONE · 1",
		"=host-c:1.0| ✗    delta                        1m",
		"| IDLE · 1",
		"=proj-b:3.0|   ◆  omega                        3h",
	})
	check(t, render(Panel{View: StateView, Words: QueryWords("delta")}), []string{
		"| DONE · 1",
		"=host-c:1.0| ✗    delta                        1m",
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
		" ? ✳  gamma",
		"   ● .0 ✳  agent-a                 5m\n           ⎇ main · review",
		"   ? .1 ✳  agent-a                 4m",
		"   ◆  omega                        3h\n      ⎇ a-very-long-branch-name-for-a-…",
	})
	p.View = StateView
	for _, r := range RenderPickRows(rows, testSessions, p) {
		if r.Target == "=proj-b:2.0" {
			if got, want := escape.ReplaceAllString(r.Display(), ""), " ● ✳  gamma                        5m\n      ⎇ main · review"; got != want {
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
		"=proj-a:1.1| ● ✳  work                         9s\n      ⎇ main",
	})
}

// A row whose agent runs subagents shows how many after its name, in the
// working colour. The count is never cut: a name too long for its column
// takes a line of its own, and its columns go on the line below.
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
		" ● ✳  build-fix ⑂2                 3m",
		" ✓ ✳  a-very-long-window-name-here ⑂12\n                                   1m",
		"   ✳  docs                         2h",
	})
	p.View = StateView
	var state []string
	for _, r := range RenderPickRows(rows, nil, p) {
		state = append(state, escape.ReplaceAllString(r.Display(), ""))
	}
	if !slices.Contains(state, " ● ✳  build-fix ⑂2                 3m") {
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
		" ● ✳  python ⑂1 ✳2 ◆1              4m",
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

// itemPanes are one session's panes over two repositories: a task's
// worker, whose agent runs a review below it, and its demo window; a
// window of the same repository; one of another; one outside git.
func itemPanes() []Row {
	git := func(repo, branch string) Label {
		return Label{Source: "git", Text: "⎇ " + branch, Tokens: map[string]string{"repo": repo, "branch": branch}}
	}
	return []Row{
		{Target: "proj-a:4.0", Session: "proj-a", Window: "item-1-demo", State: "needs-you", Rank: 0, Age: "1m", Command: "agent-a", IsAgent: true,
			Role: "role-d", Task: "tool-a/item-1", TaskTitle: "item one", Labels: []Label{git("repo-x", "item-1")}},
		{Target: "proj-a:3.0", Session: "proj-a", Window: "item-1", State: "working", Rank: 2, Age: "5m", Command: "agent-a", IsAgent: true,
			Role: "role-w", Context: 40, Task: "tool-a/item-1", TaskTitle: "item one", Runs: []RoleRun{{Role: "role-r", Start: 1, Age: "2m"}},
			Labels: []Label{{Source: "tool-a", Text: "step 2"}, git("repo-x", "item-1")}},
		{Target: "proj-a:2.0", Session: "proj-a", Window: "docs", State: "working", Rank: 2, Age: "3m", Command: "agent-a", IsAgent: true,
			Labels: []Label{git("repo-y", "main")}},
		{Target: "proj-a:1.0", Session: "proj-a", Window: "notes", State: "done", Rank: 1, Age: "9m", Command: "agent-a", IsAgent: true},
		{Target: "proj-a:0.0", Session: "proj-a", Window: "fix", Rank: 3, Age: "-", Command: "agent-a", IsAgent: true, IdleAge: "2h",
			Role: "role-w", Labels: []Label{git("repo-x", "fix")}},
	}
}

func renderItems(t *testing.T, rows []Row, view View) []PickRow {
	t.Helper()
	return RenderPickRows(rows, []Session{{ID: "$1", Name: "proj-a"}}, Panel{View: view, Width: 39, Marks: testMarks})
}

func shown(rows []PickRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Target+"|"+escape.ReplaceAllString(r.Display(), ""))
	}
	return out
}

// A task's windows are one row, bold, named by its title: it stands for
// the pane that waits for the user, else the one that works, with that
// pane's role. A session's agents group by repository under rule lines,
// the item first in its repository, the agents outside git first. Line
// two starts with the branch.
func TestRenderItems(t *testing.T) {
	rows := renderItems(t, itemPanes(), TreeView)
	check(t, shown(rows), []string{
		"=proj-a:|proj-a",
		"=proj-a:1| ✓ ✳  notes                        9m",
		"=proj-a:|─ repo-x ─────────────────────────────",
		"=proj-a:4.0| ? ✳  item one    role-d           1m\n      ⎇ item-1",
		"=proj-a:0|   ✳  fix         role-w           2h\n      ⎇ fix",
		"=proj-a:|─ repo-y ─────────────────────────────",
		"=proj-a:2| ● ✳  docs                         3m\n      ⎇ main",
	})
	item := rows[3]
	if !strings.Contains(item.Text, "\x1b[1m") {
		t.Errorf("item row not bold: %q", item.Text)
	}
	if item.Kill != "=proj-a:4" || !slices.Equal(item.Also, []string{"=proj-a:3"}) {
		t.Errorf("item kill %q, also %q", item.Kill, item.Also)
	}
	if !rows[2].IsLabel() || !rows[5].IsLabel() {
		t.Error("a rule line is no place to go")
	}
}

// With no one waiting, the item shows the role that works: a review its
// agent runs below it shows as that role, with no context share of its
// own and its age since it started. A session with one repository has
// no rule line.
func TestRenderItemBackgroundRole(t *testing.T) {
	panes := itemPanes()
	panes[0].State, panes[0].Rank = "", 3
	check(t, shown(renderItems(t, panes[:2], TreeView)), []string{
		"=proj-a:|proj-a",
		"=proj-a:3.0| ● ✳  item one    role-r        –  2m\n      ⎇ item-1 · step 2",
	})
	panes[1].Runs = nil
	check(t, shown(renderItems(t, panes[:2], TreeView))[1:], []string{
		"=proj-a:3.0| ● ✳  item one    role-w      40%  5m\n      ⎇ item-1 · step 2",
	})
}

// By state, an item is one row under its flag's group, grouped by
// repository as in the tree.
func TestRenderItemsByState(t *testing.T) {
	check(t, shown(renderItems(t, itemPanes(), StateView)), []string{
		"| NEEDS YOU · 1",
		"=proj-a:4.0| ? ✳  item one    role-d           1m\n      ⎇ item-1",
		"| WORKING · 1",
		"=proj-a:2.0| ● ✳  docs                         3m\n      ⎇ main",
		"| DONE · 1",
		"=proj-a:1.0| ✓ ✳  notes                        9m",
		"| IDLE · 1",
		"=proj-a:0.0|   ✳  fix         role-w           2h\n      ⎇ fix",
	})
}

func TestParseRuns(t *testing.T) {
	got := parseRuns("role-r@100 role-s@x", 220)
	want := []RoleRun{{Role: "role-r", Start: 100, Age: "2m"}, {Role: "role-s"}}
	if !slices.Equal(got, want) {
		t.Errorf("parseRuns = %+v, want %+v", got, want)
	}
}

// openRows renders rows with the row whose key is key open.
func openRows(rows []Row, key string) []string {
	var out []string
	for _, r := range RenderPickRows(rows, []Session{{ID: "$1", Name: "proj-a"}}, Panel{View: TreeView, Width: 39, Marks: testMarks}) {
		r.Open = r.Key == key
		out = append(out, escape.ReplaceAllString(r.Display(), ""))
	}
	return out
}

// The cursor's row shows its whole name, broken after a '-' where the
// name has one, its columns on the line below; its labels whole, the
// lines after the first two columns further in. A row whose name and
// labels fit looks as it does closed; a closed row cuts them.
func TestOpenRow(t *testing.T) {
	rows := []Row{
		{Target: "proj-a:1.0", Pane: "%1", WindowID: "@1", Session: "proj-a", Window: "a-popup-closed-by-its-hotkey-leaves-the-client",
			State: "working", Rank: 2, Age: "3m", Command: "agent-a", IsAgent: true,
			Labels: []Label{{Source: "git", Text: "⎇ f-a-popup-closed-by-its-hotkey-leaves-the-client wt"}}},
		{Target: "proj-a:2.0", Pane: "%2", WindowID: "@2", Session: "proj-a", Window: "short-one",
			State: "working", Rank: 2, Age: "3m", Command: "agent-a", IsAgent: true,
			Labels: []Label{{Source: "git", Text: "⎇ main"}}},
	}
	long := []string{
		"proj-a",
		" ● ✳  a-popup-closed-by-its-hotkey-\n" +
			"      leaves-the-client\n" +
			strings.Repeat(" ", 35) + "3m\n" +
			"      ⎇ f-a-popup-closed-by-its-hotkey-\n" +
			"        leaves-the-client wt",
		" ● ✳  short-one                    3m\n      ⎇ main",
	}
	check(t, openRows(rows, "%1"), long)
	closed := []string{
		"proj-a",
		" ● ✳  a-popup-closed-by-its-hotkey-lea…\n" +
			strings.Repeat(" ", 35) + "3m\n" +
			"      ⎇ f-a-popup-closed-by-its-hotkey…",
		" ● ✳  short-one                    3m\n      ⎇ main",
	}
	check(t, openRows(rows, ""), closed)
	check(t, openRows(rows, "%2"), closed)
}

// A window without an agent opens too: its name whole, its command on
// the line below at the row's right edge.
func TestOpenRowWithoutAgent(t *testing.T) {
	rows := []Row{{Target: "proj-a:1.0", Pane: "%1", WindowID: "@1", Session: "proj-a", Window: "a-very-long-window-name-for-a-shell-here",
		Rank: 4, Age: "-", Command: "zsh"}}
	p := Panel{View: TreeView, Width: 39, Marks: testMarks, Open: []string{"$1"}}
	got := RenderPickRows(rows, []Session{{ID: "$1", Name: "proj-a"}}, p)[1]
	if want := "      a-very-long-window-name-for-… zsh"; escape.ReplaceAllString(got.Display(), "") != want {
		t.Errorf("closed: %q, want %q", escape.ReplaceAllString(got.Display(), ""), want)
	}
	got.Open = true
	want := "      a-very-long-window-name-for-a-\n      shell-here\n" + strings.Repeat(" ", 36) + "zsh"
	if escape.ReplaceAllString(got.Display(), "") != want {
		t.Errorf("open: %q, want %q", escape.ReplaceAllString(got.Display(), ""), want)
	}
}

// Each row has a key of its own that the panel's join leaves as it is: a
// pane's row its pane's id, a split window's row its window's id.
func TestRowKeys(t *testing.T) {
	rows := []Row{
		{Target: "proj-a:1.1", Pane: "%5", WindowID: "@3", Session: "proj-a", Window: "work", State: "working", Rank: 2, Age: "1m", Command: "agent-a", IsAgent: true},
		{Target: "proj-a:1.2", Pane: "%6", WindowID: "@3", Session: "proj-a", Window: "work", State: "working", Rank: 2, Age: "1m", Command: "agent-a", IsAgent: true},
		{Target: "proj-a:2.0", Pane: "%7", WindowID: "@4", Session: "proj-a", Window: "solo", State: "done", Rank: 1, Age: "1m", Command: "agent-a", IsAgent: true},
	}
	var got []string
	for _, r := range RenderPickRows(rows, []Session{{ID: "$1", Name: "proj-a"}}, Panel{View: TreeView, Width: 39}) {
		got = append(got, r.Target+"|"+r.Key)
	}
	check(t, got, []string{"=proj-a:|", "=proj-a:1|@3", "=proj-a:1.1|%5", "=proj-a:1.2|%6", "=proj-a:2|%7"})
}

// A task's row is keyed by the pane it stands for, the one that needs
// the user; its other panes' keys name it too.
func TestTaskRowKeys(t *testing.T) {
	rows := []Row{
		{Target: "proj-a:1.0", Pane: "%1", WindowID: "@1", Session: "proj-a", Window: "task-a", State: "working", Rank: 2, Age: "1m", Command: "agent-a", IsAgent: true, Task: "wb/t1"},
		{Target: "proj-a:2.0", Pane: "%2", WindowID: "@2", Session: "proj-a", Window: "task-b", State: "needs-you", Rank: 0, Age: "1m", Command: "agent-a", IsAgent: true, Task: "wb/t1"},
	}
	SortRows(rows)
	row := RenderPickRows(rows, []Session{{ID: "$1", Name: "proj-a"}}, Panel{View: TreeView, Width: 39})[1]
	if row.Key != "%2" || !row.Named("%1") || !row.Named("%2") || row.Named("") {
		t.Errorf("task row Key %q, Keys %q; want %%2 and %%1", row.Key, row.Keys)
	}
}

// A search word that the open row's name breaks across lines is
// underlined on each line it falls on, as on a cut row.
func TestOpenRowUnderlinesAcrossBreak(t *testing.T) {
	for _, c := range []struct {
		row  Row
		word string
	}{
		{Row{Target: "proj-a:1.0", Pane: "%1", WindowID: "@1", Session: "proj-a", Window: "a-popup-closed-by-its-hotkey-leaves-the-client",
			State: "working", Rank: 2, Age: "3m", Command: "agent-a", IsAgent: true}, "hotkey-leaves"},
		{Row{Target: "proj-a:1.0", Pane: "%1", WindowID: "@1", Session: "proj-a", Window: "a-very-long-window-name-for-a-shell-here",
			Rank: 4, Age: "-", Command: "zsh"}, "window-name-for-a-shell"},
	} {
		p := Panel{View: TreeView, Width: 39, Marks: testMarks, Open: []string{"$1"}, Words: []string{c.word}}
		got := RenderPickRows([]Row{c.row}, []Session{{ID: "$1", Name: "proj-a"}}, p)[1]
		got.Open = true
		d := got.Display()
		if strings.Contains(escape.ReplaceAllString(d, ""), c.word) {
			t.Fatalf("%q is on one line of the open row:\n%s", c.word, escape.ReplaceAllString(d, ""))
		}
		if u := underlined(d); u != c.word {
			t.Errorf("open row underlines %q, want %q:\n%q", u, c.word, d)
		}
	}
}

// underlined is the text of s between an underline's start and its end.
func underlined(s string) string {
	var b strings.Builder
	on := false
	for len(s) > 0 {
		if m := escape.FindStringIndex(s); m != nil && m[0] == 0 {
			switch code := s[:m[1]]; {
			case code == "\x1b[4m":
				on = true
			case code == "\x1b[24m" || strings.HasPrefix(code, "\x1b[0"):
				on = false
			}
			s = s[m[1]:]
			continue
		}
		r, n := utf8.DecodeRuneInString(s)
		if on {
			b.WriteRune(r)
		}
		s = s[n:]
	}
	return b.String()
}
