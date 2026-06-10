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

// readStringFromFile polls a file until content appears (written by tmux command-prompt).
func readStringFromFile(path string) (string, bool) {
	deadline := time.Now().Add(inputTimeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			s := strings.TrimRight(string(data), "\n\r")
			debugLog("readStringFromFile: got %q from %s", s, path)
			return s, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	debugLog("readStringFromFile: timeout on %s", path)
	return "", false
}

// promptChar spawns a tmux command-prompt -1 and polls for the result.
// Used for label selection after the overlay is rendered.
func promptChar(prompt string) (byte, bool) {
	dir, err := os.MkdirTemp("", "tmux-warp-*")
	if err != nil {
		return 0, false
	}
	defer os.RemoveAll(dir)

	tmpFile := filepath.Join(dir, "key")
	cmdStr := fmt.Sprintf("run-shell 'printf %%1 >> %s'", tmpFile)

	debugLog("promptChar: spawning command-prompt prompt=%q tmpFile=%s", prompt, tmpFile)

	cmd := exec.Command("tmux", "command-prompt", "-1", "-p", prompt, cmdStr)
	if err := cmd.Start(); err != nil {
		debugLog("promptChar: start error: %v", err)
		return 0, false
	}
	go cmd.Wait()

	s, ok := readStringFromFile(tmpFile)
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
