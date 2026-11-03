package tmuxtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SelfMain lets a test binary stand in for the program under test: started
// with envVar=1 it runs main and exits with its code; otherwise it runs the
// tests. Call it from TestMain.
func SelfMain(m *testing.M, envVar string, main func() int) {
	if os.Getenv(envVar) == "1" {
		os.Exit(main())
	}
	os.Exit(m.Run())
}

// InstallSelf puts the test binary into the sandbox as name, and sets
// envVar=1 so that every run of name in the sandbox, from a tmux hook, a
// pane or a stub, runs SelfMain's main: the code under test, built with
// the test's own flags (the race detector included). Call it before the
// server starts: the server keeps the environment it started with.
//
// Under -race, every such run writes its race reports to a file, and the
// test fails if any appear. The runs also skip the race runtime's 1 s
// sleep at exit, which would push every hook past its time budget.
func (s *Sandbox) InstallSelf(t testing.TB, name, envVar string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s.Link(t, name, exe)
	s.Setenv(envVar, "1")
	if s.raceDir != "" {
		return // one report folder for every program in the sandbox
	}
	s.raceDir = filepath.Join(s.Dir, "race")
	if err := os.MkdirAll(s.raceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0 log_path=" + filepath.Join(s.raceDir, "report"))
	s.Setenv("GORACE", opts)
	dir := s.raceDir
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
		t.Errorf("a program in the sandbox reported a race (%s):\n%s", filepath.Base(f), b)
	}
}
