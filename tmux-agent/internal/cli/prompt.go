package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// The exit codes of prompt and read, besides 1 (an error) and 2 (usage).
const (
	exitBusy     = 3 // the agent works, or waits on the user
	exitNoAgent  = 4 // no agent in the target
	exitNoAnswer = 5 // the turn ended without an answer: cut short, or the agent ended
	exitNoTurn   = 6 // no receipt: the agent took no prompt, or other text than was sent
	exitSignal   = 130
)

// receiptWait is how long prompt waits for the receipt, the agent's submit
// hook noting the prompt it took. Claude Code runs the hook as the turn
// starts.
const receiptWait = 10 * time.Second

// promptSource is the label source prompt reports a failed delivery on,
// on the agent's pane; promptLabelTTL is how long such a label shows.
const (
	promptSource   = "prompt"
	promptLabelTTL = time.Hour
)

// screenTail is how many of a pane's last lines a failed delivery shows.
const screenTail = 6

// waitSlice is how long a wait waits on its channel before it checks
// that the pane and its agent are still there: a closed pane signals
// nothing.
const waitSlice = 2 * time.Second

// readTimeout bounds a read of the pane during and after a wait.
const readTimeout = 5 * time.Second

// answerWait is how long a prompt looks for the turn's answer in the
// transcript after the turn ended.
const answerWait = 5 * time.Second

// answerPoll is how often it looks.
const answerPoll = 100 * time.Millisecond

// codeError is an error with the exit code it ends the command with.
type codeError struct {
	code int
	msg  string
}

func (e *codeError) Error() string { return e.msg }

func exitWith(code int, format string, args ...any) error {
	return &codeError{code, fmt.Sprintf(format, args...)}
}

// promptedJSON is what `prompt --json` prints.
type promptedJSON struct {
	Version      int    `json:"version"`
	Pane         string `json:"pane"`
	Window       string `json:"window"`
	Session      string `json:"session"`
	Name         string `json:"name"`
	AgentSession string `json:"agent_session"`
	// State is the pane's flag when the wait ended: done or idle with an
	// answer; needs-you when the agent asks the user. Empty without --wait.
	State  string      `json:"state,omitempty"`
	Reason string      `json:"reason,omitempty"`
	Answer *answerJSON `json:"answer,omitempty"`
}

type answerJSON struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// readJSON is what `read --json` prints.
type readJSON struct {
	Version      int          `json:"version"`
	Pane         string       `json:"pane"`
	AgentSession string       `json:"agent_session"`
	Answers      []answerJSON `json:"answers"`
}

// runCode runs a command that ends with a codeError's code.
func (a *app) runCode(cmd string, fn func() error) int {
	err := fn()
	if err == nil {
		return 0
	}
	if errors.Is(err, errUsage) {
		fmt.Fprint(a.stderr, usage)
		return 2
	}
	fmt.Fprintf(a.stderr, "tmux-agent %s: %v\n", cmd, err)
	if ce, ok := errors.AsType[*codeError](err); ok {
		return ce.code
	}
	return 1
}

