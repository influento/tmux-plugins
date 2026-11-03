package revive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/tmux-revive/internal/snapshot"
)

// Report is what a restore did.
type Report struct {
	From     snapshot.Entry
	Sessions []string // the sessions created
	Windows  int      // the windows created, in new sessions or not
	Panes    int
	Typed    int      // commands typed into restored shells
	NotTyped []string // where a command could not be typed: session:window.pane
	// Target is the session a client attached to last before the snapshot,
	// when the server has it; attach lands there.
	Target string
}

// Restore adds back what the running server lacks from snap: every
// session it does not have, and every window (by @revive_id) that is in
// none of its sessions. It starts the server when none runs. Windows the
// server has are left as they are.
//
// It then types each restored pane's command, @revive_cmd or a program,
// into the pane's shell, once the shell is ready, exactly once. Saves wait
// until it is done, so the first snapshot after it sees the programs it
// started.
func (r *Reviver) Restore(ctx context.Context, snap *snapshot.Snapshot, from snapshot.Entry) (Report, error) {
	rep, err := func() (Report, error) {
		unlock, err := lock(ctx, r.lockPath("save"))
		if err != nil {
			return Report{From: from}, err
		}
		defer unlock()
		rep, err := r.restoreLocked(ctx, snap)
		rep.From = from
		// What the server now holds is its state, even after a failure.
		if rerr := r.markReady(ctx); rerr != nil && !errors.Is(rerr, errNoServer) {
			err = errors.Join(err, rerr)
		}
		return rep, err
	}()
	r.logRestore(rep, err)
	if len(rep.Sessions) > 0 || rep.Windows > 0 {
		if _, serr := r.Save(ctx, 0); serr != nil {
			err = errors.Join(err, serr)
		}
	}
	return rep, err
}

func (r *Reviver) logRestore(rep Report, err error) {
	from := "-"
	if rep.From.Path != "" {
		from = rep.From.Time.Format(time.RFC3339)
	}
	msg := fmt.Sprintf("restore from=%s sessions=%d windows=%d panes=%d typed=%d not_typed=%d",
		from, len(rep.Sessions), rep.Windows, rep.Panes, rep.Typed, len(rep.NotTyped))
	if len(rep.NotTyped) > 0 {
		msg += " at=" + strings.Join(rep.NotTyped, ",")
	}
	if err != nil {
		msg += fmt.Sprintf(" error=%q", err.Error())
	}
	r.logf("%s", msg)
}

// Summary describes the report in a few lines.
func (rep Report) Summary() string {
	var b strings.Builder
	if len(rep.Sessions) == 0 && rep.Windows == 0 {
		b.WriteString("nothing to restore: the server has every saved window\n")
		return b.String()
	}
	when := ""
	if rep.From.Path != "" {
		when = " of " + rep.From.Time.Local().Format("2006-01-02 15:04:05")
	}
	fmt.Fprintf(&b, "restored the snapshot%s: %d windows, %d panes\n", when, rep.Windows, rep.Panes)
	if len(rep.Sessions) > 0 {
		fmt.Fprintf(&b, "new sessions: %s\n", strings.Join(rep.Sessions, ", "))
	}
	if rep.Typed > 0 {
		fmt.Fprintf(&b, "commands typed: %d\n", rep.Typed)
	}
	if len(rep.NotTyped) > 0 {
		fmt.Fprintf(&b, "not typed (no shell prompt): %s\n", strings.Join(rep.NotTyped, ", "))
	}
	return b.String()
}

// liveState is what the running server has, by session name and window id.
type liveState struct {
	running  bool
	sessions map[string]map[int]bool // window indexes in use, per session
	ids      map[string]bool         // @revive_id of every window
}

func (r *Reviver) readLive(ctx context.Context) (liveState, error) {
	l := liveState{sessions: map[string]map[int]bool{}, ids: map[string]bool{}}
	out, err := r.Tmux.Output(ctx, "list-windows", "-a", "-F",
		"#{session_name}"+sep+"#{window_index}"+sep+"#{"+OptWindowID+"}")
	if err := noServer(err); errors.Is(err, errNoServer) {
		return l, nil
	} else if err != nil {
		return l, err
	}
	l.running = true
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(line, sep)
		if len(f) != 3 {
			continue
		}
		if l.sessions[f[0]] == nil {
			l.sessions[f[0]] = map[int]bool{}
		}
		l.sessions[f[0]][atoi(f[1])] = true
		if f[2] != "" {
			l.ids[f[2]] = true
		}
	}
	return l, nil
}

// restoreWindow is one window restore creates.
type restoreWindow struct {
	session string
	first   bool // the first window of a new session: new-session makes it
	w       snapshot.Window
	panes   []string // tmux pane ids, in the saved order
}

// typeTask is a command to type into a restored pane.
type typeTask struct {
	pane, label, text string
}

