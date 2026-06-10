package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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
// option is unset and show-options exits non-zero, so we keep polling. Returns
// false on timeout (e.g. the user cancelled the prompt without submitting).
func readOptionValue(name string) (string, bool) {
	deadline := time.Now().Add(inputTimeout)
	for time.Now().Before(deadline) {
		out, err := tmuxCmd("show-options", "-gv", name)
		if err == nil {
			s := strings.TrimRight(out, "\n\r")
			debugLog("readOptionValue: %s=%q", name, s)
			return s, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	debugLog("readOptionValue: timeout on %s", name)
	return "", false
}

// promptChar spawns a tmux command-prompt -1 and polls for the result.
// Used for label selection after the overlay is rendered. The keypress is stored
// in a tmux option (not run through a shell) and read back. If the prompt times
// out, a late keypress harmlessly sets the option, which the next run clears.
func promptChar(prompt string) (byte, bool) {
	tmuxCmd("set-option", "-gu", optLabel) // clear any stale value

	debugLog("promptChar: spawning command-prompt prompt=%q", prompt)
	cmd := exec.Command("tmux", "command-prompt", "-1", "-p", prompt, "set-option -g "+optLabel+" '%1'")
	if err := cmd.Start(); err != nil {
		debugLog("promptChar: start error: %v", err)
		return 0, false
	}
	go cmd.Wait()

	s, ok := readOptionValue(optLabel)
	if !ok || len(s) == 0 {
		return 0, false
	}
	return s[0], true
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
