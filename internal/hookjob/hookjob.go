// Package hookjob runs a plugin's command as the job of a tmux hook. tmux
// prints a run-shell -b job's output, and its exit status when not 0, into
// the current pane of the job's client; tmux 3.8 crashes when that
// client's session has no window (cmd-run-shell.c:94), as one of
// tmux-overlay's has while it closes. So a hook's job prints nothing and
// exits 0, and what it would print goes to the plugin's log.
package hookjob

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Arg comes before the command that a tmux hook runs: `run-shell -b
// "<plugin> --tmux-hook <command>"`.
const Arg = "--tmux-hook"

// Run runs main, which writes its output and errors to out and returns an
// exit code, and gives logf, as one line, whatever it wrote, its exit code
// when not 0, or its panic. It returns the exit code of the job: 0.
func Run(command []string, main func(out io.Writer) int, logf func(line string)) int {
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
