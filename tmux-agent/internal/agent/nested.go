package agent

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// An agent can start other agents outside its own subagents: a script of
// claude -p runs from a shell in the background, say. Their hooks are
// dropped (NestedHook), so the pane learns of them from the process tree.
// A pane lists them in attention.OptNested, and a turn that ends while
// they run leaves the pane working and held (optHeld): the agent waits on
// them, not on the user. When the last one ends, the turn is done.

// nestedRuns are the agents that run below the agent of the pane whose
// shell is pid; none when the process table cannot be read.
func (e *Env) nestedRuns(pid int) []string {
	if pid <= 0 {
		return nil
	}
	tree := processTree()
	if tree == nil {
		return nil
	}
	return nestedAgents(pid, e.Registry.ProcessNames(), tree)
}

// holdFor marks the pane's ended turn as held by runs, and lists them.
func (e *Env) holdFor(ctx context.Context, pane string, l live, runs []string) error {
	opts := map[string]string{optHeld: "1"}
	listed := slices.Equal(l.nested, runs)
	if !listed {
		opts[attention.OptNested] = strings.Join(runs, " ")
	}
	if err := e.optsOf(ctx, pane, opts); err != nil {
		return err
	}
	if !listed {
		e.changed(ctx)
	}
	return nil
}

func (e *Env) changed(ctx context.Context) {
	if e.Core.Changed != nil {
		e.Core.Changed(ctx)
	}
}

// SweepNested brings each agent pane's list of nested runs up to date,
// and ends the held turn of a pane whose runs all ended.
func (e *Env) SweepNested(ctx context.Context) error {
	format := strings.Join([]string{
		"#{pane_id}", "#{pane_pid}", "#{" + optAgent + "}", "#{" + attention.OptNested + "}", "#{" + optHeld + "}",
	}, "\x1f")
	out, err := e.Tmux.Output(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return err
	}
	type pane struct {
		id, nested string
		pid        int
		held       bool
	}
	var panes []pane
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\x1f", 5)
		if len(f) < 5 || f[2] == "" {
			continue
		}
		panes = append(panes, pane{id: f[0], pid: atoi(f[1]), nested: f[3], held: f[4] == "1"})
	}
	if len(panes) == 0 {
		return nil
	}
	tree := processTree()
	if tree == nil {
		return nil
	}
	names := e.Registry.ProcessNames()
	listed := false
	for _, p := range panes {
		runs := strings.Join(nestedAgents(p.pid, names, tree), " ")
		if runs != p.nested {
			err := e.optsOf(ctx, p.id, map[string]string{attention.OptNested: runs})
			if err != nil && !isGone(err) {
				return err
			}
			listed = true
		}
		if p.held && runs == "" {
			if err := e.endHeld(ctx, p.id); err != nil && !isGone(err) {
				return err
			}
		}
	}
	if listed {
		e.changed(ctx)
	}
	return nil
}

// endHeld ends the turn of a pane held by runs that are over, as its turn
// end would have, unless the agent took a turn since.
func (e *Env) endHeld(ctx context.Context, pane string) error {
	defer e.lock(ctx)()
	pe := *e
	pe.Pane = pane
	l, err := pe.readLive(ctx)
	if err != nil || !l.held || l.agent == "" {
		return err
	}
	return pe.markTurnEnd(ctx, e.Registry.Get(l.agent), Event{Kind: TurnEnd}, l, l.session)
}

// atoi is n, or 0 for no number.
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
