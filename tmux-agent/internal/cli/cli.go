// Package cli is tmux-agent's command line: it wires the tmux client, the
// core, the agent layer and the adapters together, and runs a subcommand.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/influento/tmux-plugins/internal/shell"
	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
	"github.com/influento/tmux-plugins/tmux-agent/internal/agent/claude"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
	"github.com/influento/tmux-plugins/tmux-agent/internal/ui"
)

const usage = `usage:
  tmux-agent run AGENT [ARGS...]       start an agent through the launcher
  tmux-agent hook AGENT                handle one hook event (JSON on stdin)
  tmux-agent flag STATE [--reason TEXT] [--pane ID] [--owner CMD] [--quiet] [--failed]
                                       flag a pane: needs-you, done, working or clear;
                                       --failed marks a done flag as a failure;
                                       prints the previous state when it changed
  tmux-agent seen [PANE]               done → idle; run when a pane gets focus
  tmux-agent list [--json]             every pane, flagged first
  tmux-agent jump [--pane ID]          go to the next pane that needs you, else the next done
  tmux-agent pick                      pick a session with fzf
  tmux-agent popup [CLIENT]            pick in a popup sized to its rows
  tmux-agent rows                      the rows pick lists
  tmux-agent kill-session NAME         kill a session, unless it is the last one
  tmux-agent status                    render the counts into @attention_status
  tmux-agent sweep [--window ID]       drop dead flags and titles; set the window's borders
  tmux-agent borders WINDOW            borders on for a split window, off otherwise
  tmux-agent shell-init SHELL [NAME...]  shell functions that start agents through run
  tmux-agent tmux-init                 set tmux-agent's hooks, at index [10]
  tmux-agent --version
`

// hookIndex is tmux-agent's slot in each tmux hook array; tmux-revive has
// [20]. Setting an index again replaces it.
const hookIndex = 10

// app is one run of the command.
type app struct {
	version string
	stdout  io.Writer
	stderr  io.Writer
	getenv  func(string) string
	reg     *agent.Registry
	tmux    *tmux.Client
	core    *attention.Core
	env     *agent.Env
	self    string // the tmux-agent executable
	debug   io.Writer
}

// Registry is every agent tmux-agent knows: one line per adapter.
func Registry() *agent.Registry {
	return agent.NewRegistry(claude.Claude{})
}

// Main runs tmux-agent with args (without the program name) and returns
// the exit code. self is the whole command line, for the launcher.
func Main(version string, self []string, stdin io.Reader, stdout, stderr io.Writer) int {
	args := self[1:]
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	a := newApp(version, stdout, stderr, os.Getenv)
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "hook":
		return a.hook(rest, stdin)
	case "run":
		if len(rest) == 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		a.env.Log = a.logf // the terminal belongs to the agent
		return a.env.Launch(a.reg.Get(rest[0]), rest[1:], self, stderr)
	case "--version", "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := a.command(ctx, cmd, rest)
	if errors.Is(err, errUsage) {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "tmux-agent %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage")

func newApp(version string, stdout, stderr io.Writer, getenv func(string) string) *app {
	a := &app{version: version, stdout: stdout, stderr: stderr, getenv: getenv, reg: Registry()}
	a.self, _ = os.Executable() // unknown: the name on PATH
	if a.self == "" {
		a.self = "tmux-agent"
	}
	if getenv("TMUX_AGENT_DEBUG") != "" {
		dir := store.RuntimeDir(getenv)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			f, err := os.OpenFile(filepath.Join(dir, "debug.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
			if err == nil {
				a.debug = f
			}
		}
	}
	a.tmux = &tmux.Client{Timeout: 2 * time.Second}
	a.core = &attention.Core{Tmux: a.tmux, Agents: a.reg.CommandNames(), Notify: a.spawnNotify}
	a.env = &agent.Env{
		Tmux: a.tmux, Core: a.core, Registry: a.reg,
		Store:    &store.Store{Dir: store.StateDir(getenv)},
		Pane:     getenv("TMUX_PANE"),
		Run:      agent.RunFromEnv(getenv),
		LockPath: filepath.Join(store.RuntimeDir(getenv), "lock"),
	}
	return a
}

// logf writes to the debug log, when TMUX_AGENT_DEBUG is set.
func (a *app) logf(format string, args ...any) {
	if a.debug != nil {
		fmt.Fprintf(a.debug, "%s %d %s\n", time.Now().Format(time.RFC3339), os.Getpid(), fmt.Sprintf(format, args...))
	}
}

// spawnNotify starts the notifier for a flag and returns: it waits a moment
// and re-reads the flag, so a prompt that resolves at once stays quiet. It
// runs in its own session with no terminal or pipes, so it outlives a hook
// and holds nothing open that the agent waits on.
func (a *app) spawnNotify(pane string, s attention.State) {
	cmd := exec.Command(a.self, "notify", pane, string(s))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		a.logf("notify: %v", err)
		return
	}
	_ = cmd.Process.Release() // never waited for
}

// hook handles one hook event. It never writes stdout, which Claude Code
// adds to the model's context; never fails, since exit 2 from PreToolUse
// blocks the tool call; and gives up in time, since the agent waits.
func (a *app) hook(args []string, stdin io.Reader) (code int) {
	if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		_ = syscall.Dup3(int(devnull.Fd()), 1, 0) // nothing can reach the agent through fd 1
		devnull.Close()
	}
	a.stdout = io.Discard
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(a.stderr, "tmux-agent hook: panic: %v\n", r)
			a.logf("hook panic: %v", r)
			code = 0
		}
	}()
	if len(args) != 1 {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	payload, err := io.ReadAll(io.LimitReader(stdin, 64<<20))
	if err != nil {
		return 0
	}
	ag, ok := a.reg.Lookup(args[0])
	if !ok {
		return 0
	}
	a.env.Log = a.logf
	if err := a.env.Hook(ctx, ag, payload); err != nil {
		fmt.Fprintf(a.stderr, "tmux-agent hook %s: %v\n", args[0], err)
		a.logf("hook %s: %v", args[0], err)
	}
	return 0
}

