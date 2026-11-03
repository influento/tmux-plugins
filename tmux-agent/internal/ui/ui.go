// Package ui is what tmux-agent shows outside tmux's own status bar: the
// session picker through fzf, the popup it opens in, and desktop
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

// PopupSize sizes the picker's popup to its rows, capped to the client.
// The padding covers the popup border, fzf's border and margin, the pointer
// column, and the prompt, separator and header lines.
func PopupSize(rows []attention.SessionRow, clientW, clientH int) (w, h int) {
	for _, r := range rows {
		w = max(w, utf8.RuneCountInString(StripANSI(r.Display)))
	}
	w, h = w+10, len(rows)+7
	w = min(max(w, 40), clientW-4)
	h = min(h, clientH-2)
	return w, h
}

// PickHeader is the line above the picker's rows.
const PickHeader = "sessions: ? needs you  ● working  ✓ done   ctrl-x: kill"

// Pick lets the user choose a session with fzf and returns its name; ""
// when the picker was left without a choice. self is the command that runs
// tmux-agent, for the picker's kill key and reload.
func Pick(ctx context.Context, rows []attention.SessionRow, self string) (string, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return "", errors.New("fzf is not installed")
	}
	var in strings.Builder
	for _, r := range rows {
		in.WriteString(r.Session + "\t" + r.Display + "\n")
	}
	cmd := exec.CommandContext(ctx, "fzf", "--ansi", "--height=100%", "--reverse",
		"--delimiter=\t", "--with-nth=2", "--no-info", "--header="+PickHeader,
		fmt.Sprintf("--bind=ctrl-x:execute-silent(%s kill-session {1})+reload(%s rows)", self, self))
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
	name, _, _ := strings.Cut(strings.TrimSuffix(out.String(), "\n"), "\t")
	return name, nil
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
