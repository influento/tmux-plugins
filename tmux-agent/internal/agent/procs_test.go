package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeProcs points procRoot at a process table of its own for one test.
// Each process is pid: {ppid, state, comm, argv...}.
func fakeProcs(t *testing.T, procs map[int][]string) {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (%s) %s %s 1 1 0 -1", pid, p[2], p[1], p[0])
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
		cmdline := strings.Join(p[3:], "\x00") + "\x00"
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
}

func TestReadProc(t *testing.T) {
	fakeProcs(t, map[int][]string{
		7: {"3", "T", "a (b) c", "/opt/x/a (b) c", "--flag"},
	})
	p, err := readProc(7)
	if err != nil {
		t.Fatal(err)
	}
	if p.pid != 7 || p.ppid != 3 || p.state != 'T' || p.comm != "a (b) c" || !slices.Equal(p.argv, []string{"/opt/x/a (b) c", "--flag"}) {
		t.Errorf("proc = %+v", p)
	}
	if !isStopped(7) {
		t.Error("isStopped = false for state T")
	}
	if _, err := readProc(8); err == nil {
		t.Error("readProc of a missing process succeeded")
	}
}

func TestIsAgentProc(t *testing.T) {
	names := []string{"claude"}
	for _, c := range []struct {
		name string
		p    proc
		want bool
	}{
		{"native binary", proc{comm: "claude", argv: []string{"claude"}}, true},
		{"npm script under node", proc{comm: "node", argv: []string{"node", "/opt/bin/claude", "-p"}}, true},
		{"the launcher, named after the agent", proc{comm: "tmux-agent", argv: []string{"claude", "run", "claude"}}, false},
		{"a shell", proc{comm: "bash", argv: []string{"bash", "-c", "claude"}}, false},
		{"no arguments", proc{comm: "node"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := isAgentProc(c.p, names); got != c.want {
				t.Errorf("isAgentProc = %v, want %v", got, c.want)
			}
		})
	}
}

// The process table of a nested run: an outer claude under its launcher
// runs a shell for a tool call, and the shell runs claude -p, whose hook
// runs in sh.
func TestAgentAncestors(t *testing.T) {
	fakeProcs(t, map[int][]string{
		10: {"1", "S", "zsh", "zsh"},
		11: {"10", "S", "tmux-agent", "claude", "run", "claude"},
		12: {"11", "S", "claude", "claude"},
		13: {"12", "S", "zsh", "zsh", "-c", "claude -p hi"},
		14: {"13", "S", "node", "node", "/opt/bin/claude", "-p", "hi"},
		15: {"14", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
		20: {"12", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
	})
	names := []string{"claude"}
	for _, c := range []struct {
		name            string
		from, stop, max int
		want            int
	}{
		{"the run's own hook", 20, 11, 2, 1},
		{"the nested agent's hook", 15, 11, 2, 2},
		{"counting stops at max", 15, 11, 1, 1},
		{"without a launcher, to init", 15, 0, 9, 2},
		{"the launcher's parent", 10, 0, 9, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := agentAncestors(c.from, names, c.stop, c.max); got != c.want {
				t.Errorf("agentAncestors(%d) = %d, want %d", c.from, got, c.want)
			}
		})
	}
}
