package attention

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultSkipSessions hides tmux-overlay's sessions when the global option
// @skip_sessions is unset. Set it to "" to list every session.
const DefaultSkipSessions = "ov-*"

// skipper returns a test for sessions the list, the pickers and the jump
// leave out: names matching a pattern in @skip_sessions (globs, space
// separated).
func (c *Core) skipper(ctx context.Context) (func(session string) bool, error) {
	v, ok, err := c.Tmux.Option(ctx, "-g", "", "@skip_sessions")
	if err != nil {
		return nil, err
	}
	if !ok {
		v = DefaultSkipSessions
	}
	patterns := strings.Fields(v)
	return func(session string) bool {
		for _, p := range patterns {
			if ok, _ := path.Match(p, session); ok {
				return true
			}
		}
		return false
	}, nil
}

// Row is one pane in the list behind the pickers and the jump key.
type Row struct {
	Rank    int    `json:"rank"`
	Pane    string `json:"pane"`
	Glyph   string `json:"glyph"`
	Window  string `json:"window"`
	Target  string `json:"target"` // session:window.pane
	Detail  string `json:"detail"`
	Age     string `json:"age"`              // "-" for none
	State   string `json:"state,omitempty"`  // the flag; empty for idle
	At      int64  `json:"at,omitempty"`     // when the flag was set, unix seconds
	Command string `json:"command"`          // the pane's command
	Session string `json:"session"`          // the session's name
	Agent   string `json:"agent,omitempty"`  // the agent the pane's launcher runs
	Title   string `json:"-"`                // the pane title
	Gone    string `json:"gone,omitempty"`   // what a sweep dropped
	Reason  string `json:"reason,omitempty"` // the flag's reason
	Failed  bool   `json:"failed,omitempty"` // a done flag for a run that failed
}

// Humanize renders an age in seconds as 5s, 3m, 2h or 4d.
func Humanize(s int64) string {
	switch {
	case s < 60:
		return strconv.FormatInt(s, 10) + "s"
	case s < 3600:
		return strconv.FormatInt(s/60, 10) + "m"
	case s < 86400:
		return strconv.FormatInt(s/3600, 10) + "h"
	}
	return strconv.FormatInt(s/86400, 10) + "d"
}

// isAgent reports whether a pane's command is an agent: one the core was
// told about, or the one the pane's launcher runs.
func (c *Core) isAgent(cmd, launched string) bool {
	return cmd != "" && (cmd == launched || slices.Contains(c.Agents, cmd))
}

// List returns every pane outside the skipped sessions: flagged panes by
// rank, oldest first; then idle agents and panes whose flag a sweep dropped;
// then everything else. It sweeps first.
func (c *Core) List(ctx context.Context) ([]Row, error) {
	dropped, err := c.Sweep(ctx)
	if err != nil {
		return nil, err
	}
	if dropped {
		if err := c.Status(ctx); err != nil {
			return nil, err
		}
	}
	skip, err := c.skipper(ctx)
	if err != nil {
		return nil, err
	}
	format := strings.Join([]string{
		"#{session_name}", "#{window_index}.#{pane_index}", "#{pane_id}", "#{window_name}",
		"#{@attention}", "#{@attention_reason}", "#{@attention_at}", "#{pane_current_command}",
		"#{@attention_gone}", "#{@agent}", "#{pane_title}", "#{@attention_failed}",
	}, sep)
	out, err := c.Tmux.Output(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return nil, err
	}
	now := c.now().Unix()
	var rows []Row
	for _, l := range lines(out) {
		f := split(l, 12)
		if skip(f[0]) {
			continue
		}
		r := Row{
			Session: f[0], Target: f[0] + ":" + f[1], Pane: f[2], Window: f[3],
			State: f[4], Reason: f[5], Command: f[7], Gone: f[8], Agent: f[9], Title: f[10],
			Failed: f[4] == string(Done) && f[11] == "1",
		}
		r.At, _ = strconv.ParseInt(f[6], 10, 64) // unset or garbled: no age
		describe(&r, now, c.isAgent(r.Command, r.Agent))
		rows = append(rows, r)
	}
	SortRows(rows)
	return rows, nil
}

// describe fills a row's rank, glyph, detail and age.
func describe(r *Row, now int64, agent bool) {
	st := State(r.State)
	r.Glyph = st.Glyph()
	if r.Failed {
		r.Glyph = FailedGlyph
	}
	r.Age = "-"
	switch {
	case st != Idle:
		r.Rank = st.Rank()
		r.Detail = cmp.Or(r.Reason, r.State)
		if r.At != 0 {
			r.Age = Humanize(now - r.At)
		}
	case r.Gone != "":
		r.Rank, r.Detail = 3, "flag dropped · "+r.Gone
	case agent:
		r.Rank, r.Detail = 3, "idle · "+trimSpinner(r.Title)
	default:
		r.Rank, r.Detail = 4, r.Command
	}
}

