// Package tally adds up the tokens agents spent, from the sessions' token
// tallies and the subagents' records in the store, into the rows of
// `tmux-agent tokens`. It knows no agent.
package tally

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// Main is the type of a main agent's calls, beside its subagents' types.
const Main = "main"

// Entry is tokens spent at one time: a main agent's calls of one model in
// one hour, or one subagent. The session's own folder (Path, Where with ~
// for the home folder) and title; and the same of its parent, the session
// it counts for (see Attribution): itself when no session started it.
type Entry struct {
	Agent                 string
	Session, Path, Where  string
	Title                 string
	Parent, PPath, PWhere string
	PTitle                string
	Model, Type           string
	At                    time.Time
	Tokens                store.Tokens
	Subagent              bool
}

// IsRun reports whether another session started the entry's.
func (e Entry) IsRun() bool { return e.Parent != e.Session }

// Entries reads the store's tallies and subagent records of the agents. A
// subagent is counted at its start.
func Entries(st *store.Store, agents []string, at Attribution) ([]Entry, error) {
	var out []Entry
	for _, ag := range agents {
		tallies, err := st.AllUsage(ag)
		if err != nil {
			return nil, err
		}
		r := newResolver(ag, tallies, at)
		entry := func(session string) Entry {
			top := r.top(session)
			own, ppath := r.path(session), r.path(top)
			if ppath == "" {
				ppath = own // the parent's tally is gone
			}
			return Entry{Agent: ag,
				Session: session, Path: own, Where: r.short(own), Title: r.tallies[session].Title,
				Parent: top, PPath: ppath, PWhere: r.short(ppath), PTitle: r.tallies[top].Title}
		}
		for _, u := range tallies {
			for hour, models := range u.Hours {
				t, err := time.Parse(store.HourLayout, hour)
				if err != nil {
					continue
				}
				for model, tok := range models {
					e := entry(u.Session)
					e.Model, e.Type, e.At, e.Tokens = model, Main, t, tok
					out = append(out, e)
				}
			}
		}
		subs, err := st.AllSubagents(ag)
		if err != nil {
			return nil, err
		}
		for _, s := range subs {
			if s.Tokens == nil {
				continue
			}
			e := entry(s.Session)
			e.Model, e.Type, e.At = s.Model, cmp.Or(s.Type, "?"), time.Unix(cmp.Or(s.Started, s.Ended, s.Updated), 0)
			e.Tokens, e.Subagent = *s.Tokens, true
			out = append(out, e)
		}
	}
	return out, nil
}

// shortID is the start of a session id, enough to tell sessions apart.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// The groupings.
var Bys = []string{"day", "session", "path", "model", "type", "agent"}

// Row is the tokens of one group. By session or path, unless flat, a row
// is a parent: Totals are its own work (its calls and its subagents), the
// runs it started are its Children, and WithRuns adds them.
type Row struct {
	Key     string `json:"key"`
	Agent   string `json:"agent,omitempty"`   // by session
	Session string `json:"session,omitempty"` // by session, and of one run
	Path    string `json:"path,omitempty"`    // by session and by path
	Totals
	WithRuns *Totals  `json:"with_runs,omitempty"`
	Sessions []string `json:"sessions,omitempty"` // of the runs a child folds
	Children []Row    `json:"children,omitempty"`

	order    string          // days sort by date
	runs     map[string]bool // the runs counted
	children map[string]*Row
	with     *Row
	title    string // a child's runs' title, by session
}

// Totals are tokens by kind, the subagents among them, and the runs
// another session started among them.
type Totals struct {
	Input      int64 `json:"input"`
	CacheWrite int64 `json:"cache_write"`
	CacheRead  int64 `json:"cache_read"`
	Output     int64 `json:"output"`
	Subagents  int   `json:"subagent_runs"`
	Runs       int   `json:"runs"`
}

// Fresh is the input the cache did not hold: input and cache writes.
func (t Totals) Fresh() int64 { return t.Input + t.CacheWrite }

// Total is all the tokens together.
func (t Totals) Total() int64 { return t.Fresh() + t.CacheRead + t.Output }

// Sorts are the orders of rows other than days: the most of a kind first.
var Sorts = map[string]func(Totals) int64{
	"total":  Totals.Total,
	"fresh":  Totals.Fresh,
	"cache":  func(t Totals) int64 { return t.CacheRead },
	"output": func(t Totals) int64 { return t.Output },
}

// Options say how Group adds up.
type Options struct {
	Since time.Time
	By    string // one of Bys
	Sort  string // one of Sorts
	Loc   *time.Location
	// Flat gives each session its own row by session or path, in its own
	// folder: no runs under their parent.
	Flat bool
	// Path, when set, keeps what counts for that folder or one below: the
	// parent's folder, or with Flat the session's own.
	Path string
}

