package agent

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeProcs points procRoot at a process table of its own for one test.
// Each process is pid: {ppid, state, comm, argv...}.
// fixtureServer plays the tmux server in the fixtures: their panes' first
// processes hang under pid 1.
const fixtureServer = 1

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
		{"a zombie", proc{comm: "claude", state: 'Z', argv: []string{""}}, false},
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

// A hook reads the arguments of the agent it runs under, however the agent
// was started, and finds an agent anywhere below a pane's shell.
func TestAgentArgsAndBelow(t *testing.T) {
	fakeProcs(t, map[int][]string{
		10: {"1", "S", "zsh", "zsh"},
		11: {"10", "S", "claude", "claude", "-p", "hi"},
		12: {"11", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
		20: {"1", "S", "zsh", "zsh"},
		21: {"20", "S", "node", "node", "/opt/bin/claude", "--resume", "x"},
		22: {"21", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
		30: {"1", "S", "zsh", "zsh"},
		31: {"30", "S", "bash", "bash", "-c", "make"},
		32: {"31", "T", "claude", "claude"},
		40: {"1", "S", "zsh", "zsh"},
		41: {"40", "S", "vim", "vim"},
	})
	names := []string{"claude"}
	for _, c := range []struct {
		name string
		from int
		want []string
	}{
		{"native binary", 12, []string{"-p", "hi"}},
		{"npm script under node", 22, []string{"--resume", "x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := nearestAgentArgs(c.from, names, 0, fixtureServer)
			if !ok || !slices.Equal(got, c.want) {
				t.Errorf("nearestAgentArgs(%d) = %q, %v; want %q", c.from, got, ok, c.want)
			}
		})
	}
	if _, ok := nearestAgentArgs(41, names, 0, fixtureServer); ok {
		t.Error("nearestAgentArgs found an agent above vim")
	}
	tree := processTree()
	for _, c := range []struct {
		name string
		pane int
		want bool
	}{
		{"agent under the shell", 10, true},
		{"stopped agent two levels down", 30, true},
		{"no agent", 40, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := hasAgentBelow(c.pane, names, tree); got != c.want {
				t.Errorf("hasAgentBelow(%d) = %v, want %v", c.pane, got, c.want)
			}
		})
	}
}

// A hook counts for a run only when the run is above it: a headless run
// whose hook process was killed (a Stop hook's timeout kills its process
// group) sends its last hooks from under init, with the pane's run still
// in its environment.
func TestNestedFrom(t *testing.T) {
	fakeProcs(t, map[int][]string{
		10: {"1", "S", "zsh", "zsh"},
		11: {"10", "S", "tmux-agent", "claude", "run", "claude"},
		12: {"11", "S", "claude", "claude"},
		13: {"12", "S", "zsh", "zsh", "-c", "claude -p hi"},
		14: {"13", "S", "node", "node", "/opt/bin/claude", "-p", "hi"},
		15: {"14", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
		20: {"12", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
		30: {"1", "S", "claude", "claude", "-p", "review", "--agent", "rev"},
		31: {"30", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
	})
	names := []string{"claude"}
	for _, c := range []struct {
		name      string
		from, run int
		want      bool
	}{
		{"the run's own hook", 20, 11, false},
		{"the nested agent's hook", 15, 11, true},
		{"a run's hook gone from under it", 31, 11, true},
		{"no launcher", 31, 0, false},
		{"nested, no launcher", 15, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := nestedFrom(c.from, names, c.run, fixtureServer); got != c.want {
				t.Errorf("nestedFrom(%d, run %d) = %v, want %v", c.from, c.run, got, c.want)
			}
		})
	}
}

// fakeStarts gives processes of fakeProcs their start times, in clock
// ticks after boot.
func fakeStarts(t *testing.T, starts map[int]int) {
	t.Helper()
	for pid, at := range starts {
		path := filepath.Join(procRoot, strconv.Itoa(pid), "stat")
		stat, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Fields 3 to 21 after the name; the start is the 22nd field.
		fields := strings.Fields(string(stat))
		for len(fields) < 21 {
			fields = append(fields, "0")
		}
		fields = append(fields[:21], strconv.Itoa(at))
		if err := os.WriteFile(path, []byte(strings.Join(fields, " ")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The agent starts an event's hooks together: the calling hook's
// siblings that started with it and still run are the event's other
// hooks; a shell that runs in the background since earlier is none.
func TestOtherHooks(t *testing.T) {
	names := []string{"claude"}
	table := map[int][]string{
		10: {"1", "S", "zsh", "zsh"},
		11: {"10", "S", "tmux-agent", "claude", "run", "claude"},
		12: {"11", "S", "claude", "claude"},
		20: {"12", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
		22: {"12", "S", "zsh", "zsh", "-c", "make watch"},
		23: {"12", "Z", "sh", "sh"},
	}
	starts := map[int]int{11: 10, 12: 20, 20: 5000, 22: 1000, 23: 5000}
	t.Run("none", func(t *testing.T) {
		fakeProcs(t, table)
		fakeStarts(t, starts)
		if got := otherHooks(20, names, fixtureServer); len(got) != 0 {
			t.Errorf("otherHooks = %v, want none", got)
		}
	})
	// A hook run outside the agent's pane (a test's, under a Stop hook of
	// a real agent) counts none of that agent's hooks.
	t.Run("an agent outside the pane", func(t *testing.T) {
		table := maps.Clone(table)
		table[21] = []string{"12", "S", "bash", "bash", ".claude/hooks/review"}
		fakeProcs(t, table)
		starts := maps.Clone(starts)
		starts[21] = 5001
		fakeStarts(t, starts)
		if got := otherHooks(20, names, 40); len(got) != 0 { // no pane of server 40 runs them
			t.Errorf("otherHooks = %v, want none", got)
		}
	})
	t.Run("a review hook", func(t *testing.T) {
		table := maps.Clone(table)
		table[21] = []string{"12", "S", "bash", "bash", ".claude/hooks/review"}
		table[30] = []string{"21", "S", "claude", "claude", "-p", "review"}
		fakeProcs(t, table)
		starts := maps.Clone(starts)
		starts[21], starts[30] = 5001, 5100
		fakeStarts(t, starts)
		if got := otherHooks(20, names, fixtureServer); !slices.Equal(got, []int{21}) {
			t.Errorf("otherHooks = %v, want [21]", got)
		}
	})
}

// A hook finds its agents only in a pane of its server: one sent from
// outside every pane (a test's, run by a claude -p of a review inside an
// agent's session) has none, however many agents run above it.
func TestHookOutsideItsPane(t *testing.T) {
	const server = 5
	fakeProcs(t, map[int][]string{
		5:  {"1", "S", "tmux", "tmux"},
		10: {"5", "S", "zsh", "zsh"},
		11: {"10", "S", "claude", "claude", "--agent", "role-w"},
		12: {"11", "S", "sh", "sh", "-c", "tmux-agent hook claude"},
		60: {"1", "S", "claude", "claude"},
		61: {"60", "S", "python3", "python3", "review"},
		62: {"61", "S", "claude", "claude", "-p", "review", "--agent", "role-r"},
		63: {"62", "S", "go", "go", "test"},
		64: {"63", "S", "conformance.test", "conformance.test"},
	})
	names := []string{"claude"}
	t.Run("nested", func(t *testing.T) {
		if nestedFrom(12, names, 0, server) {
			t.Error("the pane's agent's hook is nested")
		}
		if nestedFrom(64, names, 0, server) {
			t.Error("a hook outside the pane is nested")
		}
	})
	t.Run("arguments", func(t *testing.T) {
		if got, ok := nearestAgentArgs(12, names, 0, server); !ok || !slices.Equal(got, []string{"--agent", "role-w"}) {
			t.Errorf("the pane's agent's arguments = %q, %v; want [--agent role-w]", got, ok)
		}
		if got, ok := nearestAgentArgs(64, names, 0, server); ok {
			t.Errorf("a hook outside the pane has an agent with arguments %q", got)
		}
	})
	t.Run("pid", func(t *testing.T) {
		if got := nearestAgentPid(12, names, server); got != 11 {
			t.Errorf("the pane's agent = %d, want 11", got)
		}
		if got := nearestAgentPid(64, names, server); got != 0 {
			t.Errorf("a hook outside the pane has agent %d, want none", got)
		}
	})
}
