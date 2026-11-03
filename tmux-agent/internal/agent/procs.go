package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// proc is one process as /proc shows it.
type proc struct {
	pid, ppid int
	state     byte // R, S, T (stopped) and so on
	comm      string
	argv      []string
}

// procRoot is where process information lives; tests point it elsewhere.
var procRoot = "/proc"

func readProc(pid int) (proc, error) {
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return proc{}, err
	}
	// The name sits in parentheses and may hold spaces and parentheses
	// itself; the fields after the last ")" are plain.
	open, end := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return proc{}, fmt.Errorf("process %d: malformed stat", pid)
	}
	rest := strings.Fields(string(stat[end+1:]))
	if len(rest) < 2 {
		return proc{}, fmt.Errorf("process %d: malformed stat", pid)
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return proc{}, fmt.Errorf("process %d: parent: %w", pid, err)
	}
	p := proc{pid: pid, ppid: ppid, state: rest[0][0], comm: string(stat[open+1 : end])}
	if cmdline, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline")); err == nil {
		p.argv = strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
	}
	return p, nil
}

// isStopped reports whether process pid is stopped by a signal now.
func isStopped(pid int) bool {
	p, err := readProc(pid)
	return err == nil && p.state == 'T'
}

// launcherName is tmux-agent's own process name. The launcher puts the
// agent's name in its argv[0], so it is told apart by this.
const launcherName = "tmux-agent"

// isAgentProc reports whether p is an agent's process: its name is one of
// names, or, for a script run by an interpreter (an npm-installed claude
// runs as node), the program in its first argument is.
func isAgentProc(p proc, names []string) bool {
	if p.comm == launcherName {
		return false
	}
	if slices.Contains(names, p.comm) {
		return true
	}
	return len(p.argv) > 1 && slices.Contains(names, filepath.Base(p.argv[1]))
}

// agentAncestors counts the agent processes from pid up to, not including,
// stop (the launcher; 0 for none), or to init. It stops counting at max.
func agentAncestors(pid int, names []string, stop, max int) int {
	n := 0
	for pid > 1 && pid != stop && n < max {
		p, err := readProc(pid)
		if err != nil {
			break
		}
		if isAgentProc(p, names) {
			n++
		}
		pid = p.ppid
	}
	return n
}

// NestedHook reports whether the hook process runs under an agent that was
// started inside another one, such as a `claude -p` run as a tool: it
// inherits the outer pane and run, and its hooks would take them over. A
// hook counts for a run only when its nearest agent process is the run's
// own, so two agent processes between the hook and the launcher (or init)
// mean nested.
func NestedHook(names []string, run int) bool {
	if len(names) == 0 {
		return false
	}
	return agentAncestors(os.Getppid(), names, run, 2) >= 2
}

// InsideAgent reports whether the calling process runs under an agent
// process, which a launcher started from an agent's tool call does: it
// must leave the pane to the outer run.
func InsideAgent(names []string) bool {
	if len(names) == 0 {
		return false
	}
	return agentAncestors(os.Getppid(), names, 0, 1) >= 1
}
