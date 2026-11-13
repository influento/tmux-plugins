package attention

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// View is a layout of the panel's rows.
type View string

const (
	// TreeView lists each session's windows: agent windows first, the
	// others folded into one row. The panel opens in it.
	TreeView View = "tree"
	// StateView lists only agents, grouped by their flag.
	StateView View = "state"
)

// Views are the panel's views in the order Tab steps through them.
var Views = []View{TreeView, StateView}

// AgentMark is how the panel shows which agent runs in a window: a glyph in
// the agent's colour.
type AgentMark struct {
	Glyph  string
	Colour string // #rrggbb
}

// DefaultMark is the mark of an agent that has none of its own.
var DefaultMark = AgentMark{Glyph: "◆", Colour: "#cba6f7"}

// Session is a session as the panel needs it: tmux's id, which a fold
// row carries, and the name.
type Session struct {
	ID, Name string
}

// Panel is what the panel's rows depend on besides the panes.
type Panel struct {
	View  View
	Words []string // the search query's words; a search unfolds every session
	Open  []string // ids of the sessions whose fold the user opened
	Width int      // the columns of a row
	Col   Colours
	Marks map[string]AgentMark // by agent name or command
}

// SubagentGlyph marks a row's count of running subagents.
const SubagentGlyph = "⑂"

// runsTag is what runs below a row's agent, as the panel shows it after
// the name in the working colour: the count of its subagents, then of the
// agents it started, each kind by its mark (✳6); "" for none.
func runsTag(r Row, p Panel) string {
	var parts []string
	if r.Subagents > 0 {
		parts = append(parts, SubagentGlyph+strconv.Itoa(r.Subagents))
	}
	var kinds []string
	count := map[string]int{}
	for _, name := range r.Nested {
		if count[name] == 0 {
			kinds = append(kinds, name)
		}
		count[name]++
	}
	for _, name := range kinds {
		m, ok := p.Marks[name]
		if !ok {
			m = DefaultMark
		}
		parts = append(parts, m.Glyph+strconv.Itoa(count[name]))
	}
	return strings.Join(parts, " ")
}

// PickRow is one item of the panel: a label (a session's name, a group's
// title) the cursor steps over, a window, a pane of a split window, or a
// session's fold.
type PickRow struct {
	Label   string   // a label's text; "" on the other rows
	Session string   // the session the row is in
	Window  string   // the window's name
	Target  string   // what switch-client takes: "=name:", "=name:index" or "=name:index.pane"
	Kill    string   // what ctrl-x kills; "" for nothing
	Fold    string   // on a fold row, the id of the session it opens
	Text    string   // line one, with colour escapes
	Labels  []string // line two: the pane's labels; none, no line
	indent  int      // the column line two starts at
	width   int      // the columns of a row
	dimText string   // the dim colour escape, for line two
}

// IsLabel reports whether the row is a label, not a place to go.
func (r PickRow) IsLabel() bool { return r.Label != "" }

// Display is the row as the panel shows it: line one, and the labels on
// line two when there are any.
func (r PickRow) Display() string {
	if len(r.Labels) == 0 {
		return r.Text
	}
	text := truncEllipsis(strings.Join(r.Labels, " · "), max(r.width-r.indent, 1))
	return r.Text + "\n" + strings.Repeat(" ", r.indent) + r.dimText + text + "\x1b[0m"
}

