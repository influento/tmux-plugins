// Package ui is what tmux-agent shows outside tmux's own status bar: the
// session and window picker through fzf, and desktop notifications.
package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"github.com/influento/tmux-plugins/internal/shell"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// StripANSI removes colour escapes.
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// PanelWidth is how wide the prefix f panel is, in columns; a narrower
// screen gets all its width.
const PanelWidth = 40

// PickOptions are what the panel needs besides its rows.
type PickOptions struct {
	Self   string // the command that runs tmux-agent, for the keys and the reloads
	Client string // the client the panel shows rows in; "" for tmux's current one
	Start  int    // the row the cursor starts on, from 1
	State  PanelState
	Width  int // the columns of a row
	// Cursor is the cursor row's tmux style (@sidebar_cursor_style): its
	// bg and fg replace FZF_DEFAULT_OPTS's bg+ and fg+ in the panel only.
	Cursor string
	// Listen is the unix socket fzf takes actions on, for Reload; "" for
	// none. A key would do for fzf, but one that lands just after the
	// user's Esc turns both into an Alt key, and Esc is lost.
	Listen string
	// Want is the file the keys write the key of the row they moved to in, for
	// `sidebar go` in the background, and Going the folder that is there
	// while a go runs; "" for none: the keys then wait for each go.
	Want, Going string
	// Noted are the files the keys write the key of the row they moved
	// to in after their go, with no Want file: the .want and .gone that
	// place reads, so it keeps the cursor on that row.
	Noted []string
}

// moveActions are fzf's actions, as $FZF_ACTION names them, of the
// user's moves in the panel, a shell case pattern.
const moveActions = "up|down|click|mouse|page-up|page-down|half-page-up|half-page-down"

// queryChanged is fzf's input label, which shows nowhere (no input
// border), from a change of the query until the result event of the
// rows read for it.
const queryChanged = "changed"

var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// CursorColors turns a tmux style into fzf's colours for the cursor row:
// "bg=#45475a,fg=#b4befe" is "bg+:#45475a,fg+:#b4befe". Only bg and fg
// count, as #rrggbb or default; fzf refuses to start on a colour it does
// not know, so anything else is left out. "" when nothing is left.
func CursorColors(style string) string {
	var out []string
	for _, f := range strings.FieldsFunc(style, func(r rune) bool { return r == ',' || r == ' ' }) {
		name, colour, ok := strings.Cut(f, "=")
		if !ok || (name != "bg" && name != "fg") {
			continue
		}
		switch {
		case colour == "default":
			colour = "-1"
		case !hexColour.MatchString(colour):
			continue
		}
		out = append(out, name+"+:"+colour)
	}
	return strings.Join(out, ",")
}

// PickLines are the rows as fzf reads them, tab separated: a label's text
// ("" on the other rows), the switch-client target, what the row shows,
// what ctrl-x kills, the session id a fold row opens, then the action of
// up and the key of the row it lands on, and the same of down: past the
// labels next to the row in one step, so fzf never draws the cursor on
// one, and no move at the list's ends. Then the row's key, and last the
// query the rows were read for: fzf filters the rows it shows again for
// each new query, before the rows of that query come.
func PickLines(rows []attention.PickRow, query string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		up, upTo := stepFrom(rows, i, -1, "up")
		down, downTo := stepFrom(rows, i, 1, "down")
		out[i] = strings.Join([]string{r.Label, r.Target, r.Display(), r.Kill, r.Fold, up, upTo, down, downTo, r.Key, query}, "\t")
	}
	return out
}

// stepFrom is the action that moves the cursor from row i to the next
// row in direction dir that is no label, and that row's key: "ignore"
// and row i's when there is none. From a label it is one step, and no
// key: the result event moves the cursor off a label.
func stepFrom(rows []attention.PickRow, i, dir int, action string) (string, string) {
	if rows[i].IsLabel() {
		return action, ""
	}
	for j := i + dir; j >= 0 && j < len(rows); j += dir {
		if !rows[j].IsLabel() {
			return strings.TrimSuffix(strings.Repeat(action+"+", (j-i)*dir), "+"), rows[j].Key
		}
	}
	return "ignore", rows[i].Key
}

