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

// PromptReader reads single characters via tmux command-prompt and a temp file.
// This avoids racing with the shell for TTY input.
type PromptReader struct {
	tmpDir string
	paneID string
}

func newPromptReader(paneID string) (*PromptReader, error) {
	dir, err := os.MkdirTemp("", "tmux-warp-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	return &PromptReader{tmpDir: dir, paneID: paneID}, nil
}

func (pr *PromptReader) Close() error {
	return os.RemoveAll(pr.tmpDir)
}

// ReadChar prompts for a single character using tmux command-prompt.
// Returns the char and true on success, or (0, false) on timeout/cancel.
func (pr *PromptReader) ReadChar() (byte, bool) {
	tmpFile := filepath.Join(pr.tmpDir, fmt.Sprintf("key-%d", time.Now().UnixNano()))

	debugLog("ReadChar: spawning command-prompt, tmpFile=%s", tmpFile)

	// Spawn command-prompt asynchronously (fire-and-forget like tmux-jump).
	// command-prompt -1 reads exactly one key from the user via tmux's status line.
	cmdStr := fmt.Sprintf("run-shell \"printf '%%1' >> %s\"", tmpFile)
	cmd := exec.Command("tmux", "command-prompt", "-t", pr.paneID, "-1", "-p", "", cmdStr)
	if err := cmd.Start(); err != nil {
		debugLog("ReadChar: command-prompt start error: %v", err)
		return 0, false
	}
	// Don't wait — command-prompt returns immediately, the prompt is async.
	go cmd.Wait()

	debugLog("ReadChar: polling for input...")

	// Poll the temp file for the character.
	deadline := time.Now().Add(inputTimeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(tmpFile)
		if err == nil && len(data) > 0 {
			debugLog("ReadChar: got char %q (0x%02x)", string(data[0]), data[0])
			os.Remove(tmpFile)
			return data[0], true
		}
		time.Sleep(10 * time.Millisecond)
	}

	debugLog("ReadChar: timeout")
	os.Remove(tmpFile)
	return 0, false
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

// IsCancel returns true if the byte is Escape (0x1b) or Ctrl+C (0x03).
func IsCancel(b byte) bool {
	return b == 0x1b || b == 0x03
}
