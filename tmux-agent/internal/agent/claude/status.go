package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// Claude Code keeps a file for each of its running processes,
// sessions/<pid>.json in its folder, with the process's session and its
// status: "busy" while a turn runs, "waiting" on a dialog, "idle" at the
// prompt, "shell" at the prompt while a background shell it started runs
// (seen in 2.1.330, which counts it as idle too). statusUpdatedAt is when
// the status last changed, in Unix milliseconds. A turn the user ends
// with Esc before the first token sends no hook and writes no transcript
// line; only this file tells. (Seen in 2.1.325.) name is the session's name, the address that
// SendMessage and ListAgents use. (Seen in 2.1.329.)

// processFile is what tmux-agent reads of the file.
type processFile struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Since     int64  `json:"statusUpdatedAt"`
	Name      string `json:"name"`
}

// StatusFile is the file Claude Code keeps for its process pid.
func (Claude) StatusFile(pid int, getenv func(string) string) string {
	return filepath.Join(configDir(getenv), "sessions", strconv.Itoa(pid)+".json")
}

// process reads the file of process pid; ok is false when it is missing,
// unreadable or of another session.
func (c Claude) process(pid int, session string, getenv func(string) string) (processFile, bool) {
	data, err := os.ReadFile(c.StatusFile(pid, getenv))
	if err != nil {
		return processFile{}, false
	}
	var f processFile
	if json.Unmarshal(data, &f) != nil || f.SessionID != session {
		return processFile{}, false
	}
	return f, true
}

var idleStatuses = []string{"idle", "shell"}

// IdleSince reports when Claude Code's process pid went idle in session:
// ok is false while it works or waits on a dialog, or when its file is
// missing, unreadable or of another session.
func (c Claude) IdleSince(pid int, session string, getenv func(string) string) (time.Time, bool) {
	f, ok := c.process(pid, session, getenv)
	if !ok || !slices.Contains(idleStatuses, f.Status) || f.Since <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(f.Since), true
}

// sendMessage is Claude Code's tool that sends to another session.
const sendMessage = "SendMessage"

// PeerAddress: a session's address for SendMessage is its name.
func (c Claude) PeerAddress(pid int, session string, getenv func(string) string) (via, to string, ok bool) {
	f, ok := c.process(pid, session, getenv)
	if !ok || f.Name == "" {
		return "", "", false
	}
	return sendMessage, f.Name, true
}