// openRow is the shell command, in a transform, that prints the action
// that reads the rows again with the cursor's row (its key, field 10)
// open. Only a move has it: with no row under the cursor (a search that matches
// none) fzf runs no command that names a field, and the other reloads
// keep the row that was open.
func openRow(self string) string {
	return `printf "reload(%s'%s')" ` + shell.Quote(self+" pick-rows --fzf --open=") + " {10}"
}

// Pick lets the user choose a session, a window or a pane with fzf and
// returns its switch-client target; "" when the picker was left without a
// choice. The client shows each row as the cursor reaches it, from the
// start row on, through tmux-agent sidebar go: the client's panel comes
// along. Labels (a session's name, a group's title) the cursor
// skips. Search runs in tmux-agent pick-rows, which matches session and
// window names. ctrl-x kills the row and reloads the list; Tab switches
// the view and Enter on a fold opens it, through tmux-agent pick-key. The
// view and the open folds are the header's label, which the panel does
// not show: fzf hands it to every command it runs.
func Pick(ctx context.Context, rows []attention.PickRow, opts PickOptions) (string, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return "", errors.New("fzf is not installed")
	}
	var in strings.Builder
	for _, l := range PickLines(rows, "") {
		in.WriteString(l + "\x00")
	}
	show := opts.Self + " sidebar go " + shell.Quote(opts.Client)
	list := opts.Self + " pick-rows --fzf"
	// A row knows how far up and down go from it, past the labels, in one
	// step (fields 6 and 8), and the key of the row each lands on (7 and
	// 9): a target names a pane by its index, which the panel's join
	// changes. The key
	// takes the client there itself: fzf fires focus only when it draws,
	// and a reload's result can come before that, whose place put the
	// cursor back on the client's row and lost the move. With a Want file
	// the key, in the same shell, only notes the row and starts the go in
	// the background: fzf takes the next key at once, and a go takes the
	// client to the row noted last (see sidebar go), so a held key leaves
	// no queue of moves behind.
	goNow := show + " {2}"
	for _, f := range opts.Noted {
		goNow += "; printf %s {10} >" + shell.Quote(f)
	}
	goNow = "[ -n {1} ] || { " + goNow + "; }"
	skipUp := "transform(echo {6})+execute-silent(" + goNow + ")"
	skipDown := "transform(echo {8})+execute-silent(" + goNow + ")"
	if opts.Want != "" {
		goCmd := opts.Self + " sidebar go " + shell.Quote(opts.Client)
		skipUp = "transform(" + noteAndGo(opts.Want, opts.Going, goCmd, "{7}") + "; echo {6})"
		skipDown = "transform(" + noteAndGo(opts.Want, opts.Going, goCmd, "{9}") + "; echo {8})"
	}
	change := "change-input-label(" + queryChanged + ")+reload-sync(" + list + ")"
	focusGo := goNow
	if opts.Want != "" {
		focusGo = "[ -n {1} ] || " + noteAndGo(opts.Want, opts.Going, opts.Self+" sidebar go "+shell.Quote(opts.Client), "{10}")
	}
	place := opts.Self + " pick-key place " + shell.Quote(opts.Client)
	args := []string{"--ansi", "--height=100%", "--layout=reverse", "--no-sort",
		// Rows show the name and the detail. fzf only displays: pick-rows
		// filters on each change of the query.
		"--read0", "--delimiter=\t", "--with-nth=3", "--disabled", "--no-hscroll",
		// A row is its target and its fold (a session's line and its fold
		// share a target): after a reload the cursor on the same row is no
		// new focus.
		"--id-nth=2,5",
		"--header=" + Header(opts.State.View, opts.Width), "--header-first", "--header-label=" + opts.State.String(),
		// The views in the text's colour, not the theme's header colour. The
		// text and the background are the pane's, not a theme's from
		// FZF_DEFAULT_OPTS: tmux's styles colour the panel, focused or not.
		"--color=header:-1,fg:-1,bg:-1",
		// Plain: no borders, labels or footer; one column before the rows.
		"--margin=0", "--padding=0", "--border=none", "--list-border=none", "--input-border=none",
		"--info=hidden", "--no-scrollbar", "--no-separator", "--highlight-line",
		"--pointer=▌", "--marker=", "--gutter= ", "--ellipsis=…",
		"--prompt= ", "--ghost=search",
		fmt.Sprintf("--bind=ctrl-x:execute-silent([ -z {4} ] || %s kill {4})+reload(%s)", opts.Self, list),
		"--bind=tab:transform:" + opts.Self + " pick-key tab",
		"--bind=btab:transform:" + opts.Self + " pick-key tab",
		"--bind=resize:transform:" + opts.Self + " pick-key resize",
		"--bind=down:" + skipDown, "--bind=ctrl-j:" + skipDown, "--bind=ctrl-n:" + skipDown, "--bind=scroll-down:" + skipDown,
		"--bind=up:" + skipUp, "--bind=ctrl-k:" + skipUp, "--bind=ctrl-p:" + skipUp, "--bind=scroll-up:" + skipUp,
		// A new list: the first rows of a changed query (field 11) start
		// on its first window, unless the user moved meanwhile; rows read
		// before the change wait for them. A cursor on a session line
		// leaves it; otherwise the cursor goes to the client's window, as
		// place reads it then: the cursor's row may be gone, or the
		// client moved some other way. Every other reload during a search
		// (a tick, the panel's join, a move's open row) put the cursor
		// back on the first window, and the client followed it.
		`--bind=result:transform:if [ -n {q} ] && [ -n "$FZF_INPUT_LABEL" ]; then case "$FZF_ACTION" in ` + moveActions +
			`) echo 'change-input-label()' ;; *) [ {s11} != {q} ] || echo 'change-input-label()+pos(2)' ;; esac; ` +
			`elif [ -n {1} ]; then echo down; else ` + place + `; fi`,
		"--bind=enter:transform:[ -n {1} ] || " + opts.Self + " pick-key enter {5}",
		// Only the user's moves take the client along: the other moves (a
		// click, a page) through focus. A move of a reload (rows gone or in
		// another order) or of place fires focus with $FZF_ACTION rebind or
		// reload: a go then, from rows read before a window switch, took
		// the client back. After up or down, the key went itself. Each move
		// of the user's reads the rows again: fzf shows a row as it read
		// it, and the cursor's row opens.
		`--bind=focus:transform(case "$FZF_ACTION" in rebind|reload|reload-sync) ;; up|down) ` + openRow(opts.Self) + ` ;; *) { ` +
			focusGo + `; } >/dev/null 2>&1; ` + openRow(opts.Self) + ` ;; esac)`,
		// The client stays where it is until the cursor is on its row: the
		// first row fires a focus event before the cursor moves.
		"--bind=start:unbind(focus)",
		fmt.Sprintf("--bind=load:pos(%d)+rebind(focus)+unbind(load)", max(opts.Start, 1)),
		"--bind=change:" + change,
		// vim-tmux-navigator sends C-l to fzf as to vim: fzf passes it on,
		// so focus leaves the panel for the pane on its right.
		`--bind=ctrl-l:execute-silent(tmux select-pane -R -t "$TMUX_PANE")`,
	}
	if c := CursorColors(opts.Cursor); c != "" {
		args = append(args, "--color="+c)
	}
	if opts.Listen != "" {
		args = append(args, "--listen="+opts.Listen)
	}
	cmd := exec.CommandContext(ctx, "fzf", args...)
	cmd.Stdin = strings.NewReader(in.String())
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		// 1: no match, 130: left with Esc or Ctrl-C.
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && (exit.ExitCode() == 1 || exit.ExitCode() == 130) {
			return "", nil
		}
		return "", err
	}
	f := strings.SplitN(strings.TrimSuffix(out.String(), "\n"), "\t", 3)
	if len(f) < 2 {
		return "", nil
	}
	return f[1], nil
}

