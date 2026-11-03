// Package cli is tmux-revive's command line: it wires the tmux client and
// the snapshot store to the reviver, and runs a subcommand.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/influento/tmux-plugins/internal/lockfile"
	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/tmux-revive/internal/revive"
	"github.com/influento/tmux-plugins/tmux-revive/internal/snapshot"
)

const usage = `usage:
  tmux-revive attach                 the terminal's entry point: restore once
                                     per server, then attach
  tmux-revive restore [--from TIME]  add back what the running server lacks,
                                     from the newest snapshot or the newest
                                     one at or before TIME
  tmux-revive save [--settle DUR] [-v]
                                     save now; tmux hooks and the shell run it
  tmux-revive list                   the kept snapshots, newest last
  tmux-revive forget-session NAME    leave NAME out of every later snapshot;
                                     prefix X runs it before kill-session
  tmux-revive capture                save every pane's scrollback now
  tmux-revive shell-init zsh         zsh hooks: save on cd, and when a
                                     restorable program starts or ends
  tmux-revive tmux-init              set tmux-revive's hooks at index [20],
                                     and start the scrollback timer
  tmux-revive --version
internal:
  tmux-revive paint FILE             print saved scrollback, then become the shell
  tmux-revive scrollback-loop        capture scrollback every
                                     @revive_scrollback_interval seconds
`

// hookIndex is tmux-revive's slot in each tmux hook array; tmux-agent has
// [10]. Setting an index again replaces it.
const hookIndex = 20

var errUsage = errors.New("usage")

// app is one run of the command.
type app struct {
	version string
	stdout  io.Writer
	stderr  io.Writer
	getenv  func(string) string
	tmux    *tmux.Client
	store   *snapshot.Store
	r       *revive.Reviver
	// exec replaces the process with argv; syscall.Exec outside tests.
	exec func(argv []string) error
}

func newApp(version string, stdout, stderr io.Writer, getenv func(string) string) *app {
	a := &app{version: version, stdout: stdout, stderr: stderr, getenv: getenv, exec: execve}
	a.tmux = &tmux.Client{Timeout: 5 * time.Second}
	a.store = &snapshot.Store{Dir: revive.StateDir(getenv)}
	self, _ := os.Executable() // unknown: restored panes start without their scrollback
	a.r = &revive.Reviver{Tmux: a.tmux, Store: a.store, LockDir: revive.RuntimeDir(getenv), Self: self}
	return a
}

// Main runs tmux-revive with args (without the program name) and returns
// the exit code.
func Main(version string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	a := newApp(version, stdout, stderr, os.Getenv)
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "--version", "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	err := a.command(cmd, rest)
	if errors.Is(err, errUsage) {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "tmux-revive %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

func (a *app) command(cmd string, args []string) error {
	switch cmd {
	case "attach":
		return a.attach(args)
	case "restore":
		return a.restore(args)
	case "save":
		return a.save(args)
	case "list":
		return a.list(args)
	case "forget-session":
		if len(args) != 1 || args[0] == "" {
			return errUsage
		}
		return a.withTimeout(70*time.Second, func(ctx context.Context) error { return a.r.Forget(ctx, args[0]) })
	case "capture":
		if len(args) != 0 {
			return errUsage
		}
		return a.withTimeout(2*time.Minute, a.r.Capture)
	case "shell-init":
		if len(args) != 1 {
			return errUsage
		}
		return a.shellInit(args[0])
	case "tmux-init":
		if len(args) != 0 {
			return errUsage
		}
		return a.withTimeout(70*time.Second, a.tmuxInit)
	case "paint":
		if len(args) != 1 {
			return errUsage
		}
		return a.paint(args[0])
	case "scrollback-loop":
		if len(args) != 0 {
			return errUsage
		}
		return a.scrollbackLoop()
	}
	return errUsage
}

func (a *app) withTimeout(d time.Duration, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return fn(ctx)
}

// attach restores, then becomes the tmux client. Whatever fails, the
// terminal still gets a tmux session.
func (a *app) attach(args []string) error {
	if len(args) != 0 {
		return errUsage
	}
	var target string
	err := a.withTimeout(3*time.Minute, func(ctx context.Context) error {
		var err error
		target, err = a.r.Attach(ctx)
		return err
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "tmux-revive attach: %v\n", err)
	}
	argv := a.tmuxArgv("new-session", "-A", "-s", "main")
	if target != "" {
		argv = a.tmuxArgv("attach-session", "-t", "="+target)
	}
	return a.exec(argv)
}

// tmuxArgv is a tmux command line on the reviver's server.
func (a *app) tmuxArgv(args ...string) []string {
	argv := []string{"tmux"}
	if a.tmux.Socket != "" {
		argv = append(argv, "-S", a.tmux.Socket)
	}
	return append(argv, args...)
}

func execve(argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}

func (a *app) restore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	from := fs.String("from", "", "the newest snapshot at or before this time, or a snapshot file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errUsage
	}
	snap, e, err := a.pick(*from)
	if err != nil {
		return err
	}
	return a.withTimeout(3*time.Minute, func(ctx context.Context) error {
		rep, err := a.r.Restore(ctx, snap, e)
		fmt.Fprint(a.stdout, rep.Summary())
		return err
	})
}

// pick finds the snapshot restore uses: the newest, the newest at or
// before a time, or a file.
func (a *app) pick(from string) (*snapshot.Snapshot, snapshot.Entry, error) {
	if strings.HasSuffix(from, ".json") {
		path := from
		if !strings.ContainsRune(path, '/') {
			path = filepath.Join(a.store.Dir, "snapshots", path)
		}
		snap, err := a.store.Load(path)
		return snap, snapshot.Entry{Path: path, Time: timeOf(snap)}, err
	}
	var (
		snap *snapshot.Snapshot
		e    snapshot.Entry
		ok   bool
		err  error
	)
	if from == "" {
		snap, e, ok, err = a.store.Latest()
	} else {
		t, perr := parseWhen(from, time.Now())
		if perr != nil {
			return nil, e, perr
		}
		snap, e, ok, err = a.store.At(t)
	}
	if err != nil {
		return nil, e, err
	}
	if !ok {
		return nil, e, errors.New("no snapshot to restore")
	}
	return snap, e, nil
}

func timeOf(s *snapshot.Snapshot) time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.Time
}

