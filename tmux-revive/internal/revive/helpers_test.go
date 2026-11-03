package revive

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeProc writes a process table with the given processes: pid → tpgid
// and command line.
func fakeProc(t *testing.T, procs map[int]struct {
	tpgid int
	argv  []string
}) {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		// The command name may hold spaces and parentheses.
		stat := strconv.Itoa(pid) + " (a (b) c) S 1 " + strconv.Itoa(pid) + " " + strconv.Itoa(pid) + " 34816 " + strconv.Itoa(p.tpgid) + " 4194304"
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := strings.Join(p.argv, "\x00") + "\x00"
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmd), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
}

type proc = struct {
	tpgid int
	argv  []string
}

func TestProgramAndShell(t *testing.T) {
	fakeProc(t, map[int]proc{
		10: {tpgid: 10, argv: []string{"-zsh"}},
		20: {tpgid: 21, argv: []string{"-zsh"}},
		21: {tpgid: 21, argv: []string{"nvim", "notes file.md"}},
		30: {tpgid: 30, argv: []string{"sh", "-c", "exec tmux-revive paint x"}},
		40: {tpgid: 41, argv: []string{"-zsh"}},
		41: {tpgid: 41, argv: []string{"/usr/bin/make", "-j8"}},
		50: {tpgid: 50, argv: []string{"/bin/bash", "/home/u/bin/slowsh"}},
		60: {tpgid: 0, argv: []string{"-zsh"}},
	})
	allowed := map[string]bool{"nvim": true}
	for _, c := range []struct {
		name    string
		pid     int
		program []string
		shell   bool
	}{
		{"a shell at its prompt", 10, nil, true},
		{"nvim with an argument", 20, []string{"nvim", "notes file.md"}, false},
		{"tmux starting the pane's command", 30, nil, false},
		{"a program not on the list", 40, nil, false},
		{"a shell script run by bash", 50, nil, true},
		{"no foreground group", 60, nil, false},
		{"a process that is gone", 99, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := program(c.pid, allowed); !slices.Equal(got, c.program) {
				t.Errorf("program = %q, want %q", got, c.program)
			}
			if got := shellUp(c.pid, "/usr/bin/zsh"); got != c.shell {
				t.Errorf("shellUp = %v, want %v", got, c.shell)
			}
		})
	}
}

func TestShellJoin(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"nvim", "notes.md"}, "nvim notes.md"},
		{[]string{"less", "a file"}, "less 'a file'"},
		{[]string{"ssh", "host-b", "it's"}, `ssh host-b 'it'\''s'`},
		{[]string{"man", ""}, "man ''"},
		{[]string{"echo", "$HOME;rm"}, "echo '$HOME;rm'"},
	} {
		if got := shellJoin(c.in); got != c.want {
			t.Errorf("shellJoin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanScrollback(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"\n\n", ""},
		{"a  \nb\t \n\n\n", "a\nb\t\n"},
		{"\x1b[31mred\x1b[0m   \n", "\x1b[31mred\x1b[0m\n"},
	} {
		if got := string(cleanScrollback(c.in)); got != c.want {
			t.Errorf("cleanScrollback(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBareAndLastUsed(t *testing.T) {
	skip := func(n string) bool { return strings.HasPrefix(n, "ov-") }
	for _, c := range []struct {
		name     string
		sessions []liveSession
		bare     bool
		last     string
	}{
		{"no server", nil, true, ""},
		{"one-window main", []liveSession{{name: "main", windows: 1}}, true, "main"},
		{"main with two windows", []liveSession{{name: "main", windows: 2}}, false, "main"},
		{"another session, a tie: the first", []liveSession{{name: "main", windows: 1}, {name: "proj-x", windows: 1}}, false, "main"},
		{"attached last wins", []liveSession{
			{name: "proj-a", lastAttached: 20, activity: 1},
			{name: "proj-b", lastAttached: 10, activity: 99},
		}, false, "proj-a"},
		{"then activity", []liveSession{
			{name: "proj-a", activity: 5},
			{name: "proj-b", activity: 7},
		}, false, "proj-b"},
		{"overlay never", []liveSession{{name: "ov-nvim", lastAttached: 50}}, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := bare(c.sessions); got != c.bare {
				t.Errorf("bare = %v, want %v", got, c.bare)
			}
			if got := lastUsed(c.sessions, skip); got != c.last {
				t.Errorf("lastUsed = %q, want %q", got, c.last)
			}
		})
	}
}

// build leaves forgotten sessions out, saves a linked window once, and
// keeps a name only when automatic-rename is off.
func TestBuild(t *testing.T) {
	panes := []livePane{
		{session: "proj-a", window: "@1", winIndex: 0, windowID: "w1", windowName: "zsh", autoRename: true, pane: "%1", paneID: "p1"},
		{session: "proj-a", window: "@2", winIndex: 1, windowID: "w2", windowName: "edit", pane: "%2", paneID: "p2"},
		{session: "proj-a", window: "@2", winIndex: 1, windowID: "w2", windowName: "edit", pane: "%3", paneID: "p3", paneIndex: 1},
		{session: "proj-b", window: "@2", winIndex: 4, windowID: "w2", windowName: "edit", pane: "%2", paneID: "p2"},
		{session: "proj-b", window: "@3", winIndex: 5, windowID: "w3", pane: "%4", paneID: "p4"},
		{session: "proj-c", forgotten: true, window: "@5", windowID: "w5", pane: "%5", paneID: "p5"},
	}
	snap := build(panes, nil)
	if len(snap.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want proj-a and proj-b", snap.Sessions)
	}
	a, b := snap.Sessions[0], snap.Sessions[1]
	if len(a.Windows) != 2 || a.Windows[0].Name != "" || a.Windows[1].Name != "edit" || len(a.Windows[1].Panes) != 2 {
		t.Errorf("proj-a = %+v", a)
	}
	if len(b.Windows) != 1 || b.Windows[0].ID != "w3" {
		t.Errorf("proj-b = %+v, want only w3 (w2 is saved with proj-a)", b)
	}
}