func (r *Reviver) restoreLocked(ctx context.Context, snap *snapshot.Snapshot) (Report, error) {
	var rep Report
	live, err := r.readLive(ctx)
	if err != nil {
		return rep, err
	}
	var wins []*restoreWindow
	for _, sess := range snap.Sessions {
		_, exists := live.sessions[sess.Name]
		first := !exists
		for _, w := range sess.Windows {
			if len(w.Panes) == 0 || (w.ID != "" && live.ids[w.ID]) {
				continue
			}
			wins = append(wins, &restoreWindow{session: sess.Name, first: first, w: w})
			if first {
				rep.Sessions = append(rep.Sessions, sess.Name)
				first = false
			}
		}
	}
	if len(wins) == 0 {
		rep.Target = r.target(ctx, snap)
		return rep, nil
	}

	// New sessions first: the first one starts the server when none runs,
	// and tmux.conf is loaded before the rest.
	var args []string
	for _, rw := range wins {
		if !rw.first {
			continue
		}
		p := rw.w.Panes[0]
		args = append(args, "new-session", "-d", "-s", escapeFormat(rw.session),
			"-x", strconv.Itoa(max(rw.w.Width, 10)), "-y", strconv.Itoa(max(rw.w.Height, 5)))
		args = append(args, r.paneArgs(rw.w, p, true)...)
		args = append(args, "-P", "-F", "#{pane_id} #{window_index}")
		args = appendCommand(args, r.paneCommand(p))
		args = append(args, ";")
	}
	if len(args) > 0 {
		out, err := r.Tmux.RunAll(ctx, args...)
		lines := outLines(out)
		i := 0
		for _, rw := range wins {
			if !rw.first || i >= len(lines) {
				continue
			}
			id, idx, _ := strings.Cut(lines[i], " ")
			rw.panes = []string{id}
			if live.sessions[rw.session] == nil {
				live.sessions[rw.session] = map[int]bool{}
			}
			live.sessions[rw.session][atoi(idx)] = true
			i++
		}
		if err != nil {
			return r.finish(ctx, snap, wins, rep, fmt.Errorf("create sessions: %w", err))
		}
	}

	// The other windows, each at its saved index when that is free.
	args = nil
	for _, rw := range wins {
		if rw.first {
			continue
		}
		target := "=" + rw.session + ":"
		if used := live.sessions[rw.session]; !used[rw.w.Index] {
			target += strconv.Itoa(rw.w.Index)
			used[rw.w.Index] = true
		}
		p := rw.w.Panes[0]
		args = append(args, "new-window", "-d", "-t", target)
		args = append(args, r.paneArgs(rw.w, p, true)...)
		args = append(args, "-P", "-F", "#{pane_id}")
		args = appendCommand(args, r.paneCommand(p))
		args = append(args, ";")
	}
	if len(args) > 0 {
		out, err := r.Tmux.RunAll(ctx, args...)
		lines := outLines(out)
		i := 0
		for _, rw := range wins {
			if rw.first || i >= len(lines) {
				continue
			}
			rw.panes = []string{lines[i]}
			i++
		}
		if err != nil {
			return r.finish(ctx, snap, wins, rep, fmt.Errorf("create windows: %w", err))
		}
	}

	// The other panes. Each split goes right after the window's first
	// pane, so splitting in reverse order leaves the panes in saved order,
	// which is the order select-layout gives them their cells. A tiled
	// layout after each split keeps every pane big enough to split.
	args = nil
	for _, rw := range wins {
		if len(rw.panes) == 0 {
			continue
		}
		for k := len(rw.w.Panes) - 1; k >= 1; k-- {
			p := rw.w.Panes[k]
			args = append(args, "split-window", "-d", "-t", rw.panes[0])
			args = append(args, r.paneArgs(rw.w, p, false)...)
			args = append(args, "-P", "-F", "#{pane_id}")
			args = appendCommand(args, r.paneCommand(p))
			args = append(args, ";", "select-layout", "-t", rw.panes[0], "tiled", ";")
		}
	}
	if len(args) > 0 {
		out, err := r.Tmux.RunAll(ctx, args...)
		lines := outLines(out)
		i := 0
		for _, rw := range wins {
			if len(rw.panes) == 0 {
				continue
			}
			n := len(rw.w.Panes) - 1
			got := lines[min(i, len(lines)):min(i+n, len(lines))]
			i += n
			// got holds panes N-1 … 1; the saved order is 1 … N-1.
			rest := slices.Clone(got)
			slices.Reverse(rest)
			if len(rest) == n {
				rw.panes = append(rw.panes, rest...)
			}
		}
		if err != nil {
			return r.finish(ctx, snap, wins, rep, fmt.Errorf("split panes: %w", err))
		}
	}
	return r.finish(ctx, snap, wins, rep, nil)
}