// noteAndGo is the shell command that notes the row's key (an fzf
// field) in the file want, and runs goCmd in the background unless one
// runs (the folder going is there): that go takes the row too. Nothing
// for an empty key (a label's) or one noted already.
func noteAndGo(want, going, goCmd, key string) string {
	w := shell.Quote(want)
	return `{ t=` + key + `; r=; read -r r 2>/dev/null <` + w + `; [ -z "$t" ] || [ "$r" = "$t" ] || { printf %s "$t" >` + w +
		`; mkdir ` + shell.Quote(going) + ` 2>/dev/null && { ` + goCmd + ` </dev/null >/dev/null 2>&1 & }; }; }`
}

// placeAction puts the cursor on the row of client's window, without a
// move of the client: the window is where the client is.
func placeAction(self, client string) string {
	return fmt.Sprintf("transform(%s pick-key place %s)", self, shell.Quote(client))
}

// Reload tells the panel's fzf listening on socket to read its rows again:
// windows came, went or were renamed, a flag or an age changed. The
// result event then puts the cursor on the client's window. self is
// Pick's.
func Reload(ctx context.Context, socket, self string) error {
	// rebind(focus) changes nothing but $FZF_ACTION: the focus event of
	// a cursor the new rows moved is no move of the user's. No tracking
	// of the cursor's row: fzf would undo a key pressed during the reload.
	return send(ctx, socket, "rebind(focus)+reload-sync("+self+" pick-rows --fzf)")
}

