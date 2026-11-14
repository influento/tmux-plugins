package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

// contractVersion is the version of the JSON that start and close print
// for tools: a field's meaning changes only with it.
const contractVersion = 1

// startScript runs the launcher, then leaves a login shell in the window,
// as an agent typed into a shell would: $1 is tmux-agent, $2 the shell.
const startScript = `self=$1 sh=$2; shift 2; "$self" run "$@"; unset ` + agent.StartEnv + `; exec "$sh" -l`

// exitNameInUse is start's exit code when a window has the name already.
const exitNameInUse = 3

// startedJSON is what `start --json` prints.
type startedJSON struct {
	Version      int    `json:"version"`
	Pane         string `json:"pane"`
	Window       string `json:"window"`
	Session      string `json:"session"`
	Name         string `json:"name,omitempty"`
	Agent        string `json:"agent"`
	AgentSession string `json:"agent_session,omitempty"`
}

// start opens a window that runs an agent through the launcher, and
// returns once the agent is ready. It prints the new pane, or with --json
// what it opened.
func (a *app) start(args []string) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")
	cwd := fs.String("cwd", "", "")
	session := fs.String("session", "", "")
	focus := fs.Bool("focus", false, "")
	timeout := fs.Duration("timeout", 30*time.Second, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 || *timeout <= 0 {
		fmt.Fprint(a.stderr, usage)
		return 2
	}
	fail := func(code int, format string, args ...any) int {
		fmt.Fprintf(a.stderr, "tmux-agent start: "+format+"\n", args...)
		return code
	}
	ag := a.reg.Get(fs.Arg(0))
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	target, err := a.startTarget(ctx, *session)
	if err != nil {
		return fail(1, "%v", err)
	}
	if *name != "" {
		w, err := a.windowNamed(ctx, *name)
		if err != nil {
			return fail(1, "%v", err)
		}
		if w != "" {
			return fail(exitNameInUse, "a window %s is open: %s", *name, w)
		}
	}
	dir, err := a.startDir(*cwd)
	if err != nil {
		return fail(1, "%v", err)
	}
	sh, _, err := a.tmux.Option(ctx, "-g", "", "default-shell")
	if err != nil {
		return fail(1, "%v", err)
	}
	channel := agent.NewStartChannel()
	cmd := []string{"new-window", "-P", "-F", "#{pane_id}\x1f#{window_id}\x1f#{session_name}",
		"-t", target, "-c", dir, "-e", agent.StartEnv + "=" + channel}
	if !*focus {
		cmd = append(cmd, "-d")
	}
	if *name != "" {
		cmd = append(cmd, "-n", *name)
	}
	cmd = append(cmd, "sh", "-c", startScript, "tmux-agent-start", a.self, cmp.Or(sh, "/bin/sh"), ag.Name())
	cmd = append(cmd, fs.Args()[1:]...)
	out, err := a.tmux.Output(ctx, cmd...)
	if err != nil {
		return fail(1, "%v", err)
	}
	f := strings.SplitN(out, "\x1f", 3)
	if len(f) != 3 {
		return fail(1, "new-window printed %q", out)
	}
	opened := startedJSON{Version: contractVersion, Pane: f[0], Window: f[1], Session: f[2], Name: *name, Agent: ag.Name()}

	waitErr := a.tmux.WaitFor(ctx, channel)
	// The wait used the time; what follows gets its own.
	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	if waitErr != nil {
		// No one waits now: a late start of the session signals nothing.
		_, _ = a.tmux.Run(readCtx, "set-option", "-pu", "-t", opened.Pane, agent.OptStarting) // the pane may be gone
		if ctx.Err() != nil {
			return fail(1, "%s in %s is not ready after %s", ag.Name(), opened.Pane, *timeout)
		}
		return fail(1, "%v", waitErr)
	}
	got, err := a.env.StartedIn(readCtx, opened.Pane)
	if err != nil {
		return fail(1, "%v", err)
	}
	if got.Session == "" && !got.Running {
		return fail(1, "%s in %s ended before it was ready%s", ag.Name(), opened.Pane, when(got.Reason))
	}
	opened.AgentSession = got.Session
	if *asJSON {
		if err := json.NewEncoder(a.stdout).Encode(opened); err != nil {
			return fail(1, "%v", err)
		}
		return 0
	}
	fmt.Fprintln(a.stdout, opened.Pane)
	return 0
}

