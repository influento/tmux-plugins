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
	"unicode/utf8"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// StripANSI removes colour escapes.
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// PopupSize sizes the picker's popup to its lines, capped to the client.
// The padding covers the popup border, fzf's border and margin, the pointer
// column, and the prompt, separator and header lines.
func PopupSize(lines []string, clientW, clientH int) (w, h int) {
	for _, l := range lines {
		w = max(w, utf8.RuneCountInString(StripANSI(l)))
	}
	w, h = w+10, len(lines)+7
	w = min(max(w, utf8.RuneCountInString(PickHeader)+6, 40), clientW-4)
	h = min(h, clientH-2)
	return w, h
}

// PickHeader is the line above the picker's lines.
const PickHeader = "? needs you  ● working  ✓ done   ctrl-x on a session: kill it"

// Pick lets the user choose a session or a window with fzf and returns its
// switch-client target; "" when the picker was left without a choice. The
// cursor starts on the first window. self is the command that runs
// tmux-agent, for the picker's kill key and reload.
func Pick(ctx context.Context, rows []attention.PickRow, self string) (string, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return "", errors.New("fzf is not installed")
	}
	var in strings.Builder
	for _, r := range rows {
		in.WriteString(r.Session + "\t" + r.Target + "\t" + r.Display + "\n")
	}
	args := []string{"--ansi", "--height=100%", "--reverse", "--no-sort",
		"--delimiter=\t", "--with-nth=3", "--no-info", "--header=" + PickHeader,
		fmt.Sprintf("--bind=ctrl-x:execute-silent(%s kill-session {1})+reload(%s pick-rows)", self, self)}
	if len(rows) > 1 {
		args = append(args, "--bind=load:pos(2)") // the first session's first window
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
