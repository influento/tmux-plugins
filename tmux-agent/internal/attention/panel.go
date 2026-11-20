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

// The context shares from which the panel warns: in the failed colour,
// then in the needs-you colour.
const (
	contextWarn     = 80
	contextCritical = 95
)

// gaugeOf is a row's context share as the panel shows it before the age,
// and its colour: dim, then warning near the limit; "" for unknown.
func gaugeOf(r Row, col Colours) (text, colour string) {
	switch {
	case r.Context <= 0:
		return "", ""
	case r.Context >= contextCritical:
		colour = col.NeedsYou
	case r.Context >= contextWarn:
		colour = col.Failed
	default:
		colour = col.Dim
	}
	return strconv.Itoa(r.Context) + "%", colour
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
	Also    []string // on an item's row, its other windows ("=name:index")
	Text    string   // line one, with colour escapes
	Labels  []string // line two: the pane's labels; none, no line
	// Key names the row across reloads, as its target does not: the
	// panel's join renumbers its window's panes. tmux's id of the pane
	// the row stands for, of the window for a split window's row, of the
	// session for a fold; "" for a label. A window in two sessions or
	// more has a row in each: its keys then start with the session's id
	// ("$1:@2", "$1:@2.%3"). tmux takes it as a target.
	Key string
	// Keys also name the row: on a task's row, its other panes' keys,
	// as the pane it stands for changes with the flags; on a window's row
	// that stands for one pane, the window's id.
	Keys []string
	// Open is the cursor's row: it shows its whole name and labels.
	Open    bool
	whole   string // Text with the whole name, when Text cuts it; else ""
	indent  int    // the column line two starts at
	width   int    // the columns of a row
	dimText string // the dim colour escape, for line two
}

// Named reports whether key names the row.
func (r PickRow) Named(key string) bool {
	return key != "" && (r.Key == key || slices.Contains(r.Keys, key))
}

// paneKey is the key of the row that stands for r's pane, windowKey of
// the row of its window (see PickRow.Key).
func (r Row) paneKey() string {
	if r.Linked {
		return r.windowKey() + "." + r.Pane
	}
	return r.Pane
}

func (r Row) windowKey() string {
	if r.Linked {
		return r.SessionID + ":" + r.WindowID
	}
	return r.WindowID
}

// IsLabel reports whether the row is a label, not a place to go.
func (r PickRow) IsLabel() bool { return r.Label != "" }

// labelsIndent is how much further than the first the open row's next
// lines of labels start.
const labelsIndent = 2

// Display is the row as the panel shows it: line one, and the labels on
// line two when there are any. An open row shows its whole name and
// labels over as many lines as they take.
func (r PickRow) Display() string {
	text := r.Text
	if r.Open && r.whole != "" {
		text = r.whole
	}
	if len(r.Labels) == 0 {
		return text
	}
	labels, room := strings.Join(r.Labels, " · "), max(r.width-r.indent, 1)
	pad := strings.Repeat(" ", r.indent)
	if !r.Open || utf8.RuneCountInString(labels) <= room {
		return text + "\n" + pad + r.dimText + truncEllipsis(labels, room) + "\x1b[0m"
	}
	lines := wrap(labels, room, max(room-labelsIndent, 1))
	return text + "\n" + pad + r.dimText + strings.Join(lines, "\x1b[0m\n"+pad+strings.Repeat(" ", labelsIndent)+r.dimText) + "\x1b[0m"
}

// wrap cuts s into lines of at most first characters, then rest, each
// broken after the last '-' or space it has; one with none is cut at its
// width. A space at a break goes.
func wrap(s string, first, rest int) []string {
	r := []rune(s)
	var out []string
	for w := first; len(r) > w; w = rest {
		cut := w
		if r[w] != ' ' {
			for i := w; i > 0; i-- {
				if r[i-1] == '-' || r[i-1] == ' ' {
					cut = i
					break
				}
			}
		}
		out = append(out, strings.TrimRight(string(r[:cut]), " "))
		r = []rune(strings.TrimLeft(string(r[cut:]), " "))
	}
	return append(out, string(r))
}

