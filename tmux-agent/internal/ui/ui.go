// Package ui is what tmux-agent shows outside tmux's own status bar: the
// session and window picker through fzf, the popup it opens in, and desktop
// notifications.
package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

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
	Self   string // the command that runs tmux-agent, for the kill key and the reload
	Client string // the client the panel shows rows in; "" for tmux's current one
	Start  int    // the row the cursor starts on, from 1
	// Prefix and Key are the panel's own tmux binding, as fzf names keys:
	// pressing them in the panel closes it, as Enter does. Either "": none.
	Prefix, Key string
}

// FzfKey names a tmux key as fzf does: C-Space is ctrl-space, M-a alt-a.
// "" for a key fzf has no name for.
func FzfKey(key string) string {
	mod, base := "", key
	switch {
	case strings.HasPrefix(key, "C-"):
		mod, base = "ctrl-", key[2:]
	case strings.HasPrefix(key, "M-"):
		mod, base = "alt-", key[2:]
	}
	switch {
	case base == "Space":
		return mod + "space"
	case len(base) == 1 && base[0] > ' ' && base[0] < 0x7f:
		return mod + strings.ToLower(base)
	}
	return ""
}

// PickLine is a row as fzf reads it, tab separated: the session on a
// session's name line ("" on the others), the target, the name with what
// the query's words match underlined, and the detail.
func PickLine(r attention.PickRow, words []string) string {
	label := ""
	if r.IsSession() {
		label = r.Session
	}
	return label + "\t" + r.Target + "\t" + r.Display(words) + "\t" + r.Detail
}

// Pick lets the user choose a session, a window or a pane with fzf and
// returns its switch-client target; "" when the picker was left without a
// choice. The client shows each row as the cursor reaches it, from the
// start row on. A session's name line is a label: the cursor skips it.
// Search runs in tmux-agent pick-rows, which matches session and window
// names. ctrl-x kills the row and reloads the list.
func Pick(ctx context.Context, rows []attention.PickRow, opts PickOptions) (string, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return "", errors.New("fzf is not installed")
	}
	var in strings.Builder
	for _, r := range rows {
		in.WriteString(PickLine(r, nil) + "\n")
	}
	show := "tmux switch-client"
	if opts.Client != "" {
		show += " -c " + shell.Quote(opts.Client)
	}
	list := opts.Self + " pick-rows {q}"
	// A session line has its name in field 1: moves step over it, onto the
	// next window in their direction (down from the top line).
	skipDown := "down+transform:[ -n {1} ] && echo down || true"
	skipUp := `up+transform:if [ -n {1} ]; then [ "$FZF_POS" = 1 ] && echo down || echo up; fi`
	change := "reload-sync(" + list + ")"
	args := []string{"--ansi", "--height=100%", "--layout=reverse", "--no-sort",
		// Rows show the name and the detail. fzf only displays: pick-rows
		// filters on each change of the query.
		"--delimiter=\t", "--with-nth=3,4", "--disabled", "--tabstop=2", "--no-hscroll",
		// Plain: no borders, labels or footer; one column before the rows.
		"--margin=0", "--padding=0", "--border=none", "--list-border=none", "--input-border=none",
		"--info=hidden", "--no-scrollbar", "--no-separator", "--highlight-line",
		"--pointer=▌", "--marker=", "--gutter= ", "--ellipsis=…",
		"--prompt= ", "--ghost=search",
		fmt.Sprintf("--bind=ctrl-x:execute-silent(%s kill {2})+reload(%s)", opts.Self, list),
		"--bind=down:" + skipDown, "--bind=ctrl-j:" + skipDown, "--bind=ctrl-n:" + skipDown, "--bind=scroll-down:" + skipDown,
		"--bind=up:" + skipUp, "--bind=ctrl-k:" + skipUp, "--bind=ctrl-p:" + skipUp, "--bind=scroll-up:" + skipUp,
		// A new list: a search starts on its first window; otherwise the
		// cursor leaves a session line it landed on.
		`--bind=result:transform:if [ -n {q} ]; then echo 'pos(2)'; elif [ -n {1} ]; then echo down; fi`,
		"--bind=enter:transform:[ -n {1} ] || echo accept",
		"--bind=focus:execute-silent([ -n {1} ] || " + show + " -t {2})",
		// The client stays where it is until the cursor is on its row.
		"--bind=start:unbind(focus)",
		fmt.Sprintf("--bind=load:pos(%d)+rebind(focus)+unbind(load)", max(opts.Start, 1)),
	}
	if opts.Prefix != "" && opts.Key != "" {
		// The prefix arms the prompt; the key then closes the panel, and any
		// other change to the query disarms it.
		const armed = " prefix "
		args = append(args,
			"--bind="+opts.Prefix+":change-prompt("+armed+")",
			fmt.Sprintf("--bind=%s:transform:[ \"$FZF_PROMPT\" = %s ] && echo accept || echo %s",
				opts.Key, shell.Quote(armed), shell.Quote("put("+opts.Key+")")))
		change += "+change-prompt( )"
	}
	args = append(args, "--bind=change:"+change)
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
