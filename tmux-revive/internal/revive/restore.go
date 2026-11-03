package revive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/influento/tmux-plugins/internal/lockfile"
	"github.com/influento/tmux-plugins/internal/shell"
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
		unlock, err := lockfile.Lock(ctx, r.lockPath("save"))
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
		// Typing may have used up ctx; the save after a restore still runs.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), afterRestore)
		defer cancel()
		if _, serr := r.Save(sctx, 0); serr != nil {
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
		fmt.Fprintf(&b, "not typed (the log says why): %s\n", strings.Join(rep.NotTyped, ", "))
	}
	return b.String()
}

// liveState is what the running server has, by session name and window
// and pane id.
type liveState struct {
	sessions map[string]map[int]bool // window indexes in use, per session
	ids      map[string]bool         // @revive_id of every window
	panes    map[string]bool         // @revive_pane of every pane
}

func (r *Reviver) readLive(ctx context.Context) (liveState, error) {
	l := liveState{sessions: map[string]map[int]bool{}, ids: map[string]bool{}, panes: map[string]bool{}}
	out, err := r.Tmux.Output(ctx, "list-panes", "-a", "-F",
		"#{session_name}"+sep+"#{window_index}"+sep+"#{"+OptWindowID+"}"+sep+"#{"+OptPaneID+"}")
	if err := noServer(err); errors.Is(err, errNoServer) {
		return l, nil
	} else if err != nil {
		return l, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(line, sep)
		if len(f) != 4 {
			continue
		}
		if l.sessions[f[0]] == nil {
			l.sessions[f[0]] = map[int]bool{}
		}
		l.sessions[f[0]][atoi(f[1])] = true
		if f[2] != "" {
			l.ids[f[2]] = true
		}
		if f[3] != "" {
			l.panes[f[3]] = true
		}
	}
	return l, nil
}

// restoreWindow is one window restore creates.
type restoreWindow struct {
	session string
	first   bool // the first window of a new session: new-session makes it
	w       snapshot.Window
	// panes are the tmux pane ids, one per saved pane in its order; "" for
	// a pane that could not be made. The window exists when panes[0] does.
	panes []string
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
			// A live pane may hold a saved pane's id: it moved to another
			// window since. The restored copy gets a new id at the next save,
			// and no scrollback, so that no two panes write one file.
			w.Panes = slices.Clone(w.Panes)
			for i := range w.Panes {
				if id := w.Panes[i].ID; live.panes[id] {
					w.Panes[i].ID = ""
				} else if id != "" {
					live.panes[id] = true
				}
			}
			wins = append(wins, &restoreWindow{session: sess.Name, first: first, w: w, panes: make([]string, len(w.Panes))})
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
	var errs []error

	// New sessions first: the first one starts the server when none runs,
	// and tmux.conf is loaded before the rest.
	var (
		cmds [][]string
		made []*restoreWindow
	)
	for _, rw := range wins {
		if !rw.first {
			continue
		}
		p := rw.w.Panes[0]
		cmd := []string{"new-session", "-d", "-s", escapeFormat(rw.session),
			"-x", strconv.Itoa(max(rw.w.Width, 10)), "-y", strconv.Itoa(max(rw.w.Height, 5))}
		cmd = append(cmd, r.paneArgs(rw.w, p, true)...)
		cmd = append(cmd, "-P", "-F", "#{pane_id} #{window_index}")
		cmds = append(cmds, appendCommand(cmd, r.paneCommand(p)))
		made = append(made, rw)
	}
	lines, err := r.create(ctx, cmds)
	if err != nil {
		errs = append(errs, fmt.Errorf("create sessions: %w", err))
	}
	for i, line := range lines {
		if line == "" {
			continue
		}
		rw := made[i]
		id, idx, _ := strings.Cut(line, " ")
		rw.panes[0] = id
		if live.sessions[rw.session] == nil {
			live.sessions[rw.session] = map[int]bool{}
		}
		live.sessions[rw.session][atoi(idx)] = true
	}

	// The other windows, each at its saved index when that is free, in the
	// sessions that exist.
	cmds, made = nil, nil
	for _, rw := range wins {
		used, ok := live.sessions[rw.session]
		if rw.first || !ok {
			continue
		}
		target := "=" + rw.session + ":"
		if !used[rw.w.Index] {
			target += strconv.Itoa(rw.w.Index)
			used[rw.w.Index] = true
		}
		p := rw.w.Panes[0]
		cmd := []string{"new-window", "-d", "-t", target}
		cmd = append(cmd, r.paneArgs(rw.w, p, true)...)
		cmd = append(cmd, "-P", "-F", "#{pane_id}")
		cmds = append(cmds, appendCommand(cmd, r.paneCommand(p)))
		made = append(made, rw)
	}
	lines, err = r.create(ctx, cmds)
	if err != nil {
		errs = append(errs, fmt.Errorf("create windows: %w", err))
	}
	for i, line := range lines {
		made[i].panes[0] = line
	}

	// The other panes. Each split goes right after the window's first
	// pane, so splitting in reverse order leaves the panes in saved order,
	// which is the order select-layout gives them their cells. A tiled
	// layout after each split keeps every pane big enough to split.
	type paneAt struct {
		rw *restoreWindow
		k  int
	}
	cmds = nil
	var where []paneAt
	for _, rw := range wins {
		if rw.panes[0] == "" {
			continue
		}
		for k := len(rw.w.Panes) - 1; k >= 1; k-- {
			p := rw.w.Panes[k]
			cmd := []string{"split-window", "-d", "-t", rw.panes[0]}
			cmd = append(cmd, r.paneArgs(rw.w, p, false)...)
			cmd = append(cmd, "-P", "-F", "#{pane_id}")
			cmd = appendCommand(cmd, r.paneCommand(p))
			cmds = append(cmds, append(cmd, ";", "select-layout", "-t", rw.panes[0], "tiled"))
			where = append(where, paneAt{rw, k})
		}
	}
	lines, err = r.create(ctx, cmds)
	if err != nil {
		errs = append(errs, fmt.Errorf("split panes: %w", err))
	}
	for i, line := range lines {
		where[i].rw.panes[where[i].k] = line
	}
	return r.finish(ctx, snap, wins, rep, errors.Join(errs...))
}

