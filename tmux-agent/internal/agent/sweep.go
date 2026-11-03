package agent

import (
	"context"
	"slices"
	"strings"
)

// SweepTitles keeps window titles tied to running agents. A window that
// carries an agent's title but runs no agent gets its automatic name back:
// a pane killed with the agent inside says nothing on its way out. The
// other way round, an agent running under tmux's automatic name gets its
// session's recorded title back.
func (e *Env) SweepTitles(ctx context.Context) error {
	format := strings.Join([]string{
		"#{window_id}", "#{window_name}", "#{" + OptTitle + "}", "#{automatic-rename}",
		"#{pane_id}", "#{pane_current_command}", "#{" + optAgent + "}", "#{" + optSession + "}",
	}, "\x1f")
	out, err := e.Tmux.Output(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return err
	}
	type pane struct{ id, agent, session string }
	type window struct {
		id, name, title string
		auto            bool
		agents          []pane // panes running an agent
	}
	var order []string
	windows := map[string]*window{}
	commands := e.Registry.CommandNames()
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(l, "\x1f", 8)
		if len(f) < 8 {
			continue
		}
		w, ok := windows[f[0]]
		if !ok {
			w = &window{id: f[0], name: f[1], title: f[2], auto: f[3] == "1"}
			windows[f[0]] = w
			order = append(order, f[0])
		}
		cmd, launched := f[5], f[6]
		if cmd != "" && (slices.Contains(commands, cmd) || (launched != "" && cmd == CommandName(e.Registry.Get(launched)))) {
			w.agents = append(w.agents, pane{id: f[4], agent: launched, session: f[7]})
		}
	}
	for _, id := range order {
		w := windows[id]
		switch {
		case w.title != "" && len(w.agents) == 0:
			args := []string{"set-option", "-wu", "-t", w.id, OptTitle}
			if w.title == w.name {
				args = append(args, ";", "set-option", "-w", "-t", w.id, "automatic-rename", "on")
			}
			if _, err := e.Tmux.Run(ctx, args...); err != nil && !isGone(err) {
				return err
			}
		case w.auto && len(w.agents) > 0 && e.Store != nil:
			for _, p := range w.agents {
				title := e.recordedTitle(p.agent, p.session)
				if title == "" {
					continue
				}
				_, err := e.Tmux.Run(ctx, "rename-window", "-t", w.id, title, ";",
					"set-option", "-w", "-t", w.id, OptTitle, title, ";",
					"set-option", "-p", "-t", p.id, optNamed, title)
				if err != nil && !isGone(err) {
					return err
				}
				break
			}
		}
	}
	return nil
}

func (e *Env) recordedTitle(agent, session string) string {
	if agent == "" || session == "" {
		return ""
	}
	r, ok, err := e.Store.Load(agent, session)
	if err != nil || !ok {
		return ""
	}
	return r.Title
}
