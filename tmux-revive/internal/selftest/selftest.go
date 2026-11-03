// Package selftest lets a test binary stand in for the tmux-revive binary,
// so integration tests run the code under test, built with the test's own
// flags (the race detector included), without a separate build.
//
// A test package calls Main from TestMain, and Install on each sandbox: the
// sandbox then holds the test binary as tmux-revive, and every run of it
// there, from a tmux hook, a pane or a terminal, is tmux-revive.
package selftest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-revive/internal/cli"
	"github.com/influento/tmux-plugins/tmux-revive/internal/revive"
)

// envVar marks a run of the test binary as tmux-revive.
const envVar = "TMUX_REVIVE_SELFTEST"

// Main runs tmux-revive when the test binary was started as it, and the
// tests otherwise.
func Main(m *testing.M) {
	tmuxtest.SelfMain(m, envVar, func() int {
		return cli.Main("selftest", os.Args[1:], os.Stdout, os.Stderr)
	})
}

// Install puts the test binary into the sandbox as tmux-revive (see
// tmuxtest.Sandbox.InstallSelf). Call it before the server starts.
//
// tmux hooks run saves in the background, and one may still be writing
// when the test ends. Install's cleanup runs after the server's (cleanups
// run last-registered first), and waits for those saves, so that none
// writes into a sandbox being removed.
func Install(t testing.TB, sb *tmuxtest.Sandbox) {
	t.Helper()
	sb.InstallSelf(t, "tmux-revive", envVar)
	lockDir := filepath.Join(sb.RuntimeDir, "tmux-revive")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := revive.WaitIdle(ctx, lockDir); err != nil {
			t.Errorf("saves still running at the end of the test: %v", err)
		}
	})
}