// shownLabels are a row's labels as line two shows them.
func shownLabels(r Row) []string {
	var out []string
	for _, l := range r.Labels {
		if s := l.Shown(); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// paneOf is a pane's index in its window and its row.
type paneOf struct {
	index int
	row   Row
}

// window is one window of the tree, its panes best first.
type window struct {
	session string
	index   int
	panes   []paneOf
}

func (w *window) best() Row { return w.panes[0].row }

// hasAgent reports whether a pane of the window runs an agent, has a flag,
// or had one a sweep dropped: the window shows in full, not in the fold.
func (w *window) hasAgent() bool {
	return slices.ContainsFunc(w.panes, func(p paneOf) bool { return p.row.Rank < 4 })
}

// busyPanes are the window's panes that run an agent or have a flag.
func (w *window) busyPanes() []paneOf {
	var out []paneOf
	for _, p := range w.panes {
		if p.row.Rank < 4 {
			out = append(out, p)
		}
	}
	return out
}

// target is the window's switch-client target.
func (w *window) target() string { return "=" + w.session + ":" + strconv.Itoa(w.index) }

// RenderPickRows lays out the panel from List's rows (best first) and the
// sessions in tmux's order.
func RenderPickRows(rows []Row, sessions []Session, p Panel) []PickRow {
	if p.View == StateView {
		return renderState(rows, p)
	}
	return renderTree(rows, sessions, p)
}

// renderTree lists the sessions, those with a flag first by their best
// rank. Under each, its agent windows in index order, each with its
// flag, its agent's mark and, for a split window, its panes; then its
// other windows, folded into one row unless a search runs or the user
// opened the fold.
func renderTree(rows []Row, sessions []Session, p Panel) []PickRow {
	wins := map[string][]*window{}
	byKey := map[string]*window{}
	rank := map[string]int{}
	for _, r := range rows {
		sess, rest, _ := strings.Cut(r.Target, ":")
		idx, pidx, _ := strings.Cut(rest, ".")
		pi, _ := strconv.Atoi(pidx) // tmux prints numbers
		key := sess + ":" + idx
		w, ok := byKey[key]
		if !ok {
			i, _ := strconv.Atoi(idx)
			w = &window{session: sess, index: i}
			byKey[key] = w
			wins[sess] = append(wins[sess], w)
		}
		w.panes = append(w.panes, paneOf{pi, r}) // rows come best first
		if cur, ok := rank[sess]; r.State != "" && (!ok || r.Rank < cur) {
			rank[sess] = r.Rank
		}
	}
	rankOf := func(s string) int {
		if r, ok := rank[s]; ok {
			return r
		}
		return 9
	}
	ordered := slices.Clone(sessions)
	slices.SortStableFunc(ordered, func(a, b Session) int { return cmp.Compare(rankOf(a.Name), rankOf(b.Name)) })

	var out []PickRow
	for _, s := range ordered {
		ws := wins[s.Name]
		if len(ws) == 0 {
			continue
		}
		slices.SortFunc(ws, func(a, b *window) int { return cmp.Compare(a.index, b.index) })
		matches := func(w *window) bool { return matchesAll(s.Name+" "+w.best().Window, p.Words) }
		var body []PickRow
		var folded []*window
		for _, w := range ws {
			if !matches(w) {
				continue
			}
			if !w.hasAgent() {
				folded = append(folded, w)
				continue
			}
			body = append(body, windowRows(w, true, p)...)
		}
		if len(folded) > 0 {
			if len(p.Words) > 0 || slices.Contains(p.Open, s.ID) {
				for _, w := range folded {
					body = append(body, windowRows(w, false, p)...)
				}
			} else {
				body = append(body, foldRow(s, folded, p))
			}
		}
		if len(body) == 0 {
			continue
		}
		out = append(out, PickRow{Label: s.Name, Session: s.Name, Target: "=" + s.Name + ":",
			Text: "\x1b[1m" + highlight(s.Name, p.Words) + "\x1b[0m"})
		out = append(out, body...)
	}
	return out
}

// leadLen is the width of a window row's lead: its flag, then its
// agent's mark and a space, as a terminal may draw a mark wider than its
// cell.
const leadLen = 5

// windowRows is a window's row and, for a window with two or more panes
// that run an agent or have a flag, a row for each pane in index order; with
// one such pane, the window's row goes to it and shows its labels. An agent window shows its flag, its agent's mark
// and what the flag says; another window shows its name dim. Window
// indexes are left out: the names say more.
func windowRows(w *window, agent bool, p Panel) []PickRow {
	r := w.best()
	dim := ansiFg(p.Col.Dim)
	var row PickRow
	if agent {
		lead := " " + glyphOf(r, p.Col) + " " + markOfWindow(w, p) + " "
		row = line(lead, leadLen, r.Window, "", rightOf(r, ""), runsTag(windowAgent(w), p), p)
	} else {
		right := ""
		if r.Command != "" && r.Command != r.Window {
			right = r.Command
		}
		row = line(strings.Repeat(" ", leadLen), leadLen, r.Window, dim, right, "", p)
	}
	row.Session, row.Window, row.Target, row.Kill = w.session, r.Window, w.target(), w.target()
	if len(w.panes) < 2 {
		row.Labels = shownLabels(r)
		return []PickRow{row}
	}
	if busy := w.busyPanes(); len(busy) < 2 {
		// One pane that matters: the window's row stands for it.
		row.Labels = shownLabels(r)
		if len(busy) == 1 {
			row.Target = w.target() + "." + strconv.Itoa(busy[0].index)
			row.Labels = shownLabels(busy[0].row)
		}
		return []PickRow{row}
	}
	out := []PickRow{row}
	panes := slices.Clone(w.panes)
	slices.SortFunc(panes, func(a, b paneOf) int { return cmp.Compare(a.index, b.index) })
	for _, pn := range panes {
		pr := pn.row
		lead := "   " + glyphOf(pr, p.Col) + " ." + strconv.Itoa(pn.index) + " " + markOf(pr, p) + " "
		paneLead := 9 + len(strconv.Itoa(pn.index))
		right := ""
		if pr.Rank < 4 {
			right = rightOf(pr, "")
		}
		row := line(lead, paneLead, pr.Command, dim, right, runsTag(pr, p), p)
		row.Labels = shownLabels(pr)
		target := w.target() + "." + strconv.Itoa(pn.index)
		row.Session, row.Window, row.Target, row.Kill = w.session, r.Window, target, target
		out = append(out, row)
	}
	return out
}

// foldRow is one dim row for a session's windows without an agent: their
// names. It goes to the session; Enter opens it.
func foldRow(s Session, ws []*window, p Panel) PickRow {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.best().Window
	}
	lead := "   ┄  " // ┄ under the marks, the names under theirs
	text := truncEllipsis(strings.Join(parts, " · "), p.Width-utf8.RuneCountInString(lead))
	return PickRow{Session: s.Name, Target: "=" + s.Name + ":", Fold: s.ID,
		Text: ansiFg(p.Col.Dim) + lead + text + "\x1b[0m"}
}

// stateGroups are the by-state view's groups in order: a title, its
// colour, and which agent rows go in it.
var stateGroups = []struct {
	title  string
	colour func(Colours) string
	holds  func(Row) bool
}{
	{"NEEDS YOU", func(c Colours) string { return c.NeedsYou }, func(r Row) bool { return State(r.State) == NeedsYou }},
	{"WORKING", func(c Colours) string { return c.Working }, func(r Row) bool { return State(r.State) == Working }},
	{"DONE", func(c Colours) string { return c.Done }, func(r Row) bool { return State(r.State) == Done }},
	{"IDLE", func(c Colours) string { return c.Dim }, func(r Row) bool { return r.Rank == 3 }},
}

// renderState lists every agent pane under its group, oldest flag first,
// with its session as a dim column.
func renderState(rows []Row, p Panel) []PickRow {
	var out []PickRow
	for _, g := range stateGroups {
		var body []PickRow
		for _, r := range rows {
			if r.Rank > 3 || !g.holds(r) || !matchesAll(r.Session+" "+r.Window, p.Words) {
				continue
			}
			lead := " " + glyphOf(r, p.Col) + " " + markOf(r, p) + " "
			row := line(lead, leadLen, r.Window, "", rightOf(r, r.Session), runsTag(r, p), p)
			row.Session, row.Window, row.Target, row.Kill = r.Session, r.Window, "="+r.Target, "="+r.Target
			row.Labels = shownLabels(r)
			body = append(body, row)
		}
		if len(body) == 0 {
			continue
		}
		title := g.title + " · " + strconv.Itoa(len(body))
		out = append(out, PickRow{Label: g.title, Text: " \x1b[1m" + ansiFg(g.colour(p.Col)) + title + "\x1b[0m"})
		out = append(out, body...)
	}
	if out == nil && len(p.Words) == 0 {
		out = append(out, PickRow{Label: "none", Text: " " + ansiFg(p.Col.Dim) + "no agents\x1b[0m"})
	}
	return out
}

// glyphOf is a row's flag in its colour; a space for none.
func glyphOf(r Row, col Colours) string {
	switch {
	case r.Failed:
		return ansiFg(col.Failed) + FailedGlyph + "\x1b[0m"
	case State(r.State) == NeedsYou:
		return ansiFg(col.NeedsYou) + "?\x1b[0m"
	case State(r.State) == Done:
		return ansiFg(col.Done) + "✓\x1b[0m"
	case State(r.State) == Working:
		return ansiFg(col.Working) + "●\x1b[0m"
	}
	return " "
}

// mark returns the mark of the agent a row runs, and whether it runs one.
func mark(r Row, p Panel) (AgentMark, bool) {
	for _, name := range []string{r.Agent, r.Command} {
		if m, ok := p.Marks[name]; ok && name != "" {
			return m, true
		}
	}
	if r.IsAgent || r.Agent != "" {
		return DefaultMark, true
	}
	return AgentMark{}, false
}

// markOf is a pane's agent mark in its colour; a space for none.
func markOf(r Row, p Panel) string {
	if m, ok := mark(r, p); ok {
		return ansiFg(m.Colour) + m.Glyph + "\x1b[0m"
	}
	return " "
}

// markOfWindow is the mark of the window's best pane that runs an agent.
func markOfWindow(w *window, p Panel) string {
	for _, pn := range w.panes {
		if _, ok := mark(pn.row, p); ok {
			return markOf(pn.row, p)
		}
	}
	return " "
}

// rightOf is what follows a row's name, right-aligned: a ? or ✓ row's
// reason, the session when one is given, and the age. A working row has
// only its age, an idle agent the time since its last output.
func rightOf(r Row, session string) string {
	var parts []string
	st := State(r.State)
	switch {
	case st == NeedsYou || st == Done:
		parts = append(parts, r.Detail)
	case st == Idle && r.Gone != "":
		parts = append(parts, r.Detail)
	}
	age := ""
	switch {
	case st != Idle && r.Age != "-":
		age = r.Age
	case st == Idle:
		age = r.IdleAge
	}
	tail := strings.TrimSpace(session + " " + age)
	if len(parts) > 0 && tail != "" {
		return parts[0] + " · " + tail
	}
	if len(parts) > 0 {
		return parts[0]
	}
	return tail
}

// windowAgent is the row of the window's pane whose runs the window's row
// counts: the best one with any.
func windowAgent(w *window) Row {
	for _, pn := range w.panes {
		if pn.row.Subagents > 0 || len(pn.row.Nested) > 0 {
			return pn.row
		}
	}
	return w.best()
}

// minName is how much of a name a row keeps before its right part is cut.
const minName = 14

// line lays out line one: the lead, the name (in colour nameFg, "" for
// the default), the tag after it in the working colour (never cut), and
// the right part dim at the row's right edge. The right part gives way
// first, down to minName characters of the name; then the name.
func line(lead string, leadLen int, name, nameFg, right, tag string, p Panel) PickRow {
	nameLen, rightLen := utf8.RuneCountInString(name), utf8.RuneCountInString(right)
	room := p.Width - leadLen - 1 // after the lead and a space
	if tag != "" {
		room -= utf8.RuneCountInString(tag) + 1
	}
	if right != "" {
		if maxRight := room - min(nameLen, minName) - 1; rightLen > maxRight {
			right = truncEllipsis(right, max(maxRight, 0))
			rightLen = utf8.RuneCountInString(right)
		}
		if right != "" {
			room -= rightLen + 1
		}
	}
	shown := truncEllipsis(name, max(room, 1))
	shownLen := utf8.RuneCountInString(shown)
	var b strings.Builder
	b.WriteString(lead + " " + nameFg + highlight(shown, p.Words))
	if nameFg != "" {
		b.WriteString("\x1b[0m")
	}
	if tag != "" {
		b.WriteString(" " + ansiFg(p.Col.Working) + tag + "\x1b[0m")
	}
	if right != "" {
		b.WriteString(strings.Repeat(" ", max(room-shownLen, 0)+1))
		b.WriteString(ansiFg(p.Col.Dim) + right + "\x1b[0m")
	}
	return PickRow{Text: b.String(), indent: leadLen + 1, width: p.Width, dimText: ansiFg(p.Col.Dim)}
}

// truncEllipsis cuts s to n characters, the last an ellipsis when it cuts.
func truncEllipsis(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 1 {
		return truncRunes("…", n)
	}
	return truncRunes(s, n-1) + "…"
}

// PickRows returns the panel's rows, skipped sessions left out. It sweeps
// first. The colours and the marks come from the core.
func (c *Core) PickRows(ctx context.Context, p Panel) ([]PickRow, error) {
	rows, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	out, err := c.Tmux.Output(ctx, "list-sessions", "-F", "#{session_id}"+sep+"#{session_name}")
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, l := range lines(out) {
		f := split(l, 2)
		sessions = append(sessions, Session{ID: f[0], Name: f[1]})
	}
	p.Col, p.Marks = c.colours(ctx), c.Marks
	return RenderPickRows(rows, sessions, p), nil
}
