package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const inputTimeout = 10 * time.Second

// readCharFromFile polls a file until a byte appears (written by tmux command-prompt).
func readCharFromFile(path string) (byte, bool) {
	deadline := time.Now().Add(inputTimeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			debugLog("readCharFromFile: got %q (0x%02x) from %s", string(data[0]), data[0], path)
			return data[0], true
		}
		time.Sleep(10 * time.Millisecond)
	}
	debugLog("readCharFromFile: timeout on %s", path)
	return 0, false
}

// promptChar spawns a tmux command-prompt -1 and polls for the result.
// Used for label selection after the overlay is rendered.
func promptChar(paneID string, prompt string) (byte, bool) {
	dir, err := os.MkdirTemp("", "tmux-warp-*")
	if err != nil {
		return 0, false
	}
	defer os.RemoveAll(dir)

	tmpFile := filepath.Join(dir, "key")
	cmdStr := fmt.Sprintf("run-shell \"printf '%%1' >> %s\"", tmpFile)

	debugLog("promptChar: spawning command-prompt prompt=%q tmpFile=%s", prompt, tmpFile)

	cmd := exec.Command("tmux", "command-prompt", "-t", paneID, "-1", "-p", prompt, cmdStr)
	if err := cmd.Start(); err != nil {
		debugLog("promptChar: start error: %v", err)
		return 0, false
	}
	go cmd.Wait()

	return readCharFromFile(tmpFile)
}

var debugLogger *log.Logger

func initDebugLog() {
	f, err := os.OpenFile("/tmp/tmux-warp.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return
	}
	debugLogger = log.New(f, "", log.LstdFlags|log.Lmicroseconds)
}

func debugLog(format string, args ...any) {
	if debugLogger != nil {
		debugLogger.Printf(format, args...)
	}
}