// finish lays out, marks and selects what was created, types the
// commands, and fills in the report. createErr is a failure while
// creating, which still leaves what exists to finish.
func (r *Reviver) finish(ctx context.Context, snap *snapshot.Snapshot, wins []*restoreWindow, rep Report, createErr error) (Report, error) {
	var args, zoom []string
	var tasks []typeTask
	newSession := map[string]bool{}
	for _, name := range rep.Sessions {
		newSession[name] = true
	}
	for _, rw := range wins {
		if len(rw.panes) == 0 {
			continue
		}
		rep.Windows++
		rep.Panes += len(rw.panes)
		first := rw.panes[0]
		if len(rw.panes) == len(rw.w.Panes) && rw.w.Layout != "" {
			args = append(args, "select-layout", "-t", first, rw.w.Layout, ";")
		}
		auto := "off"
		if rw.w.AutoRename {
			auto = "on"
		}
		args = append(args,
			"set-option", "-w", "-t", first, OptWindowID, rw.w.ID, ";",
			"set-option", "-w", "-t", first, "automatic-rename", auto, ";")
		active := first
		for i, id := range rw.panes {
			p := rw.w.Panes[i]
			args = append(args, "set-option", "-p", "-t", id, OptPaneID, p.ID, ";")
			if p.Command != "" {
				// Kept until the command runs and its program replaces it, so
				// a restore cut short tries again next time.
				args = append(args, "set-option", "-p", "-t", id, OptCommand, p.Command, ";")
			}
			if p.Active {
				active = id
			}
			text := p.Command
			if text == "" && len(p.Program) > 0 {
				text = shellJoin(p.Program)
			}
			if text != "" {
				label := fmt.Sprintf("%s:%d.%d", rw.session, rw.w.Index, p.Index)
				tasks = append(tasks, typeTask{pane: id, label: label, text: text})
			}
		}
		args = append(args, "select-pane", "-t", active, ";")
		if rw.w.Zoomed && len(rw.panes) > 1 {
			zoom = append(zoom, "resize-pane", "-Z", "-t", active, ";")
		}
		if rw.w.Active && newSession[rw.session] {
			args = append(args, "select-window", "-t", first, ";")
		}
	}
	errs := []error{createErr}
	errs = append(errs, r.runEach(ctx, args))
	if len(zoom) > 0 {
		if _, err := r.Tmux.RunAll(ctx, zoom...); err != nil {
			errs = append(errs, fmt.Errorf("zoom: %w", err))
		}
	}
	shell, _ := r.option(ctx, "default-shell", "")
	rep.Typed, rep.NotTyped = r.typeAll(ctx, tasks, shell)
	rep.Target = r.target(ctx, snap)
	return rep, errors.Join(errs...)
}

// target is the session a client used last in snap, when the server has
// it and attach may land there.
func (r *Reviver) target(ctx context.Context, snap *snapshot.Snapshot) string {
	skip := r.skipper(ctx)
	name := snap.LastUsed(func(s string) bool { return !skip(s) })
	if name == "" {
		return ""
	}
	if _, err := r.Tmux.Run(ctx, "has-session", "-t", "="+name); err != nil {
		return ""
	}
	return name
}

// runEach runs commands that are safe to run twice, in batches; when a
// batch fails, it runs that batch's commands one by one, so that one bad
// command (a layout tmux refuses) costs only itself.
func (r *Reviver) runEach(ctx context.Context, args []string) error {
	var errs []error
	for _, batch := range tmux.Batches(args) {
		if _, err := r.Tmux.Run(ctx, batch...); err == nil {
			continue
		}
		for _, cmd := range tmux.Commands(batch) {
			if _, err := r.Tmux.Run(ctx, cmd...); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// paneArgs are a new window's or pane's name and folder. tmux expands
// formats in both, so a # in them is doubled.
func (r *Reviver) paneArgs(w snapshot.Window, p snapshot.Pane, window bool) []string {
	var args []string
	if window && !w.AutoRename && w.Name != "" {
		args = append(args, "-n", escapeFormat(w.Name))
	}
	if p.Cwd != "" {
		args = append(args, "-c", escapeFormat(p.Cwd))
	}
	return args
}

// paneCommand is what a restored pane runs instead of the default shell:
// tmux-revive paint, which prints the pane's saved scrollback and then
// becomes the shell. "" when the pane has no scrollback saved.
func (r *Reviver) paneCommand(p snapshot.Pane) string {
	if r.Self == "" || p.ID == "" {
		return ""
	}
	path, err := r.Store.ScrollbackPath(p.ID)
	if err != nil {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return "exec " + shellQuote(r.Self) + " paint " + shellQuote(path)
}

func appendCommand(args []string, cmd string) []string {
	if cmd == "" {
		return args
	}
	return append(args, cmd)
}

// escapeFormat doubles each #, which tmux would read as the start of a
// format.
func escapeFormat(s string) string { return strings.ReplaceAll(s, "#", "##") }

func outLines(out string) []string {
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}
