package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// AgentPane is a pane whose agent has a session, and its flag.
type AgentPane struct {
	Pane, Window, WindowName, TmuxSession string
	Agent, Session, Transcript            string
	Flag                                  attention.State
	Reason                                string
	// Wait is the channel of a prompt that waits on the pane, or "".
	Wait string
	Pid  int // the pane's first process
}

var agentPaneFormat = strings.Join([]string{
	"#{pane_id}", "#{window_id}", "#{window_name}", "#{session_name}",
	"#{" + optAgent + "}", "#{" + optSession + "}", "#{" + optTranscript + "}",
	"#{@attention}", "#{@attention_reason}", "#{" + attention.OptWait + "}", "#{pane_pid}",
}, "\x1f")

func parseAgentPane(line string) AgentPane {
	f := strings.SplitN(line, "\x1f", 11)
	for len(f) < 11 {
		f = append(f, "")
	}
	pid, _ := strconv.Atoi(f[10]) // 0 for a dead pane: no process
	return AgentPane{
		Pane: f[0], Window: f[1], WindowName: f[2], TmuxSession: f[3],
		Agent: f[4], Session: f[5], Transcript: f[6],
		Flag: attention.State(f[7]), Reason: f[8], Wait: f[9], Pid: pid,
	}
}

// AgentPanes lists every pane of the server whose agent has a session.
func (e *Env) AgentPanes(ctx context.Context) ([]AgentPane, error) {
	out, err := e.Tmux.Output(ctx, "list-panes", "-a", "-F", agentPaneFormat)
	if err != nil {
		return nil, err
	}
	var ps []AgentPane
	for l := range strings.SplitSeq(out, "\n") {
		if p := parseAgentPane(l); p.Session != "" {
			ps = append(ps, p)
		}
	}
	return ps, nil
}

// PaneNow reads one pane again; ok is false when the pane is gone.
func (e *Env) PaneNow(ctx context.Context, pane string) (p AgentPane, ok bool, err error) {
	out, err := e.Tmux.Output(ctx, "display-message", "-p", "-t", pane, agentPaneFormat)
	if err != nil {
		if isGone(err) {
			return AgentPane{}, false, nil
		}
		return AgentPane{}, false, err
	}
	return parseAgentPane(out), true, nil
}

// NewPromptChannel names a channel for one prompt that waits.
func NewPromptChannel() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails on Linux
	return "tmux-agent-prompt-" + hex.EncodeToString(b)
}

// ErrNoAgentRuns is a prompt's pane whose agent process is gone or
// stopped: the pane's shell would read the prompt as a command.
var ErrNoAgentRuns = errors.New("no agent runs in the pane")

// ClaimForPrompt takes the pane for one prompt, under the lock, so that
// two prompts never both find it free. It reads the pane again; a pane
// whose agent works or waits on the user it returns unclaimed. Else it
// answers a done flag (seen) and flags the pane working, as the agent's
// prompt hook does, before the text is typed.
func (e *Env) ClaimForPrompt(ctx context.Context, pane string) (p AgentPane, claimed bool, err error) {
	defer e.lock(ctx)()
	p, ok, err := e.PaneNow(ctx, pane)
	if err != nil {
		return p, false, err
	}
	tree := processTree()
	if !ok || p.Session == "" || p.Pid == 0 || tree == nil || !runsAgentBelow(p.Pid, e.Registry.ProcessNames(), tree) {
		return p, false, ErrNoAgentRuns
	}
	if p.Flag == attention.Working || p.Flag == attention.NeedsYou {
		return p, false, nil
	}
	if err := e.Core.Seen(ctx, pane); err != nil {
		return p, false, err
	}
	pe := *e
	pe.Pane = pane
	return p, true, pe.markWorking(ctx, e.Registry.Get(p.Agent), p.Session)
}
