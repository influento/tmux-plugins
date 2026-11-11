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
	"strings"
	"syscall"
	"time"

	"github.com/influento/tmux-plugins/internal/shell"
	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
	"github.com/influento/tmux-plugins/tmux-agent/internal/agent/claude"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/sidebar"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
	"github.com/influento/tmux-plugins/tmux-agent/internal/ui"
)

const usage = `usage:
  tmux-agent run AGENT [ARGS...]       start an agent through the launcher
  tmux-agent revive AGENT SESSION      resume a session through the launcher, as a
                                       restore types it; nothing when it is gone
  tmux-agent hook AGENT                handle one hook event (JSON on stdin)
  tmux-agent flag STATE [--reason TEXT] [--pane ID] [--owner CMD] [--quiet] [--failed]
                                       flag a pane: needs-you, done, working or clear;
                                       --failed marks a done flag as a failure;
                                       prints the previous state when it changed
  tmux-agent report --source NAME [--text TEXT] [--token KEY=VALUE]... [--ttl DURATION]
                    [--pane ID] [--clear]
                                       set what a source says about a pane: the panel
                                       shows TEXT (else the tokens' values) on the
                                       pane's second line; --ttl drops it after that
                                       long; --clear removes the source's label
  tmux-agent seen [PANE]               done → idle; run when a pane gets focus
  tmux-agent list [--json]             every pane, flagged first
  tmux-agent jump [--pane ID]          go to the next pane that needs you, else the next done
  tmux-agent pick [CLIENT [ORIGIN]]    pick a session, window or pane with fzf; the
                                       client shows each row as the cursor reaches it;
                                       the panel runs it, ORIGIN the pane it opened from
  tmux-agent popup [CLIENT]            open the panel, a pane at the left edge that
                                       follows the client, or close it
  tmux-agent sidebar ACTION [ARGS]     the panel's moves: go CLIENT TARGET, step CLIENT
                                       +|-|N (prefix n, p, 1-9), layout CLIENT NAME|next|
                                       previous (preset layouts), close CLIENT; for the
                                       hooks: follow, keep, reload
  tmux-agent pick-rows [--fzf] [QUERY] the rows pick lists in the tree: the windows whose
                                       session or window name holds every word of QUERY;
                                       --fzf takes the query, view and width from fzf
  tmux-agent pick-key KEY [ARG]        what tab, enter or a reload (place CLIENT) does
                                       in the panel, as fzf actions
  tmux-agent kill TARGET               kill a picker row's session, window or pane,
                                       unless it ends the last session
  tmux-agent rows                      one line per session, its windows' flags inline
  tmux-agent kill-session NAME         kill a session, unless it is the last one
  tmux-agent status                    render the counts into @attention_status
  tmux-agent sweep [--window ID]       drop dead flags, sessions and titles; set the
                                       window's borders
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
	side    *sidebar.Sidebar
	self    string // the tmux-agent executable
	debug   io.Writer
}

// Registry is every agent tmux-agent knows: one line per adapter.
func Registry() *agent.Registry {
	return agent.NewRegistry(claude.Claude{})
}

// marks are the panel's marks of the agents that have one, by name and by
// command.
func marks(reg *agent.Registry) map[string]attention.AgentMark {
	m := map[string]attention.AgentMark{}
	for _, ag := range reg.Agents() {
		mk, ok := ag.(agent.Marker)
		if !ok {
			continue
		}
		glyph, colour := mk.Mark()
		m[ag.Name()] = attention.AgentMark{Glyph: glyph, Colour: colour}
		m[agent.CommandName(ag)] = attention.AgentMark{Glyph: glyph, Colour: colour}
	}
	return m
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
	case "revive":
		if len(rest) != 2 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		a.env.Log = a.logf
		return a.env.Revive(a.reg.Get(rest[0]), rest[1], self, stderr)
	case "--version", "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
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

// commandTimeout bounds a command's work, the time it waits on the user
// left out.
const commandTimeout = 30 * time.Second

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
	a.core = &attention.Core{Tmux: a.tmux, Agents: a.reg.CommandNames(), Marks: marks(a.reg), Notify: a.spawnNotify}
	a.env = &agent.Env{
		Tmux: a.tmux, Core: a.core, Registry: a.reg,
		Store:    &store.Store{Dir: store.StateDir(getenv)},
		Pane:     getenv("TMUX_PANE"),
		Run:      agent.RunFromEnv(getenv),
		LockPath: filepath.Join(store.RuntimeDir(getenv), "lock"),
	}
	a.side = &sidebar.Sidebar{
		Tmux:     a.tmux,
		LockPath: filepath.Join(store.RuntimeDir(getenv), "sidebar.lock"),
		Command: func(client, origin string) string {
			return shell.Join([]string{a.self, "pick", client, origin})
		},
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

// sweepAgents ends the sessions of agents gone without a word, and clears
// the flags of turns the user cut short: no hook reports either.
func (a *app) sweepAgents(ctx context.Context) error {
	if err := a.env.SweepAgents(ctx); err != nil {
		return err
	}
	return a.env.SweepTurns(ctx)
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
	case "report":
		return a.report(ctx, args)
	case "seen":
		if len(args) > 1 {
			return errUsage
		}
		p, err := a.pane(first(args))
		if err != nil {
			return err
		}
		if err := a.sweepAgents(ctx); err != nil {
			return err
		}
		return a.core.Seen(ctx, p)
	case "status":
		if err := a.sweepAgents(ctx); err != nil {
			return err
		}
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
		if len(args) > 2 {
			return errUsage
		}
		return a.pick(ctx, first(args), first(args[min(1, len(args)):]))
	case "pick-rows":
		return a.pickRows(ctx, args)
	case "pick-key":
		return a.pickKey(ctx, args)
	case "popup":
		return a.popup(ctx, first(args))
	case "sidebar":
		return a.sidebar(ctx, args)
	case "kill-session":
		if len(args) != 1 {
			return errUsage
		}
		if err := a.evacuate(ctx, "="+args[0]+":"); err != nil {
			return err
		}
		return a.core.KillSession(ctx, args[0])
	case "kill":
		if len(args) != 1 {
			return errUsage
		}
		if err := a.evacuate(ctx, args[0]); err != nil {
			return err
		}
		return a.core.Kill(ctx, args[0])
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

// tokens collects --token KEY=VALUE flags.
type tokens map[string]string

func (t tokens) String() string { return "" }

func (t tokens) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return errors.New("want KEY=VALUE")
	}
	t[k] = val
	return nil
}

// report sets or clears a source's label on a pane.
func (a *app) report(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "")
	text := fs.String("text", "", "")
	paneArg := fs.String("pane", "", "")
	ttl := fs.Duration("ttl", 0, "")
	clear := fs.Bool("clear", false, "")
	toks := tokens{}
	fs.Var(toks, "token", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *source == "" || *ttl < 0 {
		return errUsage
	}
	says := *text != "" || len(toks) > 0
	if says == *clear {
		return errUsage // a label, or --clear: one of the two
	}
	p, err := a.pane(*paneArg)
	if err != nil {
		return err
	}
	l := attention.Label{Source: *source}
	if says {
		l.Text, l.Tokens = *text, toks
		if len(toks) == 0 {
			l.Tokens = nil
		}
		if *ttl > 0 {
			l.Until = time.Now().Add(*ttl).Unix()
		}
	}
	return a.env.Report(ctx, p, l)
}

// sweep is what tmux runs when a pane closes: the window's borders, the
// flags of commands that are gone, and the sessions and titles of agents
// that are gone, in one call.
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
	if err := a.sweepAgents(ctx); err != nil {
		return err
	}
	return a.env.SweepTitles(ctx)
}

func (a *app) list(ctx context.Context, args []string) error {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !asJSON {
		return errUsage
	}
	if err := a.sweepAgents(ctx); err != nil {
		return err
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

// panelOf reads the panel's state from the variables fzf sets: the view
// and the open folds, the query, and the width.
func (a *app) panelOf() attention.Panel {
	st := ui.ParsePanelState(a.getenv("FZF_HEADER_LABEL"))
	p := attention.Panel{View: st.View, Open: st.Open, Width: ui.PanelWidth - 1}
	p.Words = attention.QueryWords(a.getenv("FZF_QUERY"))
	if cols, err := strconv.Atoi(a.getenv("FZF_COLUMNS")); err == nil && cols > 1 {
		p.Width = cols - 1
	}
	return p
}

// pickRows prints the panel's rows that match the query, for its search,
// its reloads and a look from the shell. With --fzf they are for fzf: the
// query, the view and the open folds, and the width come from the
// variables fzf sets, and each row ends in a NUL, as a row can have two
// lines.
func (a *app) pickRows(ctx context.Context, args []string) error {
	p := attention.Panel{View: attention.TreeView, Width: ui.PanelWidth - 1}
	end := "\n"
	if len(args) > 0 && args[0] == "--fzf" {
		p = a.panelOf()
		end = "\x00"
	} else {
		p.Words = attention.QueryWords(strings.Join(args, " "))
	}
	rows, err := a.core.PickRows(ctx, p)
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Fprint(a.stdout, ui.PickLine(r)+end)
	}
	return nil
}

// pickKey prints the fzf actions of a key in the panel. tab switches the
// view; enter on a fold (its session id in FOLD) opens it, and on any
// other row accepts it; place CLIENT puts the cursor on the client's
// window after a reload. The state is the header's label (see ui.Pick).
func (a *app) pickKey(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	st := ui.ParsePanelState(a.getenv("FZF_HEADER_LABEL"))
	list := "reload-sync(" + shell.Quote(a.self) + " pick-rows --fzf)"
	width := ui.PanelWidth - 1
	if cols, err := strconv.Atoi(a.getenv("FZF_COLUMNS")); err == nil && cols > 1 {
		width = cols - 1
	}
	switch args[0] {
	case "tab":
		st = st.Next()
		fmt.Fprintln(a.stdout, "change-header-label("+st.String()+")+change-header("+ui.Header(st.View, width)+")+"+list)
	case "place":
		rows, err := a.core.PickRows(ctx, a.panelOf())
		if err != nil {
			return err
		}
		_, here, err := a.where(ctx, first(args[1:]))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "pos(%d)\n", startRow(rows, here))
	case "enter":
		fold := ""
		if len(args) > 1 {
			fold = args[1]
		}
		if fold == "" {
			fmt.Fprintln(a.stdout, "accept")
			return nil
		}
		st.Open = append(st.Open, fold)
		fmt.Fprintln(a.stdout, "change-header-label("+st.String()+")+"+list)
	default:
		return errUsage
	}
	return nil
}

// where is the pane the client shows and its window, "=session:index":
// tmux's current client's when client is "".
func (a *app) where(ctx context.Context, client string) (pane, window string, err error) {
	if client != "" {
		return a.side.ClientPane(ctx, client)
	}
	out, err := a.tmux.Output(ctx, "display-message", "-p", "#{pane_id} =#{session_name}:#{window_index}")
	if err != nil {
		return "", "", err
	}
	pane, window, _ = strings.Cut(out, " ")
	return pane, window, nil
}

// clientArgs is -c client for a tmux command, or nothing for tmux's own
// choice.
func clientArgs(client string) []string {
	if client == "" {
		return nil
	}
	return []string{"-c", client}
}

// pick runs the panel's list. In the client's sidebar pane, Enter closes
// the panel on the chosen pane, Esc goes back to origin first. Run on its
// own, it switches the client to the choice, or back on Esc.
func (a *app) pick(ctx context.Context, client, origin string) error {
	if err := a.sweepAgents(ctx); err != nil {
		return err
	}
	state := ui.PanelState{View: attention.TreeView}
	width := ui.RowWidth(os.Stdin)
	rows, err := a.core.PickRows(ctx, attention.Panel{View: state.View, Width: width})
	if err != nil {
		return err
	}
	startPane, startWindow, err := a.where(ctx, client)
	if err != nil {
		return err
	}
	if origin != "" { // the panel has the focus now
		startPane = origin
	}
	start := startRow(rows, startWindow)
	self := a.getenv("TMUX_PANE")
	inPanel := false
	if client != "" && self != "" {
		p, err := a.side.PanelOf(ctx, client)
		if err != nil {
			return err
		}
		inPanel = p == self
	}
	// The picker waits on the user; only the tmux calls are bounded.
	target, err := ui.Pick(context.WithoutCancel(ctx), rows, ui.PickOptions{
		Self: shell.Quote(a.self), Client: client, Start: start, State: state, Width: width})
	if err != nil {
		return err
	}
	// The user can take longer than the command's deadline: the calls after
	// the choice get their own.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commandTimeout)
	defer cancel()
	if inPanel {
		return a.leavePanel(ctx, client, self, startPane, target)
	}
	if target == "" { // Esc: back to where the panel opened
		if startPane == "" {
			return nil
		}
		_, err := a.tmux.Run(ctx, append(append([]string{"switch-client"}, clientArgs(client)...), "-t", startPane)...)
		return err
	}
	if _, err := a.tmux.Run(ctx, append(append([]string{"switch-client"}, clientArgs(client)...), "-t", target)...); err != nil {
		return err
	}
	// switch-client fires no select hook, so mark the pane it lands on.
	pane, err := a.tmux.Display(ctx, target, "#{pane_id}")
	if err != nil || pane == "" {
		return err
	}
	return a.core.Seen(ctx, pane)
}

// leavePanel closes the panel in pane self after the list ended: on
// target, a window or a pane, or for "" (Esc) back on origin. The pane it
// lands on is marked seen first: closing the panel ends this process.
func (a *app) leavePanel(ctx context.Context, client, self, origin, target string) error {
	if target == "" {
		target = origin
	}
	if target == "" {
		return a.side.ClosePane(ctx, self, "")
	}
	if err := a.side.Go(ctx, client, target); err != nil {
		return err
	}
	land := ""
	if _, rest, _ := strings.Cut(target, ":"); strings.Contains(rest, ".") || strings.HasPrefix(target, "%") {
		p, err := a.tmux.Display(ctx, target, "#{pane_id}")
		if err != nil {
			return err
		}
		land = p
	} else {
		p, err := a.side.Landing(ctx, self)
		if err != nil {
			return err
		}
		land = p
	}
	if land != "" {
		if err := a.core.Seen(ctx, land); err != nil {
			return err
		}
	}
	return a.side.ClosePane(ctx, self, land)
}

// startRow is the row, from 1, the panel's cursor starts on for the
// window ("=session:index"): the window's row (which may go to its agent's
// pane), else its session's fold, else the first session's first row.
func startRow(rows []attention.PickRow, window string) int {
	start := 2
	session, _, _ := strings.Cut(strings.TrimPrefix(window, "="), ":")
	for i, r := range rows {
		if r.Target == window || strings.HasPrefix(r.Target, window+".") {
			return i + 1
		}
		if r.Fold != "" && r.Session == session { // the window is in the fold
			start = i + 1
		}
	}
	return start
}

// popup opens the client's panel, or closes it.
func (a *app) popup(ctx context.Context, client string) error {
	if client == "" {
		c, err := a.tmux.Output(ctx, "display-message", "-p", "#{client_name}")
		if err != nil {
			return err
		}
		client = c
	}
	return a.side.Toggle(ctx, client)
}

// sidebar runs one of the panel's moves.
func (a *app) sidebar(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	skip, err := a.core.Skipper(ctx)
	if err != nil {
		return err
	}
	a.side.Skip = skip
	action, rest := args[0], args[1:]
	switch {
	case action == "go" && len(rest) == 2:
		if err := a.side.Go(ctx, rest[0], rest[1]); err != nil {
			return err
		}
		return a.seenOn(ctx, rest[0])
	case action == "step" && len(rest) == 2:
		if err := a.side.Step(ctx, rest[0], rest[1]); err != nil {
			return err
		}
		return a.seenOn(ctx, rest[0])
	case action == "layout" && len(rest) == 2:
		return a.side.Preset(ctx, rest[0], rest[1])
	case action == "close" && len(rest) == 1:
		return a.side.Close(ctx, rest[0])
	case action == "follow" && len(rest) == 0:
		return a.side.Follow(ctx)
	case action == "keep" && len(rest) == 0:
		return a.side.Keep(ctx)
	case action == "reload" && len(rest) == 0:
		return a.side.Reload(ctx)
	}
	return errUsage
}

// seenOn marks the pane the client shows seen, as a select hook would,
// unless it is the client's panel: the cursor moving in the panel only
// shows a window.
func (a *app) seenOn(ctx context.Context, client string) error {
	if client == "" {
		return nil
	}
	pane, _, err := a.side.ClientPane(ctx, client)
	if err != nil || pane == "" {
		return err
	}
	if p, err := a.side.PanelOf(ctx, client); err != nil || p == pane {
		return err
	}
	return a.core.Seen(ctx, pane)
}

// evacuate moves the panels out of what killing target takes with it.
func (a *app) evacuate(ctx context.Context, target string) error {
	skip, err := a.core.Skipper(ctx)
	if err != nil {
		return err
	}
	a.side.Skip = skip
	return a.side.Evacuate(ctx, target)
}

func (a *app) jump(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("jump", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	paneArg := fs.String("pane", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return errUsage
	}
	if *paneArg == "" {
		if err := a.sweepAgents(ctx); err != nil {
			return err
		}
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

// tmuxHooks are the tmux events tmux-agent follows, and what it runs. A
// sidebar hook runs only while a panel is open (@sidebar_open), so the
// many layout changes cost nothing otherwise.
var tmuxHooks = []struct {
	event, command string
	sidebar        bool
}{
	{"pane-focus-in", "seen #{pane_id}", false},
	{"after-select-window", "seen #{pane_id}", false},
	{"after-select-pane", "seen #{pane_id}", false}, // and sidebar.RecordLast
	{"after-split-window", "borders #{window_id}", false},
	{"after-kill-pane", "sweep --window #{window_id}", false},
	{"pane-exited", "sweep --window #{window_id}", false},
	{"session-closed", "status", false},
	{"client-focus-in", "status", false},
	{"session-window-changed", "sidebar follow", true},
	{"client-session-changed", "sidebar follow", true},
	{"client-detached", "sidebar follow", true},
	{"window-layout-changed", "sidebar keep", true},
	{"window-resized", "sidebar keep", true},
	{"after-select-layout", "sidebar keep", true},
	{"window-linked", "sidebar reload", true},
	{"window-unlinked", "sidebar reload", true},
	{"window-renamed", "sidebar reload", true},
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
		cmd := fmt.Sprintf(`run-shell -b "tmux-agent %s"`, h.command)
		if h.sidebar {
			cmd = fmt.Sprintf(`if-shell -F "#{%s}" '%s'`, sidebar.OptOpen, cmd)
		}
		if h.event == "after-select-pane" {
			cmd += " ; " + sidebar.RecordLast
		}
		args = append(args, "set-hook", "-g", fmt.Sprintf("%s[%d]", h.event, hookIndex), cmd)
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