// parseWhen reads a local time: a date and time, or a time of day today.
func parseWhen(s string, now time.Time) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			y, m, d := now.Date()
			return time.Date(y, m, d, t.Hour(), t.Minute(), t.Second(), 0, now.Location()), nil
		}
	}
	return time.Time{}, fmt.Errorf("unknown time %q: use 15:04[:05] or 2006-01-02 15:04[:05]", s)
}

func (a *app) save(args []string) error {
	fs := flag.NewFlagSet("save", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	settle := fs.Duration("settle", 100*time.Millisecond, "wait before reading tmux, so a burst of changes saves once")
	verbose := fs.Bool("v", false, "print what the save did")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errUsage
	}
	return a.withTimeout(70*time.Second, func(ctx context.Context) error {
		res, err := a.r.Save(ctx, *settle)
		if err == nil && *verbose {
			fmt.Fprintln(a.stdout, res)
		}
		return err
	})
}

func (a *app) list(args []string) error {
	if len(args) != 0 {
		return errUsage
	}
	es, err := a.store.Entries()
	if err != nil {
		return err
	}
	if len(es) == 0 {
		fmt.Fprintln(a.stdout, "no snapshots")
		return nil
	}
	for _, e := range es {
		snap, err := a.store.Load(e.Path)
		if err != nil {
			fmt.Fprintf(a.stdout, "%s  unreadable: %v\n", e.Time.Local().Format("2006-01-02 15:04:05"), err)
			continue
		}
		fmt.Fprintf(a.stdout, "%s  %d sessions  %d windows  %d panes  %s\n",
			e.Time.Local().Format("2006-01-02 15:04:05"), len(snap.Sessions), snap.Windows(), snap.Panes(), filepath.Base(e.Path))
	}
	return nil
}

// saveHooks are the tmux events after which tmux-revive saves.
var saveHooks = []string{
	"session-created", "session-closed", "session-renamed", "client-session-changed",
	"window-linked", "window-unlinked", "after-rename-window", "window-layout-changed",
	"session-window-changed", "window-pane-changed",
}

// saveCommand is what each hook runs: a save in the background.
const saveCommand = `run-shell -b "tmux-revive save"`

// optionHook saves after set-option, but only for the options a snapshot
// holds: tmux-agent sets other pane options on every agent event.
var optionHook = fmt.Sprintf(`if-shell -F "#{||:#{==:#{hook_argument_0},%s},#{==:#{hook_argument_0},automatic-rename}}" %q`,
	revive.OptCommand, `run-shell -b 'tmux-revive save'`)

// TmuxHookEvents lists the events tmux-init sets, for tests.
func TmuxHookEvents() []string {
	return append(saveHooks[:len(saveHooks):len(saveHooks)], "after-set-option")
}

