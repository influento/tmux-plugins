// Package tmux runs tmux commands against one server.
//
// Every call has a timeout, because callers such as hook handlers must never
// block the program that started them on a hung server.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds one tmux command when the Client sets none.
const DefaultTimeout = 5 * time.Second

// Client talks to one tmux server. Code that needs a fake in unit tests
// declares an interface with the methods it uses; Client satisfies it.
type Client struct {
	// Socket is the server's socket path (tmux -S). Empty means the server
	// tmux itself would pick: the one in $TMUX, else the default socket.
	Socket string
	// Config is the file a command loads if it starts a server (tmux -f).
	// Empty means tmux's own default.
	Config string
	// Env, when not nil, is the whole environment of each tmux process.
	Env []string
	// Timeout bounds each command. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Error is a tmux command that failed. Stderr holds what tmux printed.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("tmux %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Run runs tmux with args and returns its stdout unchanged.
func (c *Client) Run(ctx context.Context, args ...string) (string, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", c.argv(args)...)
	cmd.Env = c.Env
	// tmux forks the server from the first client; the server must not keep
	// this process's pipes open, or Output would wait for the server to exit.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w: %w", err, ctx.Err())
		}
		return stdout.String(), &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.String(), nil
}

// Output runs tmux with args and returns stdout without its final newline.
func (c *Client) Output(ctx context.Context, args ...string) (string, error) {
	out, err := c.Run(ctx, args...)
	return strings.TrimSuffix(out, "\n"), err
}

// Display expands format for target, as display-message -p does. An empty
// target means tmux's current pane.
func (c *Client) Display(ctx context.Context, target, format string) (string, error) {
	args := []string{"display-message", "-p"}
	if target != "" {
		args = append(args, "-t", target)
	}
	return c.Output(ctx, append(args, format)...)
}

// Option reads a user or tmux option. scope is one of "-g", "-s", "-w", "-p"
// (or "" for session scope with -t). ok is false when a user option (@name)
// is unset. Any other failure, a timeout included, is an error.
func (c *Client) Option(ctx context.Context, scope, target, name string) (value string, ok bool, err error) {
	args := []string{"show-options", "-v"}
	if scope != "" {
		args = append(args, scope)
	}
	if target != "" {
		args = append(args, "-t", target)
	}
	out, err := c.Run(ctx, append(args, name)...)
	if err != nil {
		if strings.HasPrefix(name, "@") && isUnsetOption(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSuffix(out, "\n"), true, nil
}

// isUnsetOption reports whether err from show-options means only that the
// option has no value: tmux ran, exited 1 and said so. A killed tmux or one
// that never started has no such message.
func isUnsetOption(err error) bool {
	te, ok := errors.AsType[*Error](err)
	if !ok {
		return false
	}
	exit, ok := errors.AsType[*exec.ExitError](te.Err)
	return ok && exit.ExitCode() == 1 && strings.HasPrefix(te.Stderr, "invalid option: ")
}

// argv is the tmux command line for args. An argument that is exactly ";"
// separates commands. tmux also takes a trailing ";" on any other argument as
// a separator and drops it, so a value such as a flag reason "x;" would lose
// its semicolon; argv escapes it, and tmux stores it as given.
func (c *Client) argv(args []string) []string {
	var argv []string
	if c.Socket != "" {
		argv = append(argv, "-S", c.Socket)
	}
	if c.Config != "" {
		argv = append(argv, "-f", c.Config)
	}
	for _, a := range args {
		if a != ";" && strings.HasSuffix(a, ";") {
			a = a[:len(a)-1] + `\;`
		}
		argv = append(argv, a)
	}
	return argv
}
