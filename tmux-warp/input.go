package main

import (
	"fmt"
	"os"
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

	// tmux command-prompt -1 reads exactly one key.
	// %% is the tmux escape for a literal %, and '%1' captures the first argument.
	cmd := fmt.Sprintf("run-shell \"printf '%%1' >> %s\"", tmpFile)
	if _, err := tmuxCmd("command-prompt", "-t", pr.paneID, "-1", "-p", "", cmd); err != nil {
		return 0, false
	}

	// Poll the temp file for the character (tmux writes it asynchronously).
	deadline := time.Now().Add(inputTimeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(tmpFile)
		if err == nil && len(data) > 0 {
			os.Remove(tmpFile)
			return data[0], true
		}
		time.Sleep(10 * time.Millisecond)
	}

	os.Remove(tmpFile)
	return 0, false
}

// IsCancel returns true if the byte is Escape (0x1b) or Ctrl+C (0x03).
func IsCancel(b byte) bool {
	return b == 0x1b || b == 0x03
}
