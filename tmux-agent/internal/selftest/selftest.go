// Package selftest lets a test binary stand in for the tmux-agent binary,
// so integration tests run the code under test, built with the test's own
// flags (the race detector included), without a separate build.
//
// A test package calls Main from TestMain, and Install on each sandbox: the
// sandbox then holds the test binary as tmux-agent, and every run of it in
// the sandbox, from a tmux hook, a pane or an agent stub, is tmux-agent.
package selftest

import (
	"os"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/cli"
)

// envVar marks a run of the test binary as tmux-agent.
const envVar = "TMUX_AGENT_SELFTEST"

// Main runs tmux-agent when the test binary was started as it, and the
// tests otherwise.
func Main(m *testing.M) {
	tmuxtest.SelfMain(m, envVar, func() int {
		return cli.Main("selftest", os.Args, os.Stdin, os.Stdout, os.Stderr)
	})
}

// Install puts the test binary into the sandbox as tmux-agent (see
// tmuxtest.Sandbox.InstallSelf). Call it before the server starts.
func Install(t testing.TB, sb *tmuxtest.Sandbox) {
	t.Helper()
	sb.InstallSelf(t, "tmux-agent", envVar)
}