// tmuxInit sets tmux-revive's hooks at its own index, so prefix r leaves no
// duplicates and tmux-agent's index stays untouched. On a server that
// already has sessions (prefix r, or the first run after an upgrade) it
// also marks the server ready and saves. It starts the scrollback timer.
func (a *app) tmuxInit(ctx context.Context) error {
	var args []string
	for _, ev := range saveHooks {
		args = append(args, "set-hook", "-g", fmt.Sprintf("%s[%d]", ev, hookIndex), saveCommand, ";")
	}
	args = append(args, "set-hook", "-g", fmt.Sprintf("after-set-option[%d]", hookIndex), optionHook, ";")
	args = append(args, "run-shell", "-b", "tmux-revive scrollback-loop")
	if _, err := a.tmux.RunAll(ctx, args...); err != nil {
		return err
	}
	out, err := a.tmux.Output(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil || out == "" {
		// At server start tmux.conf runs before the first session exists:
		// attach decides whether to restore, and marks the server.
		return nil
	}
	if _, ok, err := a.tmux.Option(ctx, "-g", "", revive.OptReady); err != nil || ok {
		return err
	}
	if _, err := a.tmux.Run(ctx, "set-option", "-g", revive.OptReady, "1"); err != nil {
		return err
	}
	_, err = a.r.Save(ctx, 0)
	return err
}

func (a *app) shellInit(shell string) error {
	if shell != "zsh" {
		return fmt.Errorf("shell-init: %q is not supported; only zsh", shell)
	}
	progs := revive.DefaultPrograms
	if a.getenv("TMUX") != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if v, ok, err := a.tmux.Option(ctx, "-g", "", revive.OptPrograms); err == nil && ok {
			progs = v
		}
	}
	var quoted []string
	for _, p := range strings.Fields(progs) {
		quoted = append(quoted, zshQuote(p))
	}
	fmt.Fprintf(a.stdout, zshInit, strings.Join(quoted, " "))
	return nil
}

// zshInit saves the pane's folder on each cd, outside subshells, so that
// (cd x && make) saves nothing; and saves when a restorable program starts
// (after a short wait, so the save sees it running) and when it ends.
const zshInit = `# tmux-revive: save the pane's folder on cd, and its program when one starts or ends
typeset -ga _tmux_revive_programs=(%s)
typeset -gi _tmux_revive_ran=0
_tmux_revive_save() { command tmux-revive save --settle 0.2s >/dev/null 2>&1 &! }
_tmux_revive_chpwd() { (( ZSH_SUBSHELL )) || _tmux_revive_save }
_tmux_revive_preexec() {
  local cmd=${${(z)1}[1]}
  if (( ${_tmux_revive_programs[(Ie)${cmd:t}]} )); then
    _tmux_revive_ran=1
    _tmux_revive_save
  fi
}
_tmux_revive_precmd() {
  (( _tmux_revive_ran )) || return 0
  _tmux_revive_ran=0
  _tmux_revive_save
}
autoload -Uz add-zsh-hook
add-zsh-hook chpwd _tmux_revive_chpwd
add-zsh-hook preexec _tmux_revive_preexec
add-zsh-hook precmd _tmux_revive_precmd
`

func zshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// paint prints the pane's saved scrollback, then becomes the pane's shell
// as tmux would have started it: default-command through default-shell,
// or default-shell as a login shell.
func (a *app) paint(file string) error {
	if err := revive.Paint(a.stdout, file); err != nil {
		fmt.Fprintf(a.stderr, "tmux-revive paint: %v\n", err)
	}
	shell, command := "", ""
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if v, ok, err := a.tmux.Option(ctx, "-g", "", "default-shell"); err == nil && ok {
		shell = v
	}
	if v, ok, err := a.tmux.Option(ctx, "-g", "", "default-command"); err == nil && ok {
		command = v
	}
	cancel()
	if shell == "" {
		shell = a.getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	argv := []string{"-" + filepath.Base(shell)}
	if command != "" {
		argv = []string{filepath.Base(shell), "-c", command}
	}
	err := syscall.Exec(shell, argv, os.Environ())
	// The shell could not start; any shell is better than a pane that
	// closes at once.
	return errors.Join(err, syscall.Exec("/bin/sh", []string{"-sh"}, os.Environ()))
}

// scrollbackLoop captures scrollback and saves every
// @revive_scrollback_interval seconds, for as long as the server lives. One
// loop runs per server: tmux-init starts one on every run, and the others
// find the lock taken and end.
func (a *app) scrollbackLoop() error {
	socket, _, _ := strings.Cut(a.getenv("TMUX"), ",")
	h := fnv.New64a()
	h.Write([]byte(socket))
	unlock, ok, err := lockfile.TryLock(filepath.Join(a.r.LockDir, fmt.Sprintf("scrollback-%x.lock", h.Sum64())))
	if err != nil || !ok {
		return err
	}
	defer unlock()
	alive := func() bool {
		if socket != "" {
			_, err := os.Stat(socket)
			return err == nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := a.tmux.Run(ctx, "list-sessions")
		return err == nil
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		interval := a.r.ScrollbackInterval(ctx)
		cancel()
		if interval == 0 {
			return nil
		}
		for range interval {
			if !alive() {
				return nil
			}
			time.Sleep(time.Second)
		}
		a.tick()
	}
}

// tick is one round of the scrollback timer: a save, which also sees
// programs started since the last tmux event and gives new panes their
// ids, then a capture, which keeps only panes with an id.
func (a *app) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := a.r.Save(ctx, 0); err != nil {
		a.store.Logf(time.Now(), "timed save: %v", err)
	}
	if err := a.r.Capture(ctx); err != nil {
		a.store.Logf(time.Now(), "scrollback capture: %v", err)
	}
}