// when is a reason as the end of a sentence.
func when(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

// startTarget is the session a start opens its window in: the one named,
// else the caller's.
func (a *app) startTarget(ctx context.Context, session string) (string, error) {
	if session != "" {
		return "=" + session + ":", nil
	}
	pane := a.getenv("TMUX_PANE")
	if pane == "" {
		return "", errors.New("not inside tmux: pass --session")
	}
	id, err := a.tmux.Display(ctx, pane, "#{session_id}")
	if err != nil {
		return "", err
	}
	return id + ":", nil
}

// startDir is the folder the agent starts in: the one given, else the
// caller's.
func (a *app) startDir(dir string) (string, error) {
	if dir == "" {
		return os.Getwd()
	}
	dir, err := filepath.Abs(expandHome(dir, a.getenv("HOME")))
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("no folder %s", dir)
	}
	return dir, nil
}

// window is one window of the server.
type window struct{ id, session, name string }

// windows lists every window of the server.
func (a *app) windows(ctx context.Context) ([]window, error) {
	out, err := a.tmux.Output(ctx, "list-windows", "-a", "-F", "#{window_id}\x1f#{session_name}\x1f#{window_name}")
	if err != nil {
		return nil, err
	}
	var ws []window
	for _, l := range strings.Split(out, "\n") {
		if f := strings.SplitN(l, "\x1f", 3); len(f) == 3 {
			ws = append(ws, window{f[0], f[1], f[2]})
		}
	}
	return ws, nil
}

// windowNamed is the id of a window with the name, or "".
func (a *app) windowNamed(ctx context.Context, name string) (string, error) {
	ws, err := a.windows(ctx)
	if err != nil {
		return "", err
	}
	for _, w := range ws {
		if w.name == name {
			return w.id, nil
		}
	}
	return "", nil
}

// closedJSON is what `close --json` prints.
type closedJSON struct {
	Version int          `json:"version"`
	Closed  []windowJSON `json:"closed"`
}

type windowJSON struct {
	Window  string `json:"window"`
	Session string `json:"session"`
	Name    string `json:"name"`
}

// closeWindows closes every window with one of the names, in any session,
// as the panel's kill does. The caller's own window goes last: closing it
// ends the caller. It prints each window it closed, or with --json all of
// them; a name no window has is no error.
func (a *app) closeWindows(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("close", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return errUsage
	}
	ws, err := a.windows(ctx)
	if err != nil {
		return err
	}
	own := ""
	if pane := a.getenv("TMUX_PANE"); pane != "" {
		if own, err = a.tmux.Display(ctx, pane, "#{window_id}"); err != nil {
			own = "" // the caller's pane is gone: nothing of it to keep
		}
	}
	var others, last []window
	for _, w := range ws {
		switch {
		case !slices.Contains(fs.Args(), w.name):
		case w.id == own:
			last = append(last, w)
		default:
			others = append(others, w)
		}
	}
	var closed []window
	for _, w := range others {
		if err := a.closeWindow(ctx, w); err != nil {
			return err
		}
	}
	if len(others) > 0 {
		// A window the core keeps (the last session's) is not closed.
		if ws, err = a.windows(ctx); err != nil {
			return err
		}
		for _, w := range others {
			if !slices.ContainsFunc(ws, func(x window) bool { return x.id == w.id }) {
				closed = append(closed, w)
			}
		}
	}
	// The caller's window: what it closes is said first, since it ends
	// the caller.
	if err := a.printClosed(append(closed, last...), *asJSON); err != nil {
		return err
	}
	for _, w := range last {
		if err := a.closeWindow(ctx, w); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) closeWindow(ctx context.Context, w window) error {
	target := "=" + w.session + ":" + w.id
	if err := a.evacuate(ctx, target); err != nil {
		return err
	}
	return a.core.Kill(ctx, target)
}

func (a *app) printClosed(ws []window, asJSON bool) error {
	if asJSON {
		out := closedJSON{Version: contractVersion, Closed: []windowJSON{}}
		for _, w := range ws {
			out.Closed = append(out.Closed, windowJSON{w.id, w.session, w.name})
		}
		return json.NewEncoder(a.stdout).Encode(out)
	}
	for _, w := range ws {
		if _, err := fmt.Fprintf(a.stdout, "%s %s\n", w.id, w.name); err != nil {
			return err
		}
	}
	return nil
}
