package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// `tmux-agent start` opens a window whose program runs the agent through
// the launcher, and waits until the agent is ready: its session started.
// It waits on a tmux wait-for channel, which it names in the window's
// environment (StartEnv). The launcher keeps the channel in its pane's
// options before the agent starts, so the agent's first hook finds it;
// the hook of the session's start signals it, once. An agent with no hooks
// is ready when the launcher has started it. A run that ends first, or a
// launcher that cannot start the agent, signals it too: start then finds
// no session and reports the end.

// StartEnv is the environment variable that names the channel.
const StartEnv = "TMUX_AGENT_START"

// OptStarting holds the channel on the launcher's pane until it is
// signalled.
const OptStarting = "@agent_starting"

// NewStartChannel names a channel for one start.
func NewStartChannel() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails on Linux
	return "tmux-agent-start-" + hex.EncodeToString(b)
}

// validChannel reports whether s is a channel NewStartChannel named: the
// launcher passes it to tmux.
func validChannel(s string) bool {
	rest, ok := strings.CutPrefix(s, "tmux-agent-start-")
	if !ok || rest == "" {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}

// startChannel is the channel a start waits on for this launcher, or "".
func (e *Env) startChannel() string {
	if c := e.getenv(StartEnv); validChannel(c) {
		return c
	}
	return ""
}

// signalStart tells the start waiting on channel to look at the pane, and
// drops the channel from the pane, so it is signalled once.
func (e *Env) signalStart(ctx context.Context, channel string) {
	if channel == "" {
		return
	}
	if _, err := e.Tmux.Run(ctx, "set-option", "-pu", "-t", e.Pane, OptStarting, ";", "wait-for", "-S", channel); err != nil {
		e.logf("start %s: %v", channel, err)
	}
}

// signalStartNow signals the start of this launcher when it ends before
// its pane holds the channel, such as when the agent's program is missing.
func (e *Env) signalStartNow() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e.signalStart(ctx, e.startChannel())
}

// Started is what a start found once the launcher signalled it.
type Started struct {
	Session string // the agent's session; "" when it has none yet
	Running bool   // the launcher still runs the agent
	Reason  string // why the run ended, when it did and said so
}

// StartedIn reads what the launcher in pane recorded.
func (e *Env) StartedIn(ctx context.Context, pane string) (Started, error) {
	out, err := e.Tmux.Output(ctx, "display-message", "-p", "-t", pane,
		"#{"+optSession+"}\x1f#{"+optRun+"}\x1f#{@attention_reason}")
	if err != nil {
		return Started{}, err
	}
	f := strings.SplitN(out, "\x1f", 3)
	for len(f) < 3 {
		f = append(f, "")
	}
	return Started{Session: f[0], Running: f[1] != "", Reason: f[2]}, nil
}