// pane is the pane a command is about: the one given, else the caller's.
func (a *app) pane(given string) (string, error) {
	if given != "" {
		return given, nil
	}
	if p := a.getenv("TMUX_PANE"); p != "" {
		return p, nil
	}
	return "", errors.New("no pane: pass one, or run inside tmux")
}

func (a *app) command(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "flag":
		return a.flag(ctx, args)
	case "seen":
		if len(args) > 1 {
			return errUsage
		}
		p, err := a.pane(first(args))
		if err != nil {
			return err
		}
		return a.core.Seen(ctx, p)
	case "status":
		return a.core.Status(ctx)
	case "sweep":
		return a.sweep(ctx, args)
	case "borders":
		if len(args) != 1 {
			return errUsage
		}
		return a.core.Borders(ctx, args[0])
	case "list":
		return a.list(ctx, args)
	case "rows":
		return a.rows(ctx)
	case "pick":
		return a.pick(ctx)
	case "popup":
		return a.popup(ctx, first(args))
	case "kill-session":
		if len(args) != 1 {
			return errUsage
		}
		return a.core.KillSession(ctx, args[0])
	case "jump":
		return a.jump(ctx, args)
	case "notify":
		if len(args) != 2 {
			return errUsage
		}
		return a.notify(args[0], attention.State(args[1]))
	case "shell-init":
		return a.shellInit(args)
	case "tmux-init":
		return a.tmuxInit(ctx)
	}
	return errUsage
}