// promptCommand types a prompt into an agent's pane; with --wait it
// prints the answer once the turn ends.
func (a *app) promptCommand(args []string, stdin io.Reader) (err error) {
	fs := flag.NewFlagSet("prompt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wait := fs.Bool("wait", false, "")
	raw := fs.Bool("raw", false, "")
	timeout := fs.Duration("timeout", 0, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() < 1 || fs.NArg() > 2 || *timeout < 0 {
		return errUsage
	}
	var text string
	if fs.NArg() == 2 {
		text = fs.Arg(1)
	} else {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		text = string(b)
	}
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return exitWith(2, "no text to send")
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	p, err := a.agentTarget(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if p.Pane == a.getenv("TMUX_PANE") {
		return exitWith(2, "%s is the caller's own pane", p.Pane)
	}
	defer func() {
		end := "ok"
		if err != nil {
			end = "failed: " + strings.ReplaceAll(err.Error(), "\n", " | ")
		}
		a.logf("prompt %s (%s): wait %v, %s", p.Pane, p.WindowName, *wait, end)
	}()
	if ic, ok := a.reg.Get(p.Agent).(agent.InputCommander); ok && !*raw && ic.InputCommand(text) {
		return exitWith(2, "%s's input box takes this text as a command, not a prompt: --raw sends it anyway", fs.Arg(0))
	}
	claimedAt := time.Now()
	now, claimed, err := a.env.ClaimForPrompt(ctx, p.Pane)
	switch {
	case errors.Is(err, agent.ErrNoAgentRuns):
		return exitWith(exitNoAgent, "no agent runs in %s", fs.Arg(0))
	case err != nil:
		return err
	case !claimed && now.Flag == attention.NeedsYou:
		return exitWith(exitBusy, "%s waits on the user: %s", fs.Arg(0), now.Reason)
	case !claimed:
		return exitWith(exitBusy, "%s is working", fs.Arg(0))
	}
	channel := ""
	if *wait {
		channel = agent.NewPromptChannel()
	}
	if err := a.typePrompt(ctx, p.Pane, text, channel); err != nil {
		return err
	}
	// A signal ends the wait, and the pane's channel with it.
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	waitCtx := sigCtx
	if *timeout > 0 {
		var cancelWait context.CancelFunc
		waitCtx, cancelWait = context.WithTimeout(waitCtx, *timeout)
		defer cancelWait()
	}
	took, err := a.receipt(waitCtx, p, text, claimedAt, channel)
	if err != nil {
		if sigCtx.Err() != nil {
			return exitWith(exitSignal, "interrupted")
		}
		return err
	}
	out := promptedJSON{
		Version: contractVersion, Pane: p.Pane, Window: p.Window, Session: p.TmuxSession,
		Name: p.WindowName, AgentSession: p.Session,
	}
	if *wait {
		end, err := a.waitTurn(waitCtx, p, channel)
		if err != nil {
			if sigCtx.Err() != nil {
				return exitWith(exitSignal, "interrupted")
			}
			return err
		}
		out.State, out.Reason = end.Flag.String(), end.Reason
		if end.Flag == attention.NeedsYou {
			if *asJSON {
				if err := json.NewEncoder(a.stdout).Encode(out); err != nil {
					return err
				}
			}
			return exitWith(exitBusy, "%s waits on the user: %s", fs.Arg(0), end.Reason)
		}
		ans, ok := a.answerSince(p, took)
		if !ok {
			return exitWith(exitNoAnswer, "the turn in %s ended without an answer", p.Pane)
		}
		out.Answer = &answerJSON{ans.Text, ans.At}
		if !*asJSON {
			_, err := fmt.Fprintln(a.stdout, ans.Text)
			return err
		}
	}
	if *asJSON {
		return json.NewEncoder(a.stdout).Encode(out)
	}
	return nil
}

// typePrompt pastes text into the pane and presses Enter. The pane leaves
// copy mode, or any other mode, first: a mode takes the keys. With a
// channel, the pane keeps it for the turn's end to signal.
func (a *app) typePrompt(ctx context.Context, pane, text, channel string) error {
	buf := agent.NewPromptChannel() // a buffer name no other paste uses
	if err := a.tmux.LoadBuffer(ctx, buf, []byte(text)); err != nil {
		return err
	}
	var args []string
	if channel != "" {
		args = append(args, "set-option", "-p", "-t", pane, attention.OptWait, channel, ";")
	}
	args = append(args, "copy-mode", "-q", "-t", pane, ";",
		"paste-buffer", "-p", "-r", "-d", "-b", buf, "-t", pane, ";",
		"send-keys", "-t", pane, "Enter")
	_, err := a.tmux.Run(ctx, args...)
	return err
}

// receipt waits for the agent's receipt of the prompt claimed at claimed,
// and returns when the agent took it. With no receipt it gives the claim
// back; with none, or one of other text (a draft in the input box joined
// it), it labels the pane, logs, and ends with exitNoTurn. A wait that
// ends, by ctx or by an error, takes channel off the pane.
func (a *app) receipt(ctx context.Context, p agent.AgentPane, text string, claimed time.Time, channel string) (time.Time, error) {
	rctx, cancel := context.WithTimeout(ctx, receiptWait)
	defer cancel()
	for {
		readCtx, cancelRead := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
		now, ok, err := a.env.PaneNow(readCtx, p.Pane)
		cancelRead()
		switch {
		case err != nil:
			a.dropWait(ctx, p.Pane, channel)
			return time.Time{}, err
		case !ok:
			return time.Time{}, exitWith(exitNoAnswer, "%s closed", p.Pane)
		case !now.PromptGot.Before(claimed) && (now.PromptGotKey == "-" || now.PromptGotKey == agent.PromptKey(text)):
			a.promptLabel(ctx, p.Pane, "")
			a.logf("prompt %s (%s): taken", p.Pane, p.WindowName)
			return now.PromptGot, nil
		case !now.PromptGot.Before(claimed):
			a.dropWait(ctx, p.Pane, channel)
			a.promptLabel(ctx, p.Pane, "⚠ prompt: the agent got other text")
			a.logf("prompt %s (%s): the agent got other text than was sent", p.Pane, p.WindowName)
			return time.Time{}, exitWith(exitNoTurn, "%s got other text than was sent: text in its input box joined it?", p.WindowName)
		}
		select {
		case <-rctx.Done():
			a.dropWait(ctx, p.Pane, channel)
			if ctx.Err() != nil { // --timeout or a signal came first: no receipt all the same
				return time.Time{}, a.noReceipt(ctx, p, claimed, time.Since(claimed).Round(time.Second), 1)
			}
			return time.Time{}, a.noReceipt(ctx, p, claimed, receiptWait, exitNoTurn)
		case <-time.After(answerPoll):
		}
	}
}

// noReceipt ends a prompt that got no receipt in waited: the claim goes
// back, the pane gets a label, and the error, with code, shows the end of
// the pane's screen. Only a full receiptWait tells that no turn started.
func (a *app) noReceipt(ctx context.Context, p agent.AgentPane, claimed time.Time, waited time.Duration, code int) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	released, err := a.env.ReleasePrompt(ctx, p.Pane, claimed)
	if err != nil {
		return err
	}
	if !released { // the receipt came at the last moment
		return exitWith(code, "%s took the prompt only after %s", p.WindowName, waited)
	}
	a.promptLabel(ctx, p.Pane, "⚠ prompt not submitted")
	a.logf("prompt %s (%s): not submitted, no receipt in %s", p.Pane, p.WindowName, waited)
	screen, _ := a.tmux.Output(ctx, "capture-pane", "-p", "-t", p.Pane)
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	lines = lines[max(0, len(lines)-screenTail):]
	what := "started no turn"
	if code != exitNoTurn {
		what = "gave no receipt yet"
	}
	return exitWith(code, "%s %s (no receipt in %s); its screen ends:\n%s",
		p.WindowName, what, waited, strings.Join(lines, "\n"))
}

// promptLabel sets the pane's prompt label to text, or clears it for "".
func (a *app) promptLabel(ctx context.Context, pane, text string) {
	l := attention.Label{Source: promptSource, Text: text}
	if text != "" {
		l.Until = time.Now().Add(promptLabelTTL).Unix()
	}
	if err := a.env.Report(ctx, pane, l); err != nil {
		a.logf("prompt %s: label: %v", pane, err)
	}
}

// waitTurn waits until the core signals channel, and returns the pane as
// the signal left it. It ends with an error when ctx ends, or when the
// pane or its agent's session goes without a signal.
func (a *app) waitTurn(ctx context.Context, p agent.AgentPane, channel string) (agent.AgentPane, error) {
	for {
		sliceCtx, cancel := context.WithTimeout(ctx, waitSlice)
		err := a.tmux.WaitFor(sliceCtx, channel)
		cancel()
		if err != nil && ctx.Err() != nil {
			a.dropWait(ctx, p.Pane, channel)
			return agent.AgentPane{}, exitWith(1, "no answer from %s in time", p.Pane)
		}
		readCtx, cancelRead := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
		now, ok, err := a.env.PaneNow(readCtx, p.Pane)
		cancelRead()
		switch {
		case err != nil:
			return agent.AgentPane{}, err
		case !ok:
			return agent.AgentPane{}, exitWith(exitNoAnswer, "%s closed", p.Pane)
		case now.Wait != channel:
			return now, nil // signalled: the signal unsets it
		case now.Session != p.Session:
			a.dropWait(ctx, p.Pane, channel)
			return agent.AgentPane{}, exitWith(exitNoAnswer, "the agent's session in %s ended", p.Pane)
		}
	}
}

// dropWait takes a wait that ends unsignalled off the pane, also after
// ctx ended.
func (a *app) dropWait(ctx context.Context, pane, channel string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	now, ok, err := a.env.PaneNow(ctx, pane)
	if err != nil || !ok || now.Wait != channel {
		return
	}
	_, _ = a.tmux.Run(ctx, "set-option", "-pu", "-t", pane, attention.OptWait) // the pane may be gone
}

// answerSince is the last answer the pane's agent wrote at or after since,
// when the agent took the prompt.
// The turn's end can come before the transcript has its last message, so
// it looks for a while.
func (a *app) answerSince(p agent.AgentPane, since time.Time) (agent.Answer, bool) {
	ans, ok := a.reg.Get(p.Agent).(agent.Answerer)
	if !ok {
		return agent.Answer{}, false
	}
	for deadline := time.Now().Add(answerWait); ; {
		if as := ans.Answers(p.Transcript, 1); len(as) == 1 && !as[0].At.Before(since.Truncate(time.Millisecond)) {
			return as[0], true
		}
		if time.Now().After(deadline) {
			return agent.Answer{}, false
		}
		time.Sleep(answerPoll)
	}
}

// readCommand prints the last answers of an agent.
func (a *app) readCommand(args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	last := fs.Int("last", 1, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 || *last < 1 {
		return errUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	p, err := a.agentTarget(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	ans, ok := a.reg.Get(p.Agent).(agent.Answerer)
	if !ok {
		return fmt.Errorf("tmux-agent reads no answers of %s", p.Agent)
	}
	as := ans.Answers(p.Transcript, *last)
	if *asJSON {
		out := readJSON{Version: contractVersion, Pane: p.Pane, AgentSession: p.Session, Answers: []answerJSON{}}
		for _, x := range as {
			out.Answers = append(out.Answers, answerJSON{x.Text, x.At})
		}
		return json.NewEncoder(a.stdout).Encode(out)
	}
	for i, x := range as {
		if i > 0 {
			fmt.Fprintln(a.stdout, "---")
		}
		if _, err := fmt.Fprintln(a.stdout, x.Text); err != nil {
			return err
		}
	}
	return nil
}

// agentTarget is the one agent pane target names: a pane id, a window name
// in any session, or SESSION:WINDOW. Panes whose agents are gone are swept
// first.
func (a *app) agentTarget(ctx context.Context, target string) (agent.AgentPane, error) {
	if err := a.sweepAgents(ctx); err != nil {
		return agent.AgentPane{}, err
	}
	ps, err := a.env.AgentPanes(ctx)
	if err != nil {
		return agent.AgentPane{}, err
	}
	// A window in two sessions, grouped or linked, is listed once in each.
	var hits []agent.AgentPane
	hit := map[string]bool{}
	for _, p := range ps {
		if !hit[p.Pane] && (p.Pane == target || p.WindowName == target || p.TmuxSession+":"+p.WindowName == target) {
			hits = append(hits, p)
			hit[p.Pane] = true
		}
	}
	switch len(hits) {
	case 0:
		return agent.AgentPane{}, exitWith(exitNoAgent, "no agent in %s", target)
	case 1:
		return hits[0], nil
	}
	var names []string
	for _, p := range hits {
		names = append(names, p.TmuxSession+":"+p.WindowName+" ("+p.Pane+")")
	}
	return agent.AgentPane{}, exitWith(2, "%s names %d agents: %s; name one as SESSION:WINDOW or by its pane",
		target, len(hits), strings.Join(names, ", "))
}