// under reports whether path is dir or a folder below it.
func under(path, dir string) bool {
	dir = filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// Group adds up the entries at or after o.Since into a report. Days come
// in order; other rows, and children, the most of the kind o.Sort first,
// parents by their total with runs.
func Group(entries []Entry, o Options) (Report, error) {
	if !slices.Contains(Bys, o.By) {
		return Report{}, fmt.Errorf("no grouping %q: one of %s", o.By, strings.Join(Bys, ", "))
	}
	kind, ok := Sorts[o.Sort]
	if !ok {
		return Report{}, fmt.Errorf("no sort %q: total, fresh, cache or output", o.Sort)
	}
	nest := !o.Flat && (o.By == "session" || o.By == "path")
	rows := map[string]*Row{}
	total := newRow(Row{})
	for _, e := range entries {
		path := e.PPath
		if o.Flat {
			path = e.Path
		}
		if e.At.Before(o.Since) || (o.Path != "" && !under(path, o.Path)) {
			continue
		}
		total.count(e)
		id, r := groupOf(e, o.By, o.Loc, o.Flat)
		if rows[id] == nil {
			rows[id] = newRow(r)
		}
		row := rows[id]
		if !nest {
			row.count(e)
			continue
		}
		if row.with == nil {
			row.with = newRow(Row{})
		}
		row.with.count(e)
		if !e.IsRun() {
			row.count(e)
			continue
		}
		row.runs[e.Session] = true
		cid, c := childOf(e, o.By)
		if row.children[cid] == nil {
			row.children[cid] = newRow(c)
		}
		row.children[cid].count(e)
	}
	order := func(a, b Row) int {
		ta, tb := a.Totals, b.Totals
		if a.WithRuns != nil {
			ta = *a.WithRuns
		}
		if b.WithRuns != nil {
			tb = *b.WithRuns
		}
		return cmp.Or(cmp.Compare(kind(tb), kind(ta)), cmp.Compare(a.Key, b.Key))
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if len(r.children) > 0 {
			r.Runs = len(r.runs)
			with := r.with.Totals
			r.WithRuns = &with
		}
		for _, c := range r.children {
			r.Children = append(r.Children, c.name(o.By))
		}
		slices.SortFunc(r.Children, order)
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b Row) int {
		if o.By == "day" {
			return cmp.Compare(a.order, b.order)
		}
		return order(a, b)
	})
	r := Report{By: o.By, Rows: out, Total: total.Totals}
	if !o.Since.IsZero() {
		r.Since = o.Since.Format(time.RFC3339)
	}
	return r, nil
}

func newRow(r Row) *Row {
	r.runs, r.children = map[string]bool{}, map[string]*Row{}
	return &r
}

// add adds an entry's tokens.
func (t *Totals) add(e Entry) {
	t.Input += e.Tokens.Input
	t.CacheWrite += e.Tokens.CacheWrite
	t.CacheRead += e.Tokens.CacheRead
	t.Output += e.Tokens.Output
	if e.Subagent {
		t.Subagents++
	}
}

// count adds an entry to the row; a run counts once however many entries
// it has.
func (r *Row) count(e Entry) {
	r.add(e)
	if e.IsRun() && !r.runs[e.Session] {
		r.runs[e.Session] = true
		r.Runs++
	}
}

// groupOf is the id and the empty row of an entry's group: by session or
// path, the parent's unless flat.
func groupOf(e Entry, by string, loc *time.Location, flat bool) (string, Row) {
	session, path, where, title := e.Parent, e.PPath, e.PWhere, e.PTitle
	if flat {
		session, path, where, title = e.Session, e.Path, e.Where, e.Title
	}
	switch by {
	case "day":
		t := e.At.In(loc)
		return t.Format(time.DateOnly), Row{Key: t.Format("Mon Jan 02"), order: t.Format(time.DateOnly)}
	case "session":
		key := shortID(session) + " " + filepath.Base(where)
		if title != "" {
			key += " · " + title
		}
		return e.Agent + "/" + session, Row{Key: key, Agent: e.Agent, Session: session, Path: path}
	case "path":
		return where, Row{Key: where, Path: path}
	case "model":
		m := cmp.Or(e.Model, "?")
		return m, Row{Key: m}
	case "type":
		return e.Type, Row{Key: e.Type}
	}
	return e.Agent, Row{Key: e.Agent}
}

// childOf is the id and the empty row of a run's child of its parent: by
// session the parent's runs of one title (a script starts many runs of
// one prompt), by path those in one folder. name names it once counted.
func childOf(e Entry, by string) (string, Row) {
	if by == "path" {
		return e.Where, Row{Key: e.Where, Path: e.Path}
	}
	return e.Title, Row{title: e.Title}
}

