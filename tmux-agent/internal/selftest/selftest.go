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
	"path/filepath"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/cli"
)

// envVar marks a run of the test binary as tmux-agent.
const envVar = "TMUX_AGENT_SELFTEST"

// Main runs tmux-agent when the test binary was started as it, and the
// tests otherwise.
func Main(m *testing.M) {
	if os.Getenv(envVar) == "1" {
		os.Exit(cli.Main("selftest", os.Args, os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// Install puts the test binary into the sandbox as tmux-agent. Call it
// before the server starts: the server keeps the environment it started
// with.
//
// Under -race, every run of tmux-agent in the sandbox writes its race
// reports to a file, and the test fails if any appear. Those runs also skip
// the race runtime's 1 s sleep at exit, which would push every hook past
// its time budget.
func Install(t testing.TB, sb *tmuxtest.Sandbox) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sb.Link(t, "tmux-agent", exe)
	sb.Setenv(envVar, "1")

	dir := filepath.Join(sb.Dir, "race")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0 log_path=" + filepath.Join(dir, "report"))
	sb.Setenv("GORACE", opts)
	t.Cleanup(func() { checkRaces(t, dir) })
}

// checkRaces fails the test with every race report a sandbox run left.
func checkRaces(t testing.TB, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "report.*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		t.Errorf("tmux-agent in the sandbox reported a race (%s):\n%s", filepath.Base(f), b)
	}
}