// create runs commands that each make one window or pane and print one
// line, and returns each command's line: "" for one that failed. tmux
// stops a command line at its first failing command, so after a failure
// the next command runs on its own, which tells whether it fails, and the
// rest go on in a new batch: one command tmux refuses costs only itself.
// A command may bring others after it (a layout); its line still counts.
func (r *Reviver) create(ctx context.Context, cmds [][]string) ([]string, error) {
	lines := make([]string, 0, len(cmds))
	var errs []error
	for len(lines) < len(cmds) {
		rest := cmds[len(lines):]
		var args []string
		for _, c := range rest {
			args = append(append(args, c...), ";")
		}
		out, err := r.Tmux.RunAll(ctx, args...)
		got := outLines(out)
		lines = append(lines, got[:min(len(got), len(rest))]...)
		if err == nil || len(lines) == len(cmds) {
			if err != nil {
				errs = append(errs, err)
			}
			break
		}
		if !refused(err) {
			// A timeout, or a server gone: tmux may still be running the
			// batch, so nothing in it runs again.
			errs = append(errs, err)
			break
		}
		out, err = r.Tmux.Run(ctx, cmds[len(lines)]...)
		line := ""
		if got := outLines(out); len(got) > 0 {
			line = got[0]
		}
		if err != nil {
			errs = append(errs, err)
			if !refused(err) {
				lines = append(lines, line)
				break
			}
		}
		lines = append(lines, line)
	}
	for len(lines) < len(cmds) {
		lines = append(lines, "")
	}
	return lines, errors.Join(errs...)
}

// refused reports whether tmux ran and refused a command; not a timeout,
// and not a server that is gone.
func refused(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(noServer(err), errNoServer) {
		return false
	}
	_, ok := errors.AsType[*exec.ExitError](err)
	return ok
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
		first := rw.panes[0]
		if first == "" {
			continue
		}
		made := 0
		for _, id := range rw.panes {
			if id != "" {
				made++
			}
		}
		rep.Windows++
		rep.Panes += made
		if made == len(rw.w.Panes) && rw.w.Layout != "" {
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
			if id == "" {
				continue
			}
			p := rw.w.Panes[i]
			if p.ID != "" {
				args = append(args, "set-option", "-p", "-t", id, OptPaneID, p.ID, ";")
			}
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
				text = shell.Join(p.Program)
			}
			if text != "" {
				label := fmt.Sprintf("%s:%d.%d", rw.session, rw.w.Index, p.Index)
				tasks = append(tasks, typeTask{pane: id, label: label, text: text})
			}
		}
		args = append(args, "select-pane", "-t", active, ";")
		if rw.w.Zoomed && made > 1 {
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
	shellPath, _ := r.option(ctx, "default-shell", "")
	rep.Typed, rep.NotTyped = r.typeAll(ctx, tasks, shellPath)
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
	return "exec " + shell.Quote(r.Self) + " paint " + shell.Quote(path)
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