// trimSpinner drops the status glyph an agent puts before its pane title
// ("✳ fix the tests").
func trimSpinner(title string) string {
	r, n := utf8.DecodeRuneInString(title)
	if n > 0 && !unicode.IsLetter(r) && !unicode.IsDigit(r) && strings.HasPrefix(title[n:], " ") {
		return title[n+1:]
	}
	return title
}

// SortRows orders rows by rank, then oldest flag first, then target.
func SortRows(rows []Row) {
	slices.SortStableFunc(rows, func(a, b Row) int {
		return cmp.Or(cmp.Compare(a.Rank, b.Rank), cmp.Compare(a.At, b.At), strings.Compare(a.Target, b.Target))
	})
}

// SessionRow is one row of the session picker.
type SessionRow struct {
	Session string
	Display string // the session's name and its windows, with colour escapes
	Rank    int    // the best rank among its windows
}

// Window is one window as the session picker shows it.
type Window struct {
	Session string
	Name    string
	State   State
	Failed  bool // its done flag is for a run that failed
}

// ansiFg is a true-colour foreground escape for a #rrggbb colour, nothing
// for a named one.
func ansiFg(colour string) string {
	h := strings.TrimPrefix(colour, "#")
	if len(h) != 6 {
		return ""
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", v>>16, v>>8&0xff, v&0xff)
}

// truncRunes cuts s to n characters.
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// RenderSessionRows lays out the picker: one row per session in the given
// order, its windows in aligned columns, each with its flag glyph before the
// name. Flagged sessions come first by their best rank.
func RenderSessionRows(sessions []string, windows []Window, col Colours) []SessionRow {
	const nameWidth = 16
	type cell struct{ mark, name string }
	cells := map[string][]cell{}
	rank := map[string]int{}
	for _, s := range sessions {
		rank[s] = 9
	}
	var widths []int
	marks := map[State]string{
		NeedsYou: ansiFg(col.NeedsYou) + "? \x1b[0m",
		Done:     ansiFg(col.Done) + "✓ \x1b[0m",
		Working:  ansiFg(col.Working) + "● \x1b[0m",
	}
	for _, w := range windows {
		if _, ok := rank[w.Session]; !ok {
			continue
		}
		name := truncRunes(w.Name, nameWidth)
		mark, ok := marks[w.State]
		if !ok {
			mark = "  "
		}
		if w.State == Done && w.Failed {
			mark = ansiFg(col.Failed) + FailedGlyph + " \x1b[0m"
		}
		rank[w.Session] = min(rank[w.Session], w.State.Rank())
		i := len(cells[w.Session])
		cells[w.Session] = append(cells[w.Session], cell{mark, name})
		if i == len(widths) {
			widths = append(widths, 0)
		}
		widths[i] = max(widths[i], utf8.RuneCountInString(name))
	}
	rows := make([]SessionRow, 0, len(sessions))
	for _, s := range sessions {
		var b strings.Builder
		cs := cells[s]
		for i, c := range cs {
			b.WriteString(c.mark + c.name)
			if i < len(cs)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c.name)+3))
			}
		}
		head := truncRunes(s, 14)
		head += strings.Repeat(" ", 14-utf8.RuneCountInString(head))
		rows = append(rows, SessionRow{Session: s, Display: head + " " + b.String(), Rank: rank[s]})
	}
	slices.SortStableFunc(rows, func(a, b SessionRow) int { return cmp.Compare(a.Rank, b.Rank) })
	return rows
}

// SessionRows returns the session picker's rows, skipped sessions left out.
// It sweeps first.
func (c *Core) SessionRows(ctx context.Context) ([]SessionRow, error) {
	dropped, err := c.Sweep(ctx)
	if err != nil {
		return nil, err
	}
	if dropped {
		if err := c.Status(ctx); err != nil {
			return nil, err
		}
	}
	skip, err := c.skipper(ctx)
	if err != nil {
		return nil, err
	}
	out, err := c.Tmux.Output(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil, err
	}
	var sessions []string
	for _, s := range lines(out) {
		if !skip(s) {
			sessions = append(sessions, s)
		}
	}
	out, err = c.Tmux.Output(ctx, "list-windows", "-a", "-F",
		"#{session_name}"+sep+"#{window_name}"+sep+"#{@attention_win}"+sep+"#{@attention_win_failed}")
	if err != nil {
		return nil, err
	}
	var windows []Window
	for _, l := range lines(out) {
		f := split(l, 4)
		windows = append(windows, Window{Session: f[0], Name: f[1], State: State(f[2]), Failed: f[3] == "1"})
	}
	return RenderSessionRows(sessions, windows, c.colours(ctx)), nil
}
