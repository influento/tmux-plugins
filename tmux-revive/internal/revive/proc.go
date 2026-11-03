package revive

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// procRoot is where the process table is read; tests may point it at a
// fixture.
var procRoot = "/proc"

// foreground returns the leader of the process group in the foreground of
// the terminal that pid controls, and its command line. leader is 0 when
// the terminal has no foreground group.
func foreground(pid int) (leader int, argv []string, err error) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, nil, err
	}
	// The command name, in parentheses, may hold spaces; the fields after
	// it are: state ppid pgrp session tty_nr tpgid ...
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, nil, fmt.Errorf("stat of %d: no command name", pid)
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 6 {
		return 0, nil, fmt.Errorf("stat of %d: %d fields", pid, len(f))
	}
	tpgid, err := strconv.Atoi(f[5])
	if err != nil || tpgid <= 0 {
		return 0, nil, nil
	}
	argv, err = cmdline(tpgid)
	if err != nil {
		return 0, nil, err
	}
	return tpgid, argv, nil
}

// cmdline is a process's arguments, exactly as it was started.
func cmdline(pid int) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil, nil
	}
	return strings.Split(string(b), "\x00"), nil
}

// commandName is the program name in argv[0]: no folder, and no "-" that
// marks a login shell.
func commandName(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return strings.TrimPrefix(filepath.Base(argv[0]), "-")
}

// program returns the command line of the program in the foreground of
// the pane whose process is pid, when it is one restore starts again.
func program(pid int, allowed map[string]bool) []string {
	_, argv, err := foreground(pid)
	if err != nil || !allowed[commandName(argv)] {
		return nil
	}
	return argv
}

// shells are the shells restore waits for before it types a command.
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true,
	"ksh": true, "mksh": true, "yash": true, "tcsh": true, "csh": true,
}

// shellUp reports whether an interactive shell is in the foreground of the
// pane whose process is pid. shell is the default-shell, which counts
// whatever its name. A shell running a -c command does not count: that is
// tmux starting the pane's command, not the shell a user types into.
func shellUp(pid int, shell string) bool {
	leader, argv, err := foreground(pid)
	if err != nil || leader == 0 || len(argv) == 0 || slices.Contains(argv[1:], "-c") {
		return false
	}
	name := commandName(argv)
	return shells[name] || name == filepath.Base(shell)
}
