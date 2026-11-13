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

// agentArgs returns the arguments the agent process p was started with:
// what follows the agent's program in its command line.
func agentArgs(p proc, names []string) []string {
	if len(p.argv) > 1 && !slices.Contains(names, filepath.Base(p.argv[0])) && slices.Contains(names, filepath.Base(p.argv[1])) {
		return p.argv[2:] // a script and its interpreter
	}
	if len(p.argv) > 0 {
		return p.argv[1:]
	}
	return nil
}

// nearestAgentArgs returns the arguments of the nearest agent process from
// pid up to, not including, stop (0 for none). ok is false when there is
// none.
func nearestAgentArgs(pid int, names []string, stop int) (args []string, ok bool) {
	for pid > 1 && pid != stop {
		p, err := readProc(pid)
		if err != nil {
			return nil, false
		}
		if isAgentProc(p, names) {
			return agentArgs(p, names), true
		}
		pid = p.ppid
	}
	return nil, false
}

// hasAgentBelow reports whether pid, or a process below it, is an agent
// process. A stopped one counts: it can go on. tree maps a pid to its
// children.
func hasAgentBelow(pid int, names []string, tree map[int][]proc) bool {
	if p, err := readProc(pid); err == nil && isAgentProc(p, names) {
		return true
	}
	queue := []int{pid}
	for len(queue) > 0 {
		for _, c := range tree[queue[0]] {
			if isAgentProc(c, names) {
				return true
			}
			queue = append(queue, c.pid)
		}
		queue = queue[1:]
	}
	return false
}

// agentName is the name of the agent process p: its process name, or for
// a script run by an interpreter, its program's.
func agentName(p proc, names []string) string {
	if slices.Contains(names, p.comm) || len(p.argv) < 2 {
		return p.comm
	}
	return filepath.Base(p.argv[1])
}

// nestedAgents returns, sorted by name, the agent processes below the
// pane's agent: the first agent process at or below pid. Only those with
// no other agent process between them and it count; what runs inside one
// of them is its own.
func nestedAgents(pid int, names []string, tree map[int][]proc) []string {
	top := 0
	if p, err := readProc(pid); err == nil && isAgentProc(p, names) {
		top = pid
	}
	for queue := []int{pid}; top == 0 && len(queue) > 0; queue = queue[1:] {
		for _, c := range tree[queue[0]] {
			if isAgentProc(c, names) {
				top = c.pid
				break
			}
			queue = append(queue, c.pid)
		}
	}
	if top == 0 {
		return nil
	}
	var runs []string
	for queue := []int{top}; len(queue) > 0; queue = queue[1:] {
		for _, c := range tree[queue[0]] {
			if isAgentProc(c, names) {
				runs = append(runs, agentName(c, names))
				continue
			}
			queue = append(queue, c.pid)
		}
	}
	slices.Sort(runs)
	return runs
}

// processTree reads every process and maps each pid to its children; nil
// when the process table cannot be read.
func processTree() map[int][]proc {
	des, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	tree := map[int][]proc{}
	for _, de := range des {
		pid, err := strconv.Atoi(de.Name())
		if err != nil {
			continue
		}
		if p, err := readProc(pid); err == nil {
			tree[p.ppid] = append(tree[p.ppid], p)
		}
	}
	return tree
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

// OneShotHook reports whether the hook process runs under a one-turn run
// of agent a, such as claude -p started without the launcher.
func OneShotHook(a Agent, names []string, run int) bool {
	o, ok := a.(OneShotter)
	if !ok || len(names) == 0 {
		return false
	}
	args, ok := nearestAgentArgs(os.Getppid(), names, run)
	return ok && o.OneShot(args)
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