// shownLabels are a row's labels as line two shows them: the branch (a
// label with the token "branch") first, then the others.
func shownLabels(r Row) []string {
	var out []string
	labels := slices.Clone(r.Labels)
	slices.SortStableFunc(labels, func(a, b Label) int {
		return cmp.Compare(boolInt(a.Tokens["branch"] == ""), boolInt(b.Tokens["branch"] == ""))
	})
	for _, l := range labels {
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

// task is the task a pane of the window is bound to, and its title.
func (w *window) task() (key, title string) {
	for _, p := range w.panes {
		if p.row.Task != "" {
			return p.row.Task, cmp.Or(p.row.TaskTitle, p.row.Task)
		}
	}
	return "", ""
}

// repo is the repository of the window's best pane that has one.
func (w *window) repo() string {
	for _, p := range w.panes {
		if r := p.row.Repo(); r != "" {
			return r
		}
	}
	return ""
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

// entry is one place of the tree: a window, or a task's windows as one
// item.
type entry struct {
	wins  []*window // one for a window; a task's, in index order
	title string    // a task's title; "" for a window
	repo  string
}

func (e entry) isItem() bool { return e.title != "" }

// renderTree lists the sessions, those with a flag first by their best
// rank. Under each, its agent windows, grouped by repository under a rule
// line when the session has two or more: a task's windows as one item
// row, first in their repository, then the others in index order. A
// split window lists its busy panes. Then the session's other windows,
// folded into one row unless a search runs or the user opened the fold.
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
		var entries []*entry
		items := map[string]*entry{}
		var folded []*window
		for _, w := range ws {
			key, title := w.task()
			if !matchesAll(s.Name+" "+w.best().Window+" "+title, p.Words) {
				continue
			}
			switch {
			case key != "":
				if it, ok := items[key]; ok {
					it.wins = append(it.wins, w)
					continue
				}
				items[key] = &entry{wins: []*window{w}, title: title}
				entries = append(entries, items[key])
			case w.hasAgent():
				entries = append(entries, &entry{wins: []*window{w}})
			default:
				folded = append(folded, w)
			}
		}
		var body []PickRow
		for _, g := range byRepo(entries, func(e *entry) string {
			if e.isItem() {
				return itemPane(windowRowsOf(e.wins)).Repo()
			}
			return e.wins[0].repo()
		}) {
			if g.rule {
				body = append(body, ruleRow(s.Name, g.repo, p))
			}
			for _, e := range g.members {
				if e.isItem() {
					body = append(body, itemRow(windowRowsOf(e.wins), e.title, p))
				} else {
					body = append(body, windowRows(e.wins[0], true, p)...)
				}
			}
		}
		if len(folded) > 0 {
			if len(p.Words) > 0 || slices.Contains(p.Open, s.ID) {
				for _, w := range folded {
					body = append(body, windowRows(w, false, p)...)
				}
			} else {
				body = append(body, foldRow(s, folded, len(entries) > 0, p))
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

// repoGroup is the members of one repository, and whether a rule line
// heads them.
type repoGroup[T any] struct {
	repo    string
	rule    bool
	members []T
}

// byRepo groups members by repository, in the order each repository
// first comes; members with none come first. An item (a task's row) goes
// before the windows of its repository. Rule lines head the groups when
// there are two or more, except the one with no repository.
func byRepo[T interface{ isItem() bool }](members []T, repoOf func(T) string) []repoGroup[T] {
	var groups []repoGroup[T]
	at := map[string]int{}
	for _, m := range members {
		r := repoOf(m)
		i, ok := at[r]
		if !ok {
			i = len(groups)
			at[r] = i
			groups = append(groups, repoGroup[T]{repo: r})
		}
		groups[i].members = append(groups[i].members, m)
	}
	slices.SortStableFunc(groups, func(a, b repoGroup[T]) int {
		return cmp.Compare(boolInt(a.repo != ""), boolInt(b.repo != ""))
	})
	for i := range groups {
		slices.SortStableFunc(groups[i].members, func(a, b T) int {
			return cmp.Compare(boolInt(!a.isItem()), boolInt(!b.isItem()))
		})
		groups[i].rule = len(groups) > 1 && groups[i].repo != ""
	}
	return groups
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ruleRow is a repository's rule line: ─ name ────, to the row's width.
// The cursor steps over it.
func ruleRow(session, repo string, p Panel) PickRow {
	dim := ansiFg(p.Col.Dim)
	name := truncEllipsis(repo, max(p.Width-6, 1))
	rest := max(p.Width-4-utf8.RuneCountInString(name), 1)
	return PickRow{Label: repo, Session: session, Target: "=" + session + ":",
		Text: dim + "─ \x1b[0m" + ansiFg(p.Col.Repo) + name + "\x1b[0m" + dim + " " + strings.Repeat("─", rest) + "\x1b[0m"}
}

// windowRowsOf are the rows of the windows' panes, best first.
func windowRowsOf(ws []*window) []Row {
	var rows []Row
	for _, w := range ws {
		for _, pn := range w.panes {
			rows = append(rows, pn.row)
		}
	}
	SortRows(rows)
	return rows
}

// itemPane is the pane an item's row stands for: the one that waits for
// the user, else the one that works, else the best.
func itemPane(rows []Row) Row {
	for _, st := range []State{NeedsYou, Working} {
		for _, r := range rows {
			if State(r.State) == st {
				return r
			}
		}
	}
	return rows[0]
}

// itemRow is a task's windows as one row: its title, bold, and the role
// that matters now. A working pane whose agent runs a role below it (a
// review in a hook) shows that role: no context share of its own, and
// its age since it started.
func itemRow(rows []Row, title string, p Panel) PickRow {
	r := itemPane(rows)
	c := cellOf(r, title, p)
	c.bold = true
	if State(r.State) == Working && len(r.Runs) > 0 {
		run := slices.MaxFunc(r.Runs, func(a, b RoleRun) int { return cmp.Compare(a.Start, b.Start) })
		c.role, c.share, c.shareFg, c.age = run.Role, "–", p.Col.Dim, run.Age
		c.tag = ""
		if r.Subagents > 0 {
			c.tag = SubagentGlyph + strconv.Itoa(r.Subagents)
		}
	}
	row := c.render(p)
	row.Session, row.Window, row.Target = r.Session, r.Window, "="+r.Target
	win := strings.SplitN(r.Target, ".", 2)[0]
	row.Kill = "=" + win
	for _, o := range rows {
		if w := "=" + strings.SplitN(o.Target, ".", 2)[0]; w != row.Kill && !slices.Contains(row.Also, w) {
			row.Also = append(row.Also, w)
		}
	}
	row.Labels = shownLabels(r)
	row.Key = r.paneKey()
	for _, o := range rows {
		if o.Pane != r.Pane {
			row.Keys = append(row.Keys, o.paneKey())
		}
	}
	return row
}

// cellOf is a row of the table for an agent pane, named name: its flag,
// its agent's mark, its runs after the name, its role, context share and
// age.
func cellOf(r Row, name string, p Panel) cell {
	share, fg := gaugeOf(r, p.Col)
	return cell{lead: " " + glyphOf(r, p.Col) + " " + markOf(r, p) + " ", leadLen: leadLen,
		name: name, tag: runsTag(r, p), role: r.Role, share: share, shareFg: fg, age: ageOf(r)}
}

// ageOf is a row's age as the table shows it: its flag's, or for an idle
// agent the time since its last flag.
func ageOf(r Row) string {
	switch st := State(r.State); {
	case st == Idle:
		return r.IdleAge
	case r.Age != "-":
		return r.Age
	}
	return ""
}

// leadLen is the width of a window row's lead: its flag, then its
// agent's mark and a space, as a terminal may draw a mark wider than its
// cell.
const leadLen = 5

// windowRows is a window's row and, for a window with two or more panes
// that run an agent or have a flag, a row for each pane in index order; with
// one such pane, the window's row goes to it and shows its labels. An agent
// window shows its flag, its agent's mark, its role, context share and
// age; another window shows its name dim, and its command. Window indexes
// are left out: the names say more.
func windowRows(w *window, agent bool, p Panel) []PickRow {
	r := w.best()
	busy := w.busyPanes()
	var row PickRow
	switch {
	case !agent:
		right := ""
		if r.Command != "" && r.Command != r.Window {
			right = r.Command
		}
		row = line(strings.Repeat(" ", leadLen), leadLen, r.Window, ansiFg(p.Col.Dim), right, "", Row{}, p)
	case len(busy) >= 2:
		row = cell{lead: " " + glyphOf(r, p.Col) + " " + markOfWindow(w, p) + " ", leadLen: leadLen, name: r.Window}.render(p)
	default:
		if len(busy) == 1 {
			r = busy[0].row
		}
		row = cellOf(r, r.Window, p).render(p)
	}
	row.Session, row.Window, row.Target, row.Kill = w.session, r.Window, w.target(), w.target()
	row.Key = r.paneKey()
	switch len(busy) {
	case 0: // its window: a go keeps the pane the window has focused
		row.Key = r.windowKey()
	case 1:
		// The window's id too: with a second busy pane, or none, the
		// window's row is keyed by it, and it stays the row open as a
		// pane's flag comes or goes.
		row.Keys = []string{r.windowKey()}
	}
	if len(busy) < 2 {
		row.Labels = shownLabels(r)
		if len(w.panes) > 1 && len(busy) == 1 {
			row.Target = w.target() + "." + strconv.Itoa(busy[0].index)
		}
		return []PickRow{row}
	}
	row.Key = r.windowKey() // each pane's row has the pane's
	out := []PickRow{row}
	panes := slices.Clone(w.panes)
	slices.SortFunc(panes, func(a, b paneOf) int { return cmp.Compare(a.index, b.index) })
	for _, pn := range panes {
		pr := pn.row
		c := cellOf(pr, pr.Command, p)
		c.lead = "   " + glyphOf(pr, p.Col) + " ." + strconv.Itoa(pn.index) + " " + markOf(pr, p) + " "
		c.leadLen = 9 + len(strconv.Itoa(pn.index))
		c.dimName = true
		if pr.Rank >= 4 {
			c.share, c.age = "", ""
		}
		row := c.render(p)
		row.Labels = shownLabels(pr)
		target := w.target() + "." + strconv.Itoa(pn.index)
		row.Session, row.Window, row.Target, row.Kill = w.session, r.Window, target, target
		row.Key = pr.paneKey()
		out = append(out, row)
	}
	return out
}

// foldRow is one dim row for a session's windows without an agent: how
// many ("2 more windows" below agent rows). It goes to the session;
// Enter opens it.
func foldRow(s Session, ws []*window, more bool, p Panel) PickRow {
	text := strconv.Itoa(len(ws))
	if more {
		text += " more"
	}
	text += " window"
	if len(ws) != 1 {
		text += "s"
	}
	return PickRow{Session: s.Name, Target: "=" + s.Name + ":", Fold: s.ID, Key: s.ID,
		Text: ansiFg(p.Col.Dim) + "   " + text + "\x1b[0m"}
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

// stateUnit is one row of the by-state view: an agent pane, or a task's
// panes as one item.
type stateUnit struct {
	rows  []Row // one for a pane; a task's, best first
	title string
}

func (u stateUnit) isItem() bool { return u.title != "" }
func (u stateUnit) row() Row {
	if u.isItem() {
		return itemPane(u.rows)
	}
	return u.rows[0]
}

// renderState lists every agent pane, and each task as one item, under
// the group of its flag, oldest flag first, grouped by repository under a
// rule line when a group has two or more.
func renderState(rows []Row, p Panel) []PickRow {
	var units []stateUnit
	items := map[string]int{}
	for _, r := range rows {
		if r.Task == "" {
			if r.Rank <= 3 {
				units = append(units, stateUnit{rows: []Row{r}})
			}
			continue
		}
		if i, ok := items[r.Task]; ok {
			units[i].rows = append(units[i].rows, r)
			continue
		}
		items[r.Task] = len(units)
		units = append(units, stateUnit{rows: []Row{r}, title: cmp.Or(r.TaskTitle, r.Task)})
	}
	var out []PickRow
	for _, g := range stateGroups {
		var members []stateUnit
		for _, u := range units {
			r := u.row()
			if r.Rank > 3 || !g.holds(r) || !matchesAll(r.Session+" "+r.Window+" "+u.title, p.Words) {
				continue
			}
			members = append(members, u)
		}
		if len(members) == 0 {
			continue
		}
		slices.SortStableFunc(members, func(a, b stateUnit) int { return cmp.Compare(a.row().At, b.row().At) })
		title := g.title + " · " + strconv.Itoa(len(members))
		out = append(out, PickRow{Label: g.title, Text: " \x1b[1m" + ansiFg(g.colour(p.Col)) + title + "\x1b[0m"})
		for _, rg := range byRepo(members, func(u stateUnit) string { return u.row().Repo() }) {
			if rg.rule {
				out = append(out, ruleRow("", rg.repo, p))
			}
			for _, u := range rg.members {
				if u.isItem() {
					out = append(out, itemRow(u.rows, u.title, p))
					continue
				}
				r := u.rows[0]
				row := cellOf(r, r.Window, p).render(p)
				row.Session, row.Window, row.Target, row.Kill = r.Session, r.Window, "="+r.Target, "="+r.Target
				row.Key = r.paneKey()
				row.Labels = shownLabels(r)
				out = append(out, row)
			}
		}
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

// The table's columns at a row's right edge: the context share, a space
// and the age, endGap columns before the edge. The role column ends a
// space before them.
const (
	shareW = 4
	ageW   = 3
	roleW  = 11
	endGap = 2
)

// cell is one row of the table before layout.
type cell struct {
	lead           string // the flag and the mark, with colour escapes
	leadLen        int
	name           string
	bold, dimName  bool
	tag            string // after the name, in the working colour
	role           string
	share, shareFg string // the context share and its colour; "" for none
	age            string
}

// render lays the cell out: the lead, the name and its tag, then in their
// columns the role, the share and the age. A name too long for its column
// takes a line of its own, its columns below it. A row with no role lends
// that column to its name.
func (c cell) render(p Panel) PickRow {
	start := max(p.Width-endGap-ageW-1-shareW, c.leadLen+4) // where the share starts
	area := start - c.leadLen - 1                           // the name's, and the role's, after a space
	nameArea := area
	if c.role != "" {
		nameArea = area - roleW - 1
	}
	tagLen, tag := 0, ""
	if c.tag != "" {
		tagLen = utf8.RuneCountInString(c.tag) + 1
		tag = " " + ansiFg(p.Col.Working) + c.tag + "\x1b[0m"
	}
	style := ""
	switch {
	case c.bold:
		style = "\x1b[1m"
	case c.dimName:
		style = ansiFg(p.Col.Dim)
	}
	right := ""
	if c.share != "" || c.age != "" {
		right = ansiFg(c.shareFg) + padLeft(c.share, shareW) + "\x1b[0m " + ansiFg(p.Col.Dim) + padLeft(c.age, ageW) + "\x1b[0m"
	}
	role := ""
	if c.role != "" {
		role = " " + ansiFg(p.Col.Role) + padRight(truncEllipsis(c.role, roleW), roleW) + "\x1b[0m"
	}
	row := PickRow{indent: c.leadLen + 1, width: p.Width, dimText: ansiFg(p.Col.Dim)}
	colour := func(s string) string { return style + s + "\x1b[0m" }
	shown := func(name string) string { return colour(highlight(name, p.Words)) }
	nameLen := utf8.RuneCountInString(c.name)
	if nameLen+tagLen <= nameArea {
		row.Text = c.lead + " " + shown(c.name) + tag
		if role != "" || right != "" {
			row.Text += strings.Repeat(" ", nameArea-nameLen-tagLen) + role + right
		}
		return row
	}
	room := max(p.Width-c.leadLen-1-tagLen, 1)
	below := ""
	if role != "" || right != "" {
		below = "\n" + strings.Repeat(" ", c.leadLen+1)
		if role != "" {
			below += strings.Repeat(" ", nameArea) + role
		} else {
			below += strings.Repeat(" ", area)
		}
		below += right
	}
	name := truncEllipsis(c.name, room)
	row.Text = c.lead + " " + shown(name) + tag + below
	if name != c.name {
		row.whole = c.lead + " " + wrapped(c.name, room, row.indent, p.Words, colour) + tag + below
	}
	return row
}

// wrapped is name over lines of room characters, each in colour, the
// lines after the first indent columns in. A match of words is
// underlined on each line it falls on.
func wrapped(name string, room, indent int, words []string, colour func(string) string) string {
	lines := wrap(name, room, room)
	runes, marked := []rune(name), matched(name, words)
	at := 0
	for i, l := range lines {
		for i > 0 && at < len(runes) && runes[at] == ' ' { // the break's spaces, which wrap trims
			at++
		}
		n := utf8.RuneCountInString(l)
		lines[i] = colour(underline(runes[at:at+n], marked[at:at+n]))
		at += n
	}
	return strings.Join(lines, "\n"+strings.Repeat(" ", indent))
}

// padLeft and padRight fill s with spaces to n characters.
func padLeft(s string, n int) string {
	return strings.Repeat(" ", max(n-utf8.RuneCountInString(s), 0)) + s
}

func padRight(s string, n int) string {
	return s + strings.Repeat(" ", max(n-utf8.RuneCountInString(s), 0))
}

// minName is how much of a name a row keeps before its right part is cut.
const minName = 14

// line lays out line one of a window without an agent: the lead, the name
// (in colour nameFg, "" for the default), the tag after it in the working
// colour (never cut), and at the row's right edge the context share of
// gauge's row in its colour (never cut), then the right part dim. The
// right part gives way first, down to minName characters of the name; then
// the name.
func line(lead string, leadLen int, name, nameFg, right, tag string, gauge Row, p Panel) PickRow {
	nameLen, rightLen := utf8.RuneCountInString(name), utf8.RuneCountInString(right)
	wholeRight := right
	room := p.Width - leadLen - 1 // after the lead and a space
	if tag != "" {
		room -= utf8.RuneCountInString(tag) + 1
	}
	nameRoom := max(room, 1) // the name's on a line of its own
	share, shareFg := gaugeOf(gauge, p.Col)
	if share != "" {
		room -= utf8.RuneCountInString(share) + 1
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
	colour := func(s string) string {
		s = nameFg + s
		if nameFg != "" {
			s += "\x1b[0m"
		}
		return s
	}
	tagText := ""
	if tag != "" {
		tagText = " " + ansiFg(p.Col.Working) + tag + "\x1b[0m"
	}
	tail := func(right string) string {
		switch {
		case share != "" && right != "":
			return ansiFg(shareFg) + share + "\x1b[0m " + ansiFg(p.Col.Dim) + right + "\x1b[0m"
		case share != "":
			return ansiFg(shareFg) + share + "\x1b[0m"
		case right != "":
			return ansiFg(p.Col.Dim) + right + "\x1b[0m"
		}
		return ""
	}
	row := PickRow{indent: leadLen + 1, width: p.Width, dimText: ansiFg(p.Col.Dim)}
	row.Text = lead + " " + colour(highlight(shown, p.Words)) + tagText
	if share != "" || right != "" {
		row.Text += strings.Repeat(" ", max(room-shownLen, 0)+1) + tail(right)
	}
	if shown == name {
		return row
	}
	// Open, the share and the right part go on a line below the name, at
	// the row's right edge.
	row.whole = lead + " " + wrapped(name, nameRoom, row.indent, p.Words, colour) + tagText
	if share != "" || wholeRight != "" {
		avail := p.Width - row.indent
		if share != "" {
			avail -= utf8.RuneCountInString(share) + 1
		}
		right := truncEllipsis(wholeRight, max(avail, 0))
		tailLen := utf8.RuneCountInString(right)
		if share != "" {
			tailLen += utf8.RuneCountInString(share)
			if right != "" {
				tailLen++
			}
		}
		row.whole += "\n" + strings.Repeat(" ", max(p.Width-tailLen, row.indent)) + tail(right)
	}
	return row
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
	rows, _, err := c.PickRowsNext(ctx, p)
	return rows, err
}

// PickRowsNext is PickRows, and when what they show changes with no event
// to tell (NextChange); 0 for never.
func (c *Core) PickRowsNext(ctx context.Context, p Panel) ([]PickRow, int64, error) {
	rows, now, err := c.list(ctx)
	if err != nil {
		return nil, 0, err
	}
	next, _ := NextChange(rows, now)
	out, err := c.Tmux.Output(ctx, "list-sessions", "-F", "#{session_id}"+sep+"#{session_name}")
	if err != nil {
		return nil, 0, err
	}
	var sessions []Session
	for _, l := range lines(out) {
		f := split(l, 2)
		sessions = append(sessions, Session{ID: f[0], Name: f[1]})
	}
	p.Col, p.Marks = c.colours(ctx), c.Marks
	return RenderPickRows(rows, sessions, p), next, nil
}