// name names a child once its runs are counted: by session one run by its
// id, more by their number, with their sessions in the row.
func (r *Row) name(by string) Row {
	ids := slices.Sorted(maps.Keys(r.runs))
	if len(ids) == 1 {
		r.Session = ids[0]
	} else {
		r.Sessions = ids
	}
	if by == "session" {
		title := cmp.Or(r.title, "untitled")
		if len(ids) == 1 {
			r.Key = "run " + shortID(ids[0]) + " " + title
		} else {
			r.Key = fmt.Sprintf("%d runs · %s", len(ids), title)
		}
	}
	return *r
}

// Since reads when a report starts: Nd (today and the N-1 days before),
// Nh (the last N hours), a date (YYYY-MM-DD, its start in loc) or all.
func Since(s string, now time.Time, loc *time.Location) (time.Time, error) {
	if s == "all" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation(time.DateOnly, s, loc); err == nil {
		return t, nil
	}
	if n, err := strconv.Atoi(s[:max(len(s)-1, 0)]); err == nil && n > 0 {
		switch s[len(s)-1] {
		case 'd':
			y, m, d := now.In(loc).Date()
			return time.Date(y, m, d-(n-1), 0, 0, 0, 0, loc), nil
		case 'h':
			return now.Add(-time.Duration(n) * time.Hour), nil
		}
	}
	return time.Time{}, errors.New("since: Nd, Nh, YYYY-MM-DD or all, not " + strconv.Quote(s))
}

// Report is what `tokens --json` prints.
type Report struct {
	By    string `json:"by"`
	Since string `json:"since,omitempty"` // RFC 3339; empty for all
	Rows  []Row  `json:"rows"`
	Total Totals `json:"total"`
}

// WriteJSON prints the report as JSON.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// maxKey bounds the first column's width.
const maxKey = 44

// childIndent starts a child's row, sumIndent a parent's line with runs.
const (
	childIndent = "  ↳ "
	sumIndent   = "  = "
)

// WriteTable prints the report as a table: the first top rows (all for 0),
// each parent's first top children under it and its total with them, then
// a total that counts everything once.
func (r Report) WriteTable(w io.Writer, top int) error {
	first := func(n int) (int, int) {
		if top > 0 && n > top {
			return top, n - top
		}
		return n, 0
	}
	shown, more := first(len(r.Rows))
	rows := r.Rows[:shown]
	width := utf8.RuneCountInString(r.By)
	for _, row := range rows {
		width = max(width, min(utf8.RuneCountInString(row.Key), maxKey))
		for _, c := range row.Children {
			width = max(width, min(utf8.RuneCountInString(childIndent+c.Key), maxKey))
		}
	}
	var b strings.Builder
	cols := func(key string, cells ...string) {
		fmt.Fprintf(&b, "%s%s  %9s  %10s  %8s  %9s  %5s\n", key, strings.Repeat(" ", max(0, width-utf8.RuneCountInString(key))),
			cells[0], cells[1], cells[2], cells[3], cells[4])
	}
	line := func(key string, t Totals) {
		cols(cut(key, maxKey), Count(t.Fresh()), Count(t.CacheRead), Count(t.Output), number(t.Subagents), number(t.Runs))
	}
	cols(r.By, "fresh in", "cache read", "output", "subagents", "runs")
	for _, row := range rows {
		line(row.Key, row.Totals)
		n, hidden := first(len(row.Children))
		for _, c := range row.Children[:n] {
			key := c.Key
			if r.By == "path" {
				key = cutLeft(key, maxKey-utf8.RuneCountInString(childIndent))
			}
			line(childIndent+key, c.Totals)
		}
		if hidden > 0 {
			fmt.Fprintf(&b, "%s… %d more\n", childIndent, hidden)
		}
		if row.WithRuns != nil {
			line(sumIndent+"with runs", *row.WithRuns)
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "… %d more\n", more)
	}
	line("total", r.Total)
	_, err := io.WriteString(w, b.String())
	return err
}

// Count shows a number of tokens in four characters or so: 950, 12.3k,
// 141.2M, 1.73B.
func Count(n int64) string {
	switch {
	case n < 1_000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 1_000_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%.2fB", float64(n)/1e9)
}

func number(n int) string {
	if n == 0 {
		return "–"
	}
	return strconv.Itoa(n)
}

// cutLeft bounds s to n runes, keeping its end: a folder's last names
// tell it from its neighbours.
func cutLeft(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-n+1:])
}

// cut bounds s to n runes, with an ellipsis when it cuts.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
