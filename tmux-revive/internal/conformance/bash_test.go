package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// bashDriver runs dotfiles' tmux-attach. tmux-resurrect's restore.sh is a
// stub that counts its runs and creates the sessions Saved named.
type bashDriver struct {
	dotfiles string
}

const restoreStub = `#!/bin/sh
echo run >> "$HOME/restore-runs"
[ -f "$HOME/saved-sessions" ] || exit 0
while IFS= read -r name; do
  [ -n "$name" ] && tmux new-session -d -s "$name"
done < "$HOME/saved-sessions"
exit 0
`

func (d *bashDriver) Name() string { return "bash" }

func (d *bashDriver) Setup(t *testing.T, sb *tmuxtest.Sandbox) {
	t.Helper()
	src := filepath.Join(d.dotfiles, "workstation", "scripts", "tmux-attach")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("bash driver: %v", err)
	}
	sb.Link(t, "tmux-attach", src)
	sb.Stub(t, "claude-tmux", tmuxtest.StubScript)
	dir := filepath.Join(sb.DataHome, "tmux", "plugins", "tmux-resurrect", "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore.sh"), []byte(restoreStub), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (d *bashDriver) Saved(t *testing.T, sb *tmuxtest.Sandbox, sessions ...string) {
	t.Helper()
	data := strings.Join(sessions, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sb.Home, "saved-sessions"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (d *bashDriver) Command() string { return "exec tmux-attach" }

func (d *bashDriver) Restores(t *testing.T, sb *tmuxtest.Sandbox) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sb.Home, "restore-runs"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run\n")
}
