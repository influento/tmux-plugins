// Package attention is the core of tmux-agent: a pane says what it needs, and
// tmux shows, counts and finds it. It knows no agent: anything may flag the
// pane it runs in.
//
// States, by rank: needs-you (waiting on a decision), done (finished, not
// looked at yet), working (busy), and idle (no flag).
//
// The state lives in tmux options and goes with the server:
//
//	pane    @attention, @attention_reason, @attention_at, @attention_idle_at
//	        @attention_owner   the command the flag belongs to, when one was named
//	        @attention_gone    what a sweep dropped: "<state> · <owner> exited"
//	window  @attention_win     the highest-ranked state among its panes
//	global  @attention_status  the rendered counts, read as #{E:@attention_status}
//
// A flag set with an owner lives only while that command is the pane's
// foreground command. Sweep drops the others, and runs before every count,
// list and focus, so a flag whose process died never shows. There is no
// timer: an agent can be quiet for a long time.
//
// Colours come from the global options @attention_fg_needs_you, _done,
// _working and _dim, so the theme stays in tmux.conf.
package attention

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
)

// State is a pane's flag.
type State string

// The states, by rank.
const (
	Idle     State = ""
	NeedsYou State = "needs-you"
	Done     State = "done"
	Working  State = "working"
)

// ParseState reads a state as tmux options and the command line spell it.
func ParseState(s string) (State, bool) {
	switch st := State(s); st {
	case NeedsYou, Done, Working:
		return st, true
	}
	return Idle, false
}

// Rank orders states: lower needs the user sooner.
func (s State) Rank() int {
	switch s {
	case NeedsYou:
		return 0
	case Done:
		return 1
	case Working:
		return 2
	}
	return 9
}

// Glyph is the mark the status bar, borders and pickers show.
func (s State) Glyph() string {
	switch s {
	case NeedsYou:
		return "?"
	case Done:
		return "✓"
	case Working:
		return "●"
	}
	return " "
}

// String is the state's name; idle for no flag.
func (s State) String() string {
	if s == Idle {
		return "idle"
	}
	return string(s)
}

// Tmux is the part of the tmux client the core uses.
type Tmux interface {
	Run(ctx context.Context, args ...string) (string, error)
	Output(ctx context.Context, args ...string) (string, error)
	Option(ctx context.Context, scope, target, name string) (value string, ok bool, err error)
}

