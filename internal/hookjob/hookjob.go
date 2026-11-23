// Package hookjob runs a plugin's command as the job of a tmux hook. tmux
// prints a run-shell -b job's output, and its exit status when not 0, into
// the current pane of the job's client; tmux 3.8 crashes when that
// client's session has no window (cmd-run-shell.c:94), as one of
// tmux-overlay's has while it closes. So a hook's job prints nothing and
// exits 0, and what it would print goes to the plugin's log. A job that
// dies outside Go's error handling (the runtime's fatal errors, a shell
// that cannot start it) still exits non-zero: the hook's command line ends
// in Guard, and the runtime writes its fatal error to the log.
package hookjob

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"syscall"
)

// Arg comes before the command that a tmux hook runs: `run-shell -b
// "<plugin> --tmux-hook <command>"`.
const Arg = "--tmux-hook"

// Guard ends the shell command line of a hook's job: tmux shows a hook
// job's exit status when not 0, and never its stderr.
const Guard = " || true"

// Run runs main, which writes its output and errors to out and returns an
// exit code, and gives logf, as one line, whatever it wrote, its exit code
// when not 0, or its panic. The runtime writes a fatal error to the file
// at crashLog. It returns the exit code of the job: 0.
func Run(command []string, crashLog string, main func(out io.Writer) int, logf func(line string)) int {
	if f, err := os.OpenFile(crashLog, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600); err == nil {
		_ = debug.SetCrashOutput(f, debug.CrashOptions{}) // best effort: the job runs without it
		_ = f.Close()                                     // SetCrashOutput keeps its own copy
	}
	var out bytes.Buffer
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(&out, "panic: %v", r)
		}
		if out.Len() > 0 {
			logf(fmt.Sprintf("tmux hook %s: %s", strings.Join(command, " "), strings.TrimSpace(out.String())))
		}
	}()
	if code := main(&out); code != 0 {
		fmt.Fprintf(&out, "exit %d", code)
	}
	return 0
}
