package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Claude Code keeps a file for each of its running processes,
// sessions/<pid>.json in its folder, with the process's session and its
// status: "busy" while a turn runs, "waiting" on a dialog, "idle" at the
// prompt. statusUpdatedAt is when the status last changed, in Unix
// milliseconds. A turn the user ends with Esc before the first token
// sends no hook and writes no transcript line; only this file tells.
// (Seen in 2.1.325.)

// StatusFile is the file Claude Code keeps for its process pid.
func (Claude) StatusFile(pid int, getenv func(string) string) string {
	return filepath.Join(configDir(getenv), "sessions", strconv.Itoa(pid)+".json")
}

// IdleSince reports when Claude Code's process pid went idle in session:
// ok is false while it works or waits on a dialog, or when its file is
// missing, unreadable or of another session.
func (c Claude) IdleSince(pid int, session string, getenv func(string) string) (time.Time, bool) {
	data, err := os.ReadFile(c.StatusFile(pid, getenv))
	if err != nil {
		return time.Time{}, false
	}
	var s struct {
		SessionID string `json:"sessionId"`
		Status    string `json:"status"`
		Since     int64  `json:"statusUpdatedAt"`
	}
	if json.Unmarshal(data, &s) != nil || s.SessionID != session || s.Status != "idle" || s.Since <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(s.Since), true
}
