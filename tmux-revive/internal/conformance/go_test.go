package conformance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-revive/internal/revive"
	"github.com/influento/tmux-plugins/tmux-revive/internal/selftest"
	"github.com/influento/tmux-plugins/tmux-revive/internal/snapshot"
)

func TestMain(m *testing.M) { selftest.Main(m) }

// goDriver runs tmux-revive attach, with tmux.conf running tmux-init last
// as dotfiles wires it, so its save hooks fire during the restore.
type goDriver struct{}

func (d *goDriver) Name() string { return "go" }

const goConfig = `set -g @revive_scrollback_interval 0
run-shell 'tmux-revive tmux-init'
`

func (d *goDriver) Setup(t *testing.T, sb *tmuxtest.Sandbox) {
	t.Helper()
	selftest.Install(t, sb)
	dir := filepath.Join(sb.ConfigHome, "tmux")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmux.conf"), []byte(goConfig), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (d *goDriver) store(sb *tmuxtest.Sandbox) *snapshot.Store {
	return &snapshot.Store{Dir: filepath.Join(sb.StateHome, "tmux-revive")}
}

// Saved writes a snapshot holding one window with one shell per session.
func (d *goDriver) Saved(t *testing.T, sb *tmuxtest.Sandbox, sessions ...string) {
	t.Helper()
	snap := &snapshot.Snapshot{Version: snapshot.Version, Time: time.Now().Add(-time.Minute)}
	for i, name := range sessions {
		snap.Sessions = append(snap.Sessions, snapshot.Session{
			Name: name,
			Windows: []snapshot.Window{{
				Index: 0, ID: fmt.Sprintf("w%02d", i), AutoRename: true, Width: 100, Height: 30, Active: true,
				Panes: []snapshot.Pane{{Index: 0, ID: fmt.Sprintf("p%02d", i), Cwd: sb.Home, Active: true}},
			}},
		})
	}
	if _, err := d.store(sb).Write(snap, ""); err != nil {
		t.Fatal(err)
	}
}

func (d *goDriver) Command() string { return "exec tmux-revive attach" }

// Settle waits for the saves that hooks started: the client's attach
// starts one, which can still be writing a snapshot as the test ends.
func (d *goDriver) Settle(t *testing.T, sb *tmuxtest.Sandbox) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := revive.WaitIdle(ctx, filepath.Join(sb.RuntimeDir, "tmux-revive")); err != nil {
		t.Errorf("saves still running after the test: %v", err)
	}
}

// Restores counts the restores in tmux-revive's log.
func (d *goDriver) Restores(t *testing.T, sb *tmuxtest.Sandbox) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(d.store(sb).Dir, "log"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), " restore from=")
}