// Core reads and writes flags on one tmux server.
type Core struct {
	Tmux Tmux
	// Notify, when set, is called after a flag changes to needs-you or done
	// without Quiet. It must return at once; the notification waits on its
	// own (see Notification).
	Notify func(pane string, s State)
	// Agents are the commands whose idle panes List shows as agents.
	Agents []string
	// Marks are the panel's agent marks, by agent name and by command.
	Marks map[string]AgentMark
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

func (c *Core) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Flag is a state as a program sets it.
type Flag struct {
	State  State
	Reason string
	Owner  string // the command the flag lives with; empty for none
	Quiet  bool   // no notification
	// Failed marks a done flag as a run that failed. It is still done: seen
	// clears it and it ranks as done; it only looks different.
	Failed bool
}

// FailedGlyph marks a failed done flag where done shows its own glyph.
const FailedGlyph = "✗"

// FailedPrefix starts the reason of a run that failed: "failed: exit 3".
const FailedPrefix = "failed: "

// sep separates fields in tmux formats. A tab would do, but reasons and
// names are free text.
const sep = "\x1f"

// split cuts a formatted line into n fields; missing ones are empty.
func split(line string, n int) []string {
	f := strings.SplitN(line, sep, n)
	for len(f) < n {
		f = append(f, "")
	}
	return f
}

func lines(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// FlagOf returns the pane's state and reason.
func (c *Core) FlagOf(ctx context.Context, pane string) (State, string, error) {
	st, reason, _, err := c.read(ctx, pane)
	return st, reason, err
}

// read returns the pane's state, reason and failed mark.
func (c *Core) read(ctx context.Context, pane string) (st State, reason string, failed bool, err error) {
	out, err := c.Tmux.Output(ctx, "display-message", "-p", "-t", pane,
		"#{@attention}"+sep+"#{@attention_reason}"+sep+"#{@attention_failed}")
	if err != nil {
		return Idle, "", false, err
	}
	f := split(out, 3)
	return State(f[0]), f[1], f[2] == "1", nil
}

// Mark is what a tmux format draws for a flag: its glyph in its colour and
// a space, or nothing for idle. Every pane keeps its own in @attention_mark
// and every window its roll-up's in @attention_win_mark, so tmux.conf draws
// the flags without knowing them.
func Mark(s State, failed bool, col Colours) string {
	glyph, colour := s.Glyph(), ""
	switch {
	case s == Done && failed:
		glyph, colour = FailedGlyph, col.Failed
	case s == Done:
		colour = col.Done
	case s == NeedsYou:
		colour = col.NeedsYou
	case s == Working:
		colour = col.Working
	default:
		return ""
	}
	return "#[fg=" + colour + "]" + glyph + " "
}

// optArgs sets an option at scope (-p or -w), or unsets it when value is
// empty. Like every *Args helper it ends with a separator; runAll drops the
// last one.
func optArgs(scope, target, name, value string) []string {
	if value == "" {
		return []string{"set-option", scope + "u", "-t", target, name, ";"}
	}
	return []string{"set-option", scope, "-t", target, name, value, ";"}
}

func one(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// markArgs sets a pane's failed mark and its drawn mark for a flag.
func markArgs(pane string, s State, failed bool, col Colours) []string {
	return append(optArgs("-p", pane, "@attention_failed", one(failed)),
		optArgs("-p", pane, "@attention_mark", Mark(s, failed, col))...)
}

// runAll runs commands built by the *Args helpers, as few tmux calls as
// tmux accepts (see tmux.Batches). It stops at the first call that fails.
func (c *Core) runAll(ctx context.Context, args []string) error {
	for _, b := range tmux.Batches(args) {
		if _, err := c.Tmux.Run(ctx, b...); err != nil {
			return err
		}
	}
	return nil
}

// Set flags the pane. It returns the state before, and whether the state
// changed: setting the same state again only updates the reason and the
// failed mark, and sends nothing.
func (c *Core) Set(ctx context.Context, pane string, f Flag) (prev State, changed bool, err error) {
	if _, ok := ParseState(string(f.State)); !ok {
		return Idle, false, fmt.Errorf("set: invalid state %q", f.State)
	}
	if f.Failed && f.State != Done {
		return Idle, false, fmt.Errorf("set: only a done flag can be failed, not %s", f.State)
	}
	cur, reason, failed, err := c.read(ctx, pane)
	if err != nil {
		return Idle, false, err
	}
	if cur == f.State {
		if reason == f.Reason && failed == f.Failed {
			return cur, false, nil
		}
		args := []string{"set-option", "-p", "-t", pane, "@attention_reason", f.Reason}
		if failed == f.Failed {
			_, err := c.Tmux.Run(ctx, args...)
			return cur, false, err
		}
		// The failed mark changed, and with it the window's and the counts.
		col := c.colours(ctx)
		if err := c.runAll(ctx, append(markArgs(pane, f.State, f.Failed, col), args...)); err != nil {
			return cur, false, err
		}
		if err := c.rollUp(ctx, pane, col); err != nil {
			return cur, false, err
		}
		return cur, false, c.Status(ctx)
	}
	at := strconv.FormatInt(c.now().Unix(), 10)
	args := []string{
		"set-option", "-p", "-t", pane, "@attention", string(f.State), ";",
		"set-option", "-p", "-t", pane, "@attention_reason", f.Reason, ";",
		"set-option", "-p", "-t", pane, "@attention_at", at, ";",
		"set-option", "-pu", "-t", pane, "@attention_gone", ";",
	}
	col := c.colours(ctx)
	args = append(args, markArgs(pane, f.State, f.Failed, col)...)
	if f.Owner != "" {
		args = append(args, "set-option", "-p", "-t", pane, "@attention_owner", f.Owner)
	} else {
		args = append(args, "set-option", "-pu", "-t", pane, "@attention_owner")
	}
	if _, err := c.Tmux.Run(ctx, args...); err != nil {
		return cur, false, err
	}
	if err := c.rollUp(ctx, pane, col); err != nil {
		return cur, true, err
	}
	if err := c.Status(ctx); err != nil {
		return cur, true, err
	}
	if !f.Quiet && (f.State == NeedsYou || f.State == Done) && c.Notify != nil {
		c.Notify(pane, f.State)
	}
	return cur, true, nil
}

// unflagArgs unsets a pane's flag options. The time the flag was set
// stays in @attention_idle_at: for a done flag, when the turn ended.
func unflagArgs(pane string) []string {
	args := []string{"set-option", "-pF", "-t", pane, "@attention_idle_at", "#{@attention_at}", ";"}
	for _, o := range []string{"@attention", "@attention_reason", "@attention_at", "@attention_owner", "@attention_failed", "@attention_mark"} {
		args = append(args, "set-option", "-pu", "-t", pane, o, ";")
	}
	return args
}

// Clear sets the pane back to idle. A pane without a flag is left alone.
func (c *Core) Clear(ctx context.Context, pane string) error {
	cur, _, err := c.FlagOf(ctx, pane)
	if err != nil || cur == Idle {
		return err
	}
	args := append(unflagArgs(pane), "set-option", "-pu", "-t", pane, "@attention_gone")
	if _, err := c.Tmux.Run(ctx, args...); err != nil {
		return err
	}
	if err := c.rollUp(ctx, pane, c.colours(ctx)); err != nil {
		return err
	}
	return c.Status(ctx)
}

// Seen marks a done pane as looked at; other states stay. Run on focus,
// which reaches every pane, so it sweeps first.
func (c *Core) Seen(ctx context.Context, pane string) error {
	dropped, err := c.Sweep(ctx)
	if err != nil {
		return err
	}
	if dropped {
		if err := c.Status(ctx); err != nil {
			return err
		}
	}
	cur, _, err := c.FlagOf(ctx, pane)
	if err != nil || cur != Done {
		return err
	}
	return c.Clear(ctx, pane)
}

// paneFlags is the format rollUpArgs reads, one line per pane.
const paneFlags = "#{@attention}" + sep + "#{@attention_failed}"

// rollUp sets the flag of the window that holds pane from its panes' flags.
func (c *Core) rollUp(ctx context.Context, pane string, col Colours) error {
	out, err := c.Tmux.Output(ctx, "list-panes", "-t", pane, "-F", paneFlags)
	if err != nil {
		return err
	}
	return c.runAll(ctx, rollUpArgs(pane, lines(out), col))
}

// rollUpArgs sets a window's flag to the highest-ranked flag of its panes
// (paneFlags lines), marks it failed when that flag is done and one of its
// done panes failed, and sets its drawn mark.
func rollUpArgs(window string, panes []string, col Colours) []string {
	best, failed := Idle, false
	for _, l := range panes {
		f := split(l, 2)
		st := State(f[0])
		if st != Idle && st.Rank() < best.Rank() {
			best = st
		}
		failed = failed || (st == Done && f[1] == "1")
	}
	failed = failed && best == Done
	return slices.Concat(
		optArgs("-w", window, "@attention_win", string(best)),
		optArgs("-w", window, "@attention_win_failed", one(failed)),
		optArgs("-w", window, "@attention_win_mark", Mark(best, failed, col)),
	)
}

// Redraw draws every pane's and window's mark again, in the theme's
// colours now. A mark keeps the colours it was drawn in, so tmux-init runs
// this when the config is loaded.
func (c *Core) Redraw(ctx context.Context) error {
	col := c.colours(ctx)
	out, err := c.Tmux.Output(ctx, "list-panes", "-a", "-F", "#{window_id}"+sep+"#{pane_id}"+sep+paneFlags)
	if err != nil {
		return err
	}
	var args, windows []string
	panes := map[string][]string{}
	for _, l := range lines(out) {
		f := split(l, 4)
		win, pane, st := f[0], f[1], State(f[2])
		args = append(args, optArgs("-p", pane, "@attention_mark", Mark(st, st == Done && f[3] == "1", col))...)
		if _, ok := panes[win]; !ok {
			windows = append(windows, win)
		}
		panes[win] = append(panes[win], f[2]+sep+f[3])
	}
	for _, w := range windows {
		args = append(args, rollUpArgs(w, panes[w], col)...)
	}
	if err := c.runAll(ctx, args); err != nil && !isGone(err) {
		return err
	}
	return c.Status(ctx)
}

// Sweep drops every flag whose owner is no longer the pane's command and
// leaves a note the list shows. It reports whether it dropped any.
func (c *Core) Sweep(ctx context.Context) (bool, error) {
	out, err := c.Tmux.Output(ctx, "list-panes", "-a", "-F",
		"#{pane_id}"+sep+"#{@attention}"+sep+"#{@attention_owner}"+sep+"#{pane_current_command}")
	if err != nil {
		return false, err
	}
	dropped := false
	var col *Colours
	for _, l := range lines(out) {
		f := split(l, 4)
		id, st, owner, cmd := f[0], f[1], f[2], f[3]
		if st == "" || owner == "" || owner == cmd {
			continue
		}
		args := append(unflagArgs(id), "set-option", "-p", "-t", id, "@attention_gone", st+" · "+owner+" exited")
		if _, err := c.Tmux.Run(ctx, args...); err != nil {
			if isGone(err) {
				continue
			}
			return dropped, err
		}
		if col == nil {
			cc := c.colours(ctx)
			col = &cc
		}
		if err := c.rollUp(ctx, id, *col); err != nil && !isGone(err) {
			return true, err
		}
		dropped = true
	}
	return dropped, nil
}

// isGone reports whether a tmux error says only that the target no longer
// exists: panes and windows close between two calls.
func isGone(err error) bool {
	te, ok := errors.AsType[*tmux.Error](err)
	return ok && (strings.Contains(te.Stderr, "can't find") || strings.Contains(te.Stderr, "no such"))
}

// Colours are the theme colours of the counts and glyphs.
type Colours struct {
	NeedsYou, Done, Failed, Working, Dim string
}

// colours reads the theme options, with plain colour names for unset ones.
func (c *Core) colours(ctx context.Context) Colours {
	out, err := c.Tmux.Output(ctx, "display-message", "-p",
		"#{@attention_fg_needs_you}"+sep+"#{@attention_fg_done}"+sep+"#{@attention_fg_working}"+sep+
			"#{@attention_fg_dim}"+sep+"#{@attention_fg_failed}")
	if err != nil {
		out = ""
	}
	f := split(out, 5)
	return Colours{
		NeedsYou: or(f[0], "red"), Done: or(f[1], "green"), Working: or(f[2], "blue"),
		Dim: or(f[3], "default"), Failed: or(f[4], "red"),
	}
}

// Counts are the number of panes in each state. Done leaves out the
// failed ones, which Failed counts.
type Counts struct {
	NeedsYou, Done, Failed, Working int
}

// RenderStatus is the status-bar text for the counts: needs you, working,
// failed, done, then a separator; empty when nothing is flagged.
func RenderStatus(n Counts, col Colours) string {
	var b strings.Builder
	part := func(count int, colour, glyph string) {
		if count > 0 {
			fmt.Fprintf(&b, "#[fg=%s]%s %d  ", colour, glyph, count)
		}
	}
	part(n.NeedsYou, col.NeedsYou, NeedsYou.Glyph())
	part(n.Working, col.Working, Working.Glyph())
	part(n.Failed, col.Failed, FailedGlyph)
	part(n.Done, col.Done, Done.Glyph())
	if b.Len() > 0 {
		fmt.Fprintf(&b, "#[fg=%s]│", col.Dim)
	}
	return b.String()
}

// Status sweeps, then renders the counts into @attention_status and
// redraws every client's status bar.
func (c *Core) Status(ctx context.Context) error {
	if _, err := c.Sweep(ctx); err != nil {
		return err
	}
	out, err := c.Tmux.Output(ctx, "list-panes", "-a", "-F", "#{@attention}"+sep+"#{@attention_failed}")
	if err != nil {
		return err
	}
	var n Counts
	for _, l := range lines(out) {
		f := split(l, 2)
		switch State(f[0]) {
		case NeedsYou:
			n.NeedsYou++
		case Done:
			if f[1] == "1" {
				n.Failed++
			} else {
				n.Done++
			}
		case Working:
			n.Working++
		}
	}
	text := RenderStatus(n, c.colours(ctx))
	if _, err := c.Tmux.Run(ctx, "set-option", "-g", "@attention_status", text); err != nil {
		return err
	}
	clients, err := c.Tmux.Output(ctx, "list-clients", "-F", "#{client_name}")
	if err != nil {
		return err
	}
	for _, name := range lines(clients) {
		// A client can detach between the two calls; the others still redraw.
		_, _ = c.Tmux.Run(ctx, "refresh-client", "-S", "-t", name)
	}
	return nil
}
