// Package logind is tmux-revive's view of systemd-logind: a delay lock on
// shutdown, the signals that a shutdown starts and that a login session
// ended, and the user's state. It runs systemd's own tools
// (systemd-inhibit, busctl, loginctl), so tmux-revive needs no D-Bus
// library; `busctl wait` needs systemd 257.
package logind

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	service = "org.freedesktop.login1"
	manager = "/org/freedesktop/login1"
	iface   = "org.freedesktop.login1.Manager"
)

// UserClosing is the state of a user with no login session left whose
// processes still run: the user's service manager, and every program it
// runs (tmux among them), stops a few seconds later.
const UserClosing = "closing"

// Available reports whether systemd's tools are on PATH.
func Available() bool {
	for _, p := range []string{"systemd-inhibit", "busctl", "loginctl"} {
		if _, err := exec.LookPath(p); err != nil {
			return false
		}
	}
	return true
}

// Inhibit returns the command that runs argv while it holds a delay lock
// on shutdown: logind waits for argv to end (at most InhibitDelayMaxSec)
// before the machine goes down. It runs in a process group of its own,
// and the end of ctx sends the group SIGTERM: argv and what it started
// end with the lock.
func Inhibit(ctx context.Context, why string, argv ...string) *exec.Cmd {
	args := append([]string{"--what=shutdown", "--mode=delay", "--who=tmux-revive", "--why=" + why}, argv...)
	cmd := exec.CommandContext(ctx, "systemd-inhibit", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// WaitShutdown waits for logind's PrepareForShutdown signal and returns
// its value: true when a shutdown or reboot starts, false when one is
// cancelled.
func WaitShutdown(ctx context.Context) (bool, error) {
	out, err := busctl(ctx, "wait", service, manager, iface, "PrepareForShutdown")
	if err != nil {
		return false, err
	}
	return parseBool(out)
}

// PreparingForShutdown reports whether a shutdown has started: logind
// takes no new delay lock until it is cancelled.
func PreparingForShutdown(ctx context.Context) (bool, error) {
	out, err := busctl(ctx, "get-property", service, manager, iface, "PreparingForShutdown")
	if err != nil {
		return false, err
	}
	return parseBool(out)
}

// WaitSessionRemoved waits for logind's SessionRemoved signal: a login
// session of any user ended.
func WaitSessionRemoved(ctx context.Context) error {
	out, err := busctl(ctx, "wait", service, manager, iface, "SessionRemoved")
	if err != nil {
		return err
	}
	if out == "" {
		return errors.New("busctl wait SessionRemoved: no signal")
	}
	return nil
}

// UserState is logind's state of the user uid: offline, lingering,
// online, active or closing.
func UserState(ctx context.Context, uid int) (string, error) {
	out, err := exec.CommandContext(ctx, "loginctl", "show-user", strconv.Itoa(uid), "--property=State", "--value").Output()
	if err != nil {
		return "", fmt.Errorf("loginctl show-user: %w", commandError(err))
	}
	return strings.TrimSpace(string(out)), nil
}

func busctl(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "busctl", append([]string{"--system"}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("busctl %s %s: %w", args[0], args[len(args)-1], commandError(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// parseBool reads busctl's form of a boolean: "b true" or "b false".
func parseBool(out string) (bool, error) {
	switch out {
	case "b true":
		return true, nil
	case "b false":
		return false, nil
	}
	return false, fmt.Errorf("busctl: not a boolean: %q", out)
}

// commandError adds a failed command's stderr to its error.
func commandError(err error) error {
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}
