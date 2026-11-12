package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

// Launch runs agent a with args as its child on the same terminal and waits.
// It never reads or writes the terminal, so keys, colours and resizing reach
// the agent unchanged. self is the launcher's own command line (os.Args).
//
// It returns the code to exit with: the agent's. When the agent died from
// a signal, the launcher dies from the same one where it can, so loops and
// && chains stop as they would without it.
//
// Outside tmux, or when called from inside another agent (a tool call), it
// runs the agent in its place and records nothing.
func (e *Env) Launch(a Agent, args, self []string, stderr io.Writer) int {
	path, err := exec.LookPath(a.Command())
	if err != nil {
		fmt.Fprintf(stderr, "tmux-agent: %v\n", err)
		return 127
	}
	argv := append([]string{a.Command()}, args...)
	if e.Pane == "" || InsideAgent(e.Registry.ProcessNames()) {
		err := syscall.Exec(path, argv, os.Environ())
		fmt.Fprintf(stderr, "tmux-agent: %s: %v\n", path, err)
		return 126
	}
	// tmux names the pane after the argv[0] of the terminal's foreground
	// process group leader, which is this launcher. The agent's name there
	// keeps the window name, the flags' owner and the borders as they are
	// without the launcher.
	if name := CommandName(a); len(self) > 0 && self[0] != name {
		if exe, err := os.Executable(); err == nil {
			err = syscall.Exec(exe, append([]string{name}, self[1:]...), os.Environ())
			e.logf("re-exec as %s: %v", name, err)
		}
	}

	oneShot := false
	if o, ok := a.(OneShotter); ok {
		oneShot = o.OneShot(args)
	}
	e.register(a, args, oneShot)

	// Ctrl-C and Ctrl-\ reach the agent from the terminal; the launcher
	// catches them so it outlives them, and does not ignore them, which the
	// agent would inherit. SIGTERM and SIGHUP are passed on: a kill.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	proc, err := os.StartProcess(path, argv, &os.ProcAttr{
		Env:   runEnv(os.Getpid()),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		signal.Stop(sigs)
		fmt.Fprintf(stderr, "tmux-agent: %v\n", err)
		e.finish(a, oneShot, false, 126, "exit 126")
		return 126
	}
	pid := proc.Pid
	var killed atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for s := range sigs {
			if s == syscall.SIGTERM || s == syscall.SIGHUP {
				killed.Store(true)
				_ = syscall.Kill(pid, s.(syscall.Signal)) // gone already: nothing to pass on
			}
		}
	}()

	ws, err := waitAgent(pid)
	_ = proc.Release()
	signal.Stop(sigs)
	close(sigs)
	<-done
	code, desc := 0, "exit 0"
	switch {
	case err != nil:
		code, desc = 1, "lost"
		e.logf("wait for %s: %v", a.Name(), err)
	case ws.Signaled():
		code, desc = 128+int(ws.Signal()), ws.Signal().String()
	default:
		code, desc = ws.ExitStatus(), "exit "+strconv.Itoa(ws.ExitStatus())
	}
	e.finish(a, oneShot, killed.Load(), code, desc)
	if err == nil && ws.Signaled() {
		dieBy(ws.Signal())
	}
	return code
}

// waitAgent waits for the agent to end. A TUI reads Ctrl-Z as a key and
// stops only itself; the launcher then stops itself the same way, so the
// shell gets the terminal back, and fg continues both.
func waitAgent(pid int) (syscall.WaitStatus, error) {
	for {
		var ws syscall.WaitStatus
		_, err := syscall.Wait4(pid, &ws, syscall.WUNTRACED, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return ws, err
		}
		if !ws.Stopped() {
			return ws, nil
		}
		// A Ctrl-Z the terminal turns into a signal stops the launcher too,
		// and fg continues both; a stop read just before the launcher's own
		// is over by the time it runs again.
		if !isStopped(pid) {
			continue
		}
		stopSelf(ws.StopSignal())
		// Continued: by fg, which continues the agent too, or by a signal
		// to the launcher alone, which must not leave the agent stopped.
		_ = syscall.Kill(pid, syscall.SIGCONT)
	}
}