func first(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func (a *app) flag(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	state := args[0]
	fs := flag.NewFlagSet("flag", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	reason := fs.String("reason", "", "")
	paneArg := fs.String("pane", "", "")
	owner := fs.String("owner", "", "")
	quiet := fs.Bool("quiet", false, "")
	failed := fs.Bool("failed", false, "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return errUsage
	}
	p, err := a.pane(*paneArg)
	if err != nil {
		return err
	}
	if state == "clear" {
		return a.core.Clear(ctx, p)
	}
	st, ok := attention.ParseState(state)
	if !ok {
		return errUsage
	}
	prev, changed, err := a.core.Set(ctx, p, attention.Flag{State: st, Reason: *reason, Owner: *owner, Quiet: *quiet, Failed: *failed})
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintln(a.stdout, prev.String())
	}
	return nil
}

// sweep is what tmux runs when a pane closes: the window's borders, the
// flags of commands that are gone, and the titles of agents that are gone,
// in one call.
func (a *app) sweep(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	window := fs.String("window", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errUsage
	}
	if *window != "" {
		if err := a.core.Borders(ctx, *window); err != nil {
			return err
		}
	} else {
		dropped, err := a.core.Sweep(ctx)
		if err != nil {
			return err
		}
		if dropped {
			if err := a.core.Status(ctx); err != nil {
				return err
			}
		}
	}
	return a.env.SweepTitles(ctx)
}

func (a *app) list(ctx context.Context, args []string) error {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !asJSON {
		return errUsage
	}
	rows, err := a.core.List(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		if rows == nil {
			rows = []attention.Row{}
		}
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	for _, r := range rows {
		fmt.Fprintf(a.stdout, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Rank, r.Pane, r.Glyph, r.Window, r.Target, r.Detail, r.Age)
	}
	return nil
}

func (a *app) rows(ctx context.Context) error {
	rows, err := a.core.SessionRows(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Fprintf(a.stdout, "%s\t%s\n", r.Session, r.Display)
	}
	return nil
}

func (a *app) pick(ctx context.Context) error {
	rows, err := a.core.SessionRows(ctx)
	if err != nil {
		return err
	}
	// The picker waits on the user; only the tmux calls are bounded.
	choice, err := ui.Pick(context.WithoutCancel(ctx), rows, shell.Quote(a.self))
	if err != nil || choice == "" {
		return err
	}
	if _, err := a.tmux.Run(ctx, "switch-client", "-t", "="+choice); err != nil {
		return err
	}
	// switch-client fires no select hook, so mark the pane it lands on.
	pane, err := a.tmux.Display(ctx, "="+choice+":", "#{pane_id}")
	if err != nil || pane == "" {
		return err
	}
	return a.core.Seen(ctx, pane)
}

func (a *app) popup(ctx context.Context, client string) error {
	rows, err := a.core.SessionRows(ctx)
	if err != nil {
		return err
	}
	clientArgs := []string{}
	if client != "" {
		clientArgs = []string{"-c", client}
	}
	size, err := a.tmux.Output(ctx, append(append([]string{"display-message", "-p"}, clientArgs...), "#{client_width} #{client_height}")...)
	if err != nil {
		return err
	}
	var cw, ch int
	if _, err := fmt.Sscan(size, &cw, &ch); err != nil {
		return fmt.Errorf("client size %q: %w", size, err)
	}
	w, h := ui.PopupSize(rows, cw, ch)
	args := append(append([]string{"display-popup"}, clientArgs...),
		"-E", "-w", strconv.Itoa(w), "-h", strconv.Itoa(h), shell.Quote(a.self)+" pick")
	_, err = a.tmux.Run(ctx, args...)
	return err
}

func (a *app) jump(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("jump", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	paneArg := fs.String("pane", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errUsage
	}
	if *paneArg == "" {
		return a.core.Jump(ctx)
	}
	if err := a.core.GoTo(ctx, *paneArg); err != nil {
		return err
	}
	// The terminal itself, when a notification click brought us here.
	return ui.RaiseTerminal(ctx, a.getenv)
}

// notify is the notifier spawnNotify starts.
func (a *app) notify(pane string, s attention.State) error {
	time.Sleep(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	title, body, ok, err := a.core.Notification(ctx, pane, s)
	if err != nil || !ok {
		return err
	}
	// A notification with an action waits until it closes.
	wait, cancelWait := context.WithTimeout(context.Background(), time.Hour)
	defer cancelWait()
	clicked, err := ui.Notify(wait, title, body)
	if err != nil || !clicked {
		return err
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.jump(ctx, []string{"--pane", pane})
}

var shellName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// shellInit prints a shell function per registered agent, and per extra
// name, that starts it through the launcher inside tmux and plainly
// outside, or when tmux-agent is missing.
func (a *app) shellInit(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	var names []string
	for _, ag := range a.reg.Agents() {
		names = append(names, ag.Name())
	}
	for _, n := range args[1:] {
		if !shellName.MatchString(n) {
			return fmt.Errorf("not a function name: %q", n)
		}
		names = append(names, n)
	}
	var tmpl string
	switch args[0] {
	case "zsh":
		tmpl = "%[1]s() { if [[ -n $TMUX ]] && (( $+commands[tmux-agent] )); then tmux-agent run %[1]s \"$@\"; else command %[1]s \"$@\"; fi }\n"
	case "bash", "sh":
		tmpl = "%[1]s() { if [ -n \"${TMUX:-}\" ] && command -v tmux-agent >/dev/null 2>&1; then tmux-agent run %[1]s \"$@\"; else command %[1]s \"$@\"; fi; }\n"
	default:
		return fmt.Errorf("unsupported shell %q: zsh, bash or sh", args[0])
	}
	for _, n := range names {
		fmt.Fprintf(a.stdout, tmpl, n)
	}
	return nil
}

// tmuxHooks are the tmux events tmux-agent follows, and what it runs.
var tmuxHooks = []struct{ event, command string }{
	{"pane-focus-in", "seen #{pane_id}"},
	{"after-select-window", "seen #{pane_id}"},
	{"after-select-pane", "seen #{pane_id}"},
	{"after-split-window", "borders #{window_id}"},
	{"after-kill-pane", "sweep --window #{window_id}"},
	{"pane-exited", "sweep --window #{window_id}"},
	{"session-closed", "status"},
	{"client-focus-in", "status"},
}

// tmuxInit sets tmux-agent's hooks at its own index, so prefix r leaves no
// duplicates and tmux-revive's index stays untouched, then draws every
// mark and the counts in the theme's colours now.
func (a *app) tmuxInit(ctx context.Context) error {
	var args []string
	for _, h := range tmuxHooks {
		if len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-hook", "-g", fmt.Sprintf("%s[%d]", h.event, hookIndex),
			fmt.Sprintf(`run-shell -b "tmux-agent %s"`, h.command))
	}
	if _, err := a.tmux.Run(ctx, args...); err != nil {
		return err
	}
	return a.core.Redraw(ctx)
}

// TmuxHookEvents lists the events tmux-init sets, for tests.
func TmuxHookEvents() []string {
	var ev []string
	for _, h := range tmuxHooks {
		ev = append(ev, h.event)
	}
	return ev
}
