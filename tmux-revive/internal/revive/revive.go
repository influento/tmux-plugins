// Package revive saves all of one tmux server on every change and brings
// it back after a restart: every session, window and pane, with layouts,
// names, folders, programs, scrollback and each pane's @revive_cmd.
//
// What closes tmux from outside (reboot, logout, kill-server, a crash)
// comes back in full. What the user closes inside tmux stays closed: a
// save that finds the server gone writes nothing, and a session closed
// with forget-session leaves the snapshot before it goes.
package revive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/tmux-revive/internal/snapshot"
)

// The tmux options tmux-revive reads and writes.
const (
	// OptReady is set on a server once attach or restore has put the saved
	// state back, or found live work it must not restore onto. Saves wait
	// for it, so that a server started some other way cannot replace the
	// snapshot a restore still needs.
	OptReady = "@revive_ready"
	// OptForgotten marks a session closed with forget-session: saves leave
	// it out.
	OptForgotten = "@revive_forgotten"
	// OptWindowID is a window's id, kept across restarts; tmux-agent keys
	// its history by it.
	OptWindowID = "@revive_id"
	// OptPaneID is a pane's id, kept across restarts; it names the pane's
	// scrollback file.
	OptPaneID = "@revive_pane"
	// OptCommand is what restore types into a pane's shell. tmux-agent
	// sets it for an agent's resume; anything else may set it too.
	OptCommand = "@revive_cmd"
	// OptPrograms lists the programs a pane may run that restore starts
	// again, space separated.
	OptPrograms = "@revive_programs"
	// OptScrollbackLines is how many lines of each pane's scrollback a
	// capture keeps.
	OptScrollbackLines = "@revive_scrollback_lines"
	// OptScrollbackInterval is the seconds between scrollback captures; 0
	// turns them off.
	OptScrollbackInterval = "@revive_scrollback_interval"
	// OptSnapshots is how many snapshots a save keeps, at least 1.
	OptSnapshots = "@revive_snapshots"
	// OptSkipSessions lists sessions attach never lands on and save leaves
	// out (globs, space separated). tmux-agent reads it too.
	OptSkipSessions = "@skip_sessions"
)

// Defaults for options tmux.conf does not set.
const (
	// DefaultPrograms is tmux-resurrect's default list plus ssh.
	DefaultPrograms           = "vi vim nvim emacs man less more tail top htop irssi weechat mutt ssh"
	DefaultScrollbackLines    = 2000
	DefaultScrollbackInterval = 300
	DefaultSkipSessions       = "ov-*"
)

// Reviver saves and restores one tmux server.
type Reviver struct {
	Tmux  *tmux.Client
	Store *snapshot.Store
	// LockDir holds the locks: under $XDG_RUNTIME_DIR.
	LockDir string
	// Self is the tmux-revive executable, which a restored pane runs to
	// print its scrollback before its shell starts.
	Self string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Stopping reports whether the machine or the login session is shutting
	// down; SystemdStopping when nil.
	Stopping func(ctx context.Context) bool
}

func (r *Reviver) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reviver) stopping(ctx context.Context) bool {
	if r.Stopping != nil {
		return r.Stopping(ctx)
	}
	return SystemdStopping(ctx)
}

func (r *Reviver) logf(format string, args ...any) {
	r.Store.Logf(r.now(), format, args...)
}

// StateDir is tmux-revive's folder for snapshots and scrollback.
func StateDir(getenv func(string) string) string {
	if d := getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "tmux-revive")
	}
	return filepath.Join(getenv("HOME"), ".local", "state", "tmux-revive")
}

// RuntimeDir is tmux-revive's folder for locks: under $XDG_RUNTIME_DIR,
// else a per-user folder in the temp directory.
func RuntimeDir(getenv func(string) string) string {
	if d := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(d) {
		return filepath.Join(d, "tmux-revive")
	}
	return filepath.Join(os.TempDir(), "tmux-revive-"+strconv.Itoa(os.Getuid()))
}

// SystemdStopping reports whether systemd is stopping the machine or the
// user's service manager (the last login session is ending). A save that
// would lose panes then writes nothing: at shutdown, panes die after tmux
// or in any order, and the snapshot must keep them. Without systemctl it
// reports false.
func SystemdStopping(ctx context.Context) bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, args := range [][]string{{"is-system-running"}, {"--user", "is-system-running"}} {
		// is-system-running exits non-zero for every state but running,
		// so only its output counts.
		out, _ := exec.CommandContext(ctx, "systemctl", args...).Output()
		if strings.TrimSpace(string(out)) == "stopping" {
			return true
		}
	}
	return false
}

// errNoServer is a tmux call that found no server: none started, or it
// has gone.
var errNoServer = errors.New("no tmux server")

// noServer turns tmux's errors for a missing or dying server into
// errNoServer.
func noServer(err error) error {
	te, ok := errors.AsType[*tmux.Error](err)
	if !ok {
		return err
	}
	for _, s := range []string{"no server running", "error connecting to", "server exited", "lost server"} {
		if strings.Contains(te.Stderr, s) {
			return fmt.Errorf("%w: %w", errNoServer, err)
		}
	}
	return err
}

// option reads a global user option, with def when it is unset.
func (r *Reviver) option(ctx context.Context, name, def string) (string, error) {
	v, ok, err := r.Tmux.Option(ctx, "-g", "", name)
	if err != nil {
		return "", noServer(err)
	}
	if !ok {
		return def, nil
	}
	return v, nil
}

// intOption reads a global numeric option, with def when it is unset or
// not a number.
func (r *Reviver) intOption(ctx context.Context, name string, def int) int {
	v, err := r.option(ctx, name, "")
	if err != nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return def
	}
	return n
}

// skipper returns a test for the sessions attach never lands on and save
// leaves out.
func (r *Reviver) skipper(ctx context.Context) func(string) bool {
	v, err := r.option(ctx, OptSkipSessions, DefaultSkipSessions)
	if err != nil {
		v = DefaultSkipSessions
	}
	patterns := strings.Fields(v)
	return func(name string) bool {
		for _, p := range patterns {
			if ok, _ := path.Match(p, name); ok {
				return true
			}
		}
		return false
	}
}

// ScrollbackInterval is the seconds between scrollback captures; 0 means
// none.
func (r *Reviver) ScrollbackInterval(ctx context.Context) int {
	return r.intOption(ctx, OptScrollbackInterval, DefaultScrollbackInterval)
}

// markReady sets @revive_ready. It runs even when ctx has ended, as when
// typing used up the time attach had: a server left unmarked never saves.
// The tmux client's own timeout bounds it.
func (r *Reviver) markReady(ctx context.Context) error {
	_, err := r.Tmux.Run(context.WithoutCancel(ctx), "set-option", "-g", OptReady, "1")
	return noServer(err)
}

// newID makes a window or pane id.
func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// lockPath is the file of one of tmux-revive's locks.
func (r *Reviver) lockPath(name string) string {
	return filepath.Join(r.LockDir, name+".lock")
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
