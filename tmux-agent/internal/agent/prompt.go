package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
}

var agentPaneFormat = strings.Join([]string{
	"#{pane_id}", "#{window_id}", "#{window_name}", "#{session_name}",
	"#{" + optAgent + "}", "#{" + optSession + "}", "#{" + optTranscript + "}",
	"#{@attention}", "#{@attention_reason}", "#{" + attention.OptWait + "}",
}, "\x1f")

func parseAgentPane(line string) AgentPane {
	f := strings.SplitN(line, "\x1f", 10)
	for len(f) < 10 {
		f = append(f, "")
	}
	return AgentPane{
		Pane: f[0], Window: f[1], WindowName: f[2], TmuxSession: f[3],
		Agent: f[4], Session: f[5], Transcript: f[6],
		Flag: attention.State(f[7]), Reason: f[8], Wait: f[9],
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
