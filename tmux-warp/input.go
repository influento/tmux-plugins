package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
)

const inputTimeout = 10 * time.Second

// Global tmux user options used to pass command-prompt input into this binary.
// The query/label is stored via set-option (no shell), so input containing
// shell or tmux metacharacters is stored literally and never executed.
const (
	optQuery = "@warp_query"
	optLabel = "@warp_label"
)

// readOptionValue polls a global tmux user option until it is set, returning its
// value. The command-prompt template sets the option on submit; until then the
// option is unset, so we keep polling. ok is false on timeout (e.g. the user
// cancelled the prompt without submitting). Any other tmux failure, such as a
// server that is gone, is an error at once.
func readOptionValue(name string) (string, bool, error) {
	var c tmux.Client
	deadline := time.Now().Add(inputTimeout)
	for time.Now().Before(deadline) {
		v, set, err := c.Option(context.Background(), "-g", "", name)
		if err != nil {
			return "", false, err
		}
		if set {
			v = strings.TrimRight(v, "\n\r")
			debugLog("readOptionValue: %s=%q", name, v)
			return v, true, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	debugLog("readOptionValue: timeout on %s", name)
	return "", false, nil
}

// promptChar spawns a tmux command-prompt -1 and polls for the result.
// Used for label selection after the overlay is rendered. The keypress is stored
// in a tmux option (not run through a shell) and read back. If the prompt times
// out, a late keypress harmlessly sets the option, which the next run clears.
func promptChar(prompt string) (byte, bool, error) {
	if _, err := tmuxCmd("set-option", "-gu", optLabel); err != nil { // clear any stale value
		return 0, false, err
	}

	debugLog("promptChar: spawning command-prompt prompt=%q", prompt)
	// Not through tmuxCmd: without -b, command-prompt holds its client until
	// the prompt is dismissed, and the answer is read by polling below.
	cmd := exec.Command("tmux", "command-prompt", "-1", "-p", prompt, "set-option -g "+optLabel+" '%1'")
	if err := cmd.Start(); err != nil {
		return 0, false, fmt.Errorf("command-prompt: %w", err)
	}
	go cmd.Wait() // reaps the prompt's client; the answer arrives through the option

	s, ok, err := readOptionValue(optLabel)
	if err != nil || !ok || len(s) == 0 {
		return 0, false, err
	}
	return s[0], true, nil
}

var debugLogger *log.Logger

func initDebugLog() {
	// Off unless explicitly enabled; the log records search queries, so it must
	// not be written by default.
	if os.Getenv("TMUX_WARP_DEBUG") == "" {
		return
	}
	// O_NOFOLLOW, mode 0600, and a per-user path keep another local user from
	// reading our queries or symlinking the log onto one of our files.
	f, err := os.OpenFile(debugLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return
	}
	debugLogger = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
}

// debugLogPath picks a per-user, non-world-writable location for the debug log.
func debugLogPath() string {
	if p := os.Getenv("TMUX_WARP_LOG"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "tmux-warp.log")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("tmux-warp-%d.log", os.Getuid()))
}

func debugLog(format string, args ...any) {
	if debugLogger != nil {
		debugLogger.Printf(format, args...)
	}
}