// Place tells the panel's fzf listening on socket that its client moved
// some other way than the panel: the cursor goes to the client's window.
// self and client are Pick's.
func Place(ctx context.Context, socket, self, client string) error {
	return send(ctx, socket, placeAction(self, client))
}

// send posts actions to the fzf listening on socket.
func send(ctx context.Context, socket, actions string) error {
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fzf/", strings.NewReader(actions))
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("ui: send to the panel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ui: send to the panel: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return nil
}

// Notify sends a desktop notification and, where notify-send supports
// actions, waits for it to close: clicked is true when the user clicked
// it. Without notify-send it does nothing.
func Notify(ctx context.Context, title, body string) (clicked bool, err error) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return false, nil
	}
	help, _ := exec.CommandContext(ctx, "notify-send", "--help").Output() // an old notify-send has no help: no actions
	if !bytes.Contains(help, []byte("--action")) {
		return false, exec.CommandContext(ctx, "notify-send", "-a", "tmux", title, body).Run()
	}
	out, err := exec.CommandContext(ctx, "notify-send", "-a", "tmux", "-A", "default=Open", title, body).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "default", nil
}

// terminalAppID is the sway app id of the terminal that holds tmux.
const terminalAppID = "com.mitchellh.ghostty"

// RaiseTerminal focuses the terminal window under sway, after a click on
// a notification brought tmux to a pane. Without sway it does nothing.
func RaiseTerminal(ctx context.Context, getenv func(string) string) error {
	if getenv("SWAYSOCK") == "" {
		return nil
	}
	if _, err := exec.LookPath("swaymsg"); err != nil {
		return nil
	}
	return exec.CommandContext(ctx, "swaymsg", fmt.Sprintf("[app_id=%q] focus", terminalAppID)).Run()
}

// PanelState is what the panel shows: its view and the sessions whose
// fold the user opened, by id. Its text is the header's label.
type PanelState struct {
	View attention.View
	Open []string
}

// ParsePanelState reads a state from its text; the tree for none.
func ParsePanelState(s string) PanelState {
	f := strings.Fields(s)
	st := PanelState{View: attention.TreeView}
	if len(f) == 0 {
		return st
	}
	if v := attention.View(f[0]); slices.Contains(attention.Views, v) {
		st.View = v
	}
	st.Open = f[1:]
	return st
}

// String is the state's text: the view, then the open sessions' ids.
// Ids are $ and a number: no space, no bracket.
func (s PanelState) String() string {
	return strings.Join(append([]string{string(s.View)}, s.Open...), " ")
}

// Next is the state with the next view, the folds as they are.
func (s PanelState) Next() PanelState {
	i := slices.Index(attention.Views, s.View)
	s.View = attention.Views[(i+1)%len(attention.Views)]
	return s
}

// Header is the panel's top line: the views, the current one bold in
// brackets, and the key that switches them at the right edge.
func Header(current attention.View, width int) string {
	var b strings.Builder
	n := 0
	for _, v := range attention.Views {
		b.WriteString(" ")
		if v == current {
			b.WriteString("\x1b[1m[" + string(v) + "]\x1b[0m")
			n += len(v) + 3
		} else {
			b.WriteString("\x1b[2m" + string(v) + "\x1b[0m")
			n += len(v) + 1
		}
	}
	const hint = "tab ⇄ "
	gap := width - n - utf8.RuneCountInString(hint)
	if gap < 1 {
		return b.String()
	}
	return b.String() + strings.Repeat(" ", gap) + "\x1b[2m" + hint + "\x1b[0m"
}

// RowWidth is how many columns a panel row has in the terminal f: fzf
// takes one for its pointer. PanelWidth's when f is no terminal.
func RowWidth(f *os.File) int {
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.Col < 2 {
		return PanelWidth - 1
	}
	return int(ws.Col) - 1
}