// stopSelf stops the launcher with sig and returns once it is continued.
// The signal goes to this thread, so the stop takes hold before the call
// returns. Sent to the process, another thread could take it while this one
// went on to continue the agent, which would then meet a terminal that is
// no longer its own, and stop the whole job again.
func stopSelf(sig syscall.Signal) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_ = syscall.Tgkill(os.Getpid(), syscall.Gettid(), sig)
}

// dieBy ends the launcher with the agent's signal. Go's runtime dies from
// SIGINT, SIGTERM and SIGHUP once nothing listens for them, and nothing
// survives SIGKILL; any other signal becomes the shell's 128+n.
func dieBy(sig syscall.Signal) {
	switch sig {
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGKILL:
		signal.Reset(sig)
		_ = syscall.Kill(os.Getpid(), sig)
		time.Sleep(100 * time.Millisecond) // the signal is on its way; if it was ignored at start, fall through
	}
	os.Exit(128 + int(sig))
}

// register marks the pane as running this launcher's run, before the agent
// starts, so its first hook finds the run. A run replaces whatever the
// pane's last agent left.
func (e *Env) register(a Agent, args []string, oneShot bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer e.lock(ctx)()
	resume := ""
	if r, ok := a.(Resumer); ok {
		resume = r.ResumedSession(args)
	}
	shot := ""
	if oneShot {
		shot = "1"
	}
	opts := map[string]string{
		optAgent: a.Name(), optRun: strconv.Itoa(os.Getpid()), optOneShot: shot, optResume: resume,
		optStarted: strconv.FormatInt(e.now().Unix(), 10),
		optSession: "", optTranscript: "", optCwd: "", optState: "", optReason: "", optWait: "", optWaitBy: "", OptRevive: "",
	}
	if err := e.paneOpts(ctx, opts); err != nil {
		e.logf("register run: %v", err)
		return
	}
	if oneShot {
		// One turn, working from the start; the launcher ends it.
		if _, err := e.flag(ctx, a, attention.Working, ""); err != nil {
			e.logf("flag run: %v", err)
		}
	}
}

// finish records the run's end. A kill keeps the pane's session for a
// resume; an agent that ended by itself lets it go. Either way the window
// title the agent set goes, as at the agent's own end. A one-shot run ends as
// done or failed, and an interactive one that failed is flagged; these
// flags have no owner, since the run they report on is over.
func (e *Env) finish(a Agent, oneShot, killed bool, code int, desc string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer e.lock(ctx)()
	l, err := e.readLive(ctx)
	if err != nil || l.run != strconv.Itoa(os.Getpid()) {
		return // the pane is gone, or another run took it
	}
	var flagErr error
	switch {
	case killed:
	case code != 0:
		_, _, flagErr = e.Core.Set(ctx, e.Pane, attention.Flag{State: attention.Done, Reason: attention.FailedPrefix + desc, Failed: true})
	case oneShot:
		_, _, flagErr = e.Core.Set(ctx, e.Pane, attention.Flag{State: attention.Done, Reason: "finished"})
	default:
		flagErr = e.Core.Clear(ctx, e.Pane)
	}
	if flagErr != nil {
		e.logf("flag end: %v", flagErr)
	}
	// A run that ends without a word from the agent, such as a resume that
	// fails, still gives the window its name back.
	if err := e.releaseTitle(ctx, e.Pane, l); err != nil {
		e.logf("title end: %v", err)
	}
	opts := map[string]string{optRun: "", optOneShot: "", optResume: "", optStarted: "", optNamed: ""}
	end := EndKilled
	if !killed {
		end = EndExited
		for _, o := range sessionOpts {
			opts[o] = ""
		}
	}
	if err := e.paneOpts(ctx, opts); err != nil {
		e.logf("end run: %v", err)
	}
	if err := e.dropGit(ctx, e.Pane); err != nil {
		e.logf("git label end: %v", err)
	}
	e.markEnded(a, l.session, end, desc)
}
