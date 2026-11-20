package agent

import (
	"context"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// Natives gives each agent pane whose agent has its own channel (Peer)
// its address there, by pane id. The address is the agent process's: the
// first one at or below the pane's first process.
func (e *Env) Natives(ctx context.Context) (map[string]attention.Native, error) {
	ps, err := e.AgentPanes(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]attention.Native{}
	var tree map[int][]proc
	for _, p := range ps {
		a := e.Registry.Get(p.Agent)
		peer, isPeer := a.(Peer)
		names, hasNames := a.(ProcessNamer)
		if !isPeer || !hasNames || p.Pid == 0 {
			continue
		}
		if tree == nil {
			if tree = processTree(); tree == nil {
				return out, nil
			}
		}
		pid := firstAgentBelow(p.Pid, names.ProcessNames(), tree)
		if pid == 0 {
			continue
		}
		if via, to, ok := peer.PeerAddress(pid, p.Session, e.getenv); ok {
			out[p.Pane] = attention.Native{Via: via, To: to}
		}
	}
	return out, nil
}
