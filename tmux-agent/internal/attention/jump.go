package attention

import (
	"context"
	"slices"
	"strings"
)

// KillSession kills the named session from the picker. The last session
// outside the skipped ones is refused: the client, and with it the terminal,
// would go too.
func (c *Core) KillSession(ctx context.Context, name string) error {
	skip, err := c.skipper(ctx)
	if err != nil {
		return err
	}
	out, err := c.Tmux.Output(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return err
	}
	left := 0
	for _, s := range lines(out) {
		if !skip(s) {
			left++
		}
	}
	if left <= 1 {
		_, err := c.Tmux.Run(ctx, "display-message", "tmux-agent: "+name+" is the last session")
		return err
	}
	_, err = c.Tmux.Run(ctx, "kill-session", "-t", "="+name)
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

// Borders turns pane borders on for a split window and off for a single
// pane, then renders the counts, since a pane that left took its flag. A
// window that is gone has no borders to set.
func (c *Core) Borders(ctx context.Context, window string) error {
	if window != "" {
		out, err := c.Tmux.Output(ctx, "list-panes", "-t", window, "-F", "#{pane_id}")
		switch {
		case err != nil && !isGone(err):
			return err
		case err == nil:
			status := "off"
			if len(lines(out)) > 1 {
				status = "top"
			}
			if _, err := c.Tmux.Run(ctx, "set-option", "-w", "-t", window, "pane-border-status", status); err != nil {
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
	name, err := c.Tmux.Output(ctx, "display-message", "-p", "-t", pane, "#{window_name}")
	if err != nil {
		return "", "", false, err
	}
	switch s {
	case NeedsYou:
		return name + " needs you", or(reason, "waiting for a decision"), true, nil
	case Done:
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
