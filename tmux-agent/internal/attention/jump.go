package attention

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/influento/tmux-plugins/tmux-agent/internal/sidebar"
)

// KillSession kills the named session from the picker. The last session
// outside the skipped ones is refused: the client, and with it the terminal,
// would go too. Clients on the session move to another one first, as tmux
// detaches them otherwise (detach-on-destroy).
func (c *Core) KillSession(ctx context.Context, name string) error {
	skip, err := c.skipper(ctx)
	if err != nil {
		return err
	}
	out, err := c.Tmux.Output(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return err
	}
	other := ""
	for _, s := range lines(out) {
		if s != name && !skip(s) && other == "" {
			other = s
		}
	}
	if other == "" {
		_, err := c.Tmux.Run(ctx, "display-message", "tmux-agent: "+name+" is the last session")
		return err
	}
	out, err = c.Tmux.Output(ctx, "list-clients", "-F", "#{client_name}"+sep+"#{client_session}")
	if err != nil {
		return err
	}
	for _, l := range lines(out) {
		f := split(l, 2)
		if f[1] != name {
			continue
		}
		if _, err := c.Tmux.Run(ctx, "switch-client", "-c", f[0], "-t", "="+other+":"); err != nil {
			return err
		}
	}
	_, err = c.Tmux.Run(ctx, "kill-session", "-t", "="+name)
	return err
}

// Kill kills what a picker row names: a session ("=name:"), a window
// ("=name:index") or a pane ("=name:index.pane"). Killing a window's last
// pane kills the window, and a session's last window the session, so those
// follow KillSession's rule.
func (c *Core) Kill(ctx context.Context, target string) error {
	session, rest, ok := strings.Cut(strings.TrimPrefix(target, "="), ":")
	if !ok || session == "" {
		return fmt.Errorf("not a picker target: %q", target)
	}
	if rest == "" {
		return c.KillSession(ctx, session)
	}
	out, err := c.Tmux.Output(ctx, "display-message", "-p", "-t", target, "#{session_windows}"+sep+"#{window_panes}")
	if err != nil {
		return err
	}
	f := split(out, 2)
	lastWindow, lastPane := f[0] == "1", f[1] == "1"
	isPane := strings.Contains(rest, ".")
	switch {
	case lastWindow && (lastPane || !isPane):
		return c.KillSession(ctx, session)
	case isPane && !lastPane:
		_, err = c.Tmux.Run(ctx, "kill-pane", "-t", target)
	default:
		_, err = c.Tmux.Run(ctx, "kill-window", "-t", target)
	}
	return err
}

// Jump goes to the next pane that needs the user, else the next unread done
// one. A second press walks on past the current pane.
func (c *Core) Jump(ctx context.Context) error {
	here, _ := c.Tmux.Output(ctx, "display-message", "-p", "#{pane_id}") // none outside a client
	rows, err := c.List(ctx)
	if err != nil {
		return err
	}
	var ids []string
	for _, r := range rows {
		if r.Rank <= Done.Rank() {
			ids = append(ids, r.Pane)
		}
	}
	if len(ids) == 0 {
		_, err := c.Tmux.Run(ctx, "display-message", "nothing needs you")
		return err
	}
	next := ids[0]
	if i := slices.Index(ids, here); i >= 0 {
		next = ids[(i+1)%len(ids)]
	}
	return c.GoTo(ctx, next)
}

// GoTo shows the pane in the current client and marks it seen.
func (c *Core) GoTo(ctx context.Context, pane string) error {
	if _, err := c.Tmux.Run(ctx, "switch-client", "-t", pane); err != nil {
		if _, err := c.Tmux.Run(ctx, "select-window", "-t", pane); err != nil {
			return err
		}
	}
	if _, err := c.Tmux.Run(ctx, "select-pane", "-t", pane); err != nil {
		return err
	}
	return c.Seen(ctx, pane)
}

// OptBorders, set to "off" in tmux.conf, keeps the label lines off: Borders
// then leaves each window to the global pane-border-status.
const OptBorders = "@attention_borders"

// Borders turns pane borders on for a split window and off for a single
// pane, then renders the counts, since a pane that left took its flag. A
// window that is gone has no borders to set.
func (c *Core) Borders(ctx context.Context, window string) error {
	if window != "" {
		// The panel's pane does not count: it splits no window of its own.
		out, err := c.Tmux.Output(ctx, "list-panes", "-t", window, "-F",
			"#{?#{"+sidebar.OptClient+"},0,1}#{"+OptBorders+"}")
		switch {
		case err != nil && !isGone(err):
			return err
		case err == nil:
			args := []string{"set-option", "-w", "-t", window, "pane-border-status", "off"}
			switch {
			case strings.HasSuffix(strings.TrimSpace(out), "off"):
				args = []string{"set-option", "-wu", "-t", window, "pane-border-status"}
			case strings.Count(out, "1") > 1:
				args[len(args)-1] = "top"
			}
			if _, err := c.Tmux.Run(ctx, args...); err != nil {
				return err
			}
		}
	}
	return c.Status(ctx)
}

// Visible reports whether the pane is on screen in a focused client: the
// active pane of its window, the active window of its session, and a client
// of that session has focus. tmux 3.8 shows focus in #{client_flags}; it has
// no client_focused format.
func (c *Core) Visible(ctx context.Context, pane string) (bool, error) {
	out, err := c.Tmux.Output(ctx, "display-message", "-p", "-t", pane, "#{window_active}#{pane_active}"+sep+"#{session_id}")
	if err != nil {
		return false, err
	}
	f := split(out, 2)
	if f[0] != "11" {
		return false, nil
	}
	out, err = c.Tmux.Output(ctx, "list-clients", "-t", f[1], "-F", "#{client_flags}")
	if err != nil {
		return false, err
	}
	for _, l := range lines(out) {
		if slices.Contains(strings.Split(l, ","), "focused") {
			return true, nil
		}
	}
	return false, nil
}

// Notification is what to tell the desktop about a flag, after the moment
// a notification waits: ok is false when the flag changed since, or the pane
// is on screen.
func (c *Core) Notification(ctx context.Context, pane string, s State) (title, body string, ok bool, err error) {
	cur, reason, err := c.FlagOf(ctx, pane)
	if err != nil || cur != s {
		return "", "", false, err
	}
	visible, err := c.Visible(ctx, pane)
	if err != nil || visible {
		return "", "", false, err
	}
	out, err := c.Tmux.Output(ctx, "display-message", "-p", "-t", pane, "#{window_name}"+sep+"#{@attention_failed}")
	if err != nil {
		return "", "", false, err
	}
	f := split(out, 2)
	name, failed := f[0], f[1] == "1"
	switch {
	case s == NeedsYou:
		return name + " needs you", or(reason, "waiting for a decision"), true, nil
	case s == Done && failed:
		return name + " failed", or(strings.TrimPrefix(reason, FailedPrefix), "no reason given"), true, nil
	case s == Done:
		return name + " is done", or(reason, "turn ended"), true, nil
	}
	return "", "", false, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
