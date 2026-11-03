package revive

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/influento/tmux-plugins/tmux-revive/internal/snapshot"
)

// livePane is one line of list-panes -a: a pane with its window and
// session.
type livePane struct {
	session      string
	lastAttached int64
	forgotten    bool

	window     string // tmux's window id (@N), for this server only
	winIndex   int
	windowID   string // @revive_id
	windowName string
	autoRename bool
	layout     string
	width      int
	height     int
	winActive  bool
	zoomed     bool

	pane       string // tmux's pane id (%N), for this server only
	paneIndex  int
	paneID     string // @revive_pane
	cwd        string
	paneActive bool
	pid        int
	command    string // @revive_cmd

	ready bool // the server's @revive_ready
}

// paneFields is the list-panes format, one field per livePane field.
var paneFields = []string{
	"#{session_name}", "#{session_last_attached}", "#{" + OptForgotten + "}",
	"#{window_id}", "#{window_index}", "#{" + OptWindowID + "}", "#{window_name}", "#{automatic-rename}",
	"#{window_layout}", "#{window_width}", "#{window_height}", "#{window_active}", "#{window_zoomed_flag}",
	"#{pane_id}", "#{pane_index}", "#{" + OptPaneID + "}", "#{pane_current_path}", "#{pane_active}", "#{pane_pid}",
	"#{" + OptCommand + "}", "#{" + OptReady + "}",
}

// sep separates fields in a format; end ends a record, so that a newline
// inside a name or a command does not split it.
const (
	sep = "\x1f"
	end = "\x1e"
)

// readPanes reads every pane of the server in one call.
func (r *Reviver) readPanes(ctx context.Context) ([]livePane, error) {
	out, err := r.Tmux.Run(ctx, "list-panes", "-a", "-F", strings.Join(paneFields, sep)+end)
	if err != nil {
		return nil, noServer(err)
	}
	var panes []livePane
	for rec := range strings.SplitSeq(out, end+"\n") {
		if rec == "" {
			continue
		}
		f := strings.Split(rec, sep)
		if len(f) != len(paneFields) {
			// The separator inside a name or a command: the record cannot
			// be read, so the snapshot would be wrong.
			return nil, fmt.Errorf("list-panes: a record has %d fields, want %d: %q", len(f), len(paneFields), rec)
		}
		panes = append(panes, livePane{
			session: f[0], lastAttached: atoi64(f[1]), forgotten: f[2] == "1",
			window: f[3], winIndex: atoi(f[4]), windowID: f[5], windowName: f[6], autoRename: isOn(f[7]),
			layout: f[8], width: atoi(f[9]), height: atoi(f[10]), winActive: f[11] == "1", zoomed: f[12] == "1",
			pane: f[13], paneIndex: atoi(f[14]), paneID: f[15], cwd: f[16], paneActive: f[17] == "1", pid: atoi(f[18]),
			command: f[19], ready: f[20] == "1",
		})
	}
	return panes, nil
}

func isOn(s string) bool { return s == "1" || s == "on" }

func atoi(s string) int {
	n, _ := strconv.Atoi(s) // tmux prints numbers; a blank field is 0
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64) // as atoi
	return n
}

// assignIDs gives every window and pane without a revive id one, on the
// server and in panes.
func (r *Reviver) assignIDs(ctx context.Context, panes []livePane) error {
	winIDs := map[string]string{}
	for _, p := range panes {
		if p.windowID != "" {
			winIDs[p.window] = p.windowID
		}
	}
	var args []string
	for i := range panes {
		p := &panes[i]
		if p.windowID == "" {
			id, ok := winIDs[p.window]
			if !ok {
				var err error
				if id, err = newID(); err != nil {
					return err
				}
				winIDs[p.window] = id
				args = append(args, "set-option", "-w", "-t", p.window, OptWindowID, id, ";")
			}
			p.windowID = id
		}
		if p.paneID == "" {
			id, err := newID()
			if err != nil {
				return err
			}
			p.paneID = id
			args = append(args, "set-option", "-p", "-t", p.pane, OptPaneID, id, ";")
		}
	}
	if len(args) == 0 {
		return nil
	}
	_, err := r.Tmux.RunAll(ctx, args...)
	return noServer(err)
}

// programs is the set of programs restore starts again.
func (r *Reviver) programs(ctx context.Context) (map[string]bool, error) {
	v, err := r.option(ctx, OptPrograms, DefaultPrograms)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range strings.Fields(v) {
		set[p] = true
	}
	return set, nil
}

// build turns the live panes into a snapshot. Forgotten sessions are left
// out. A window linked into several sessions is saved once, with the first.
func build(panes []livePane, programs map[string]bool) *snapshot.Snapshot {
	snap := &snapshot.Snapshot{Version: snapshot.Version}
	sessionAt := map[string]int{}
	windowAt := map[string][2]int{} // tmux window id → session, window position
	for _, p := range panes {
		if p.forgotten {
			continue
		}
		si, ok := sessionAt[p.session]
		if !ok {
			si = len(snap.Sessions)
			sessionAt[p.session] = si
			snap.Sessions = append(snap.Sessions, snapshot.Session{Name: p.session, LastAttached: p.lastAttached})
		}
		pos, ok := windowAt[p.window]
		if ok && pos[0] != si {
			continue // linked: saved with its first session
		}
		sess := &snap.Sessions[si]
		if !ok {
			pos = [2]int{si, len(sess.Windows)}
			windowAt[p.window] = pos
			w := snapshot.Window{
				Index: p.winIndex, ID: p.windowID, AutoRename: p.autoRename, Layout: p.layout,
				Width: p.width, Height: p.height, Active: p.winActive, Zoomed: p.zoomed,
			}
			if !p.autoRename {
				w.Name = p.windowName
			}
			sess.Windows = append(sess.Windows, w)
		}
		w := &sess.Windows[pos[1]]
		w.Panes = append(w.Panes, snapshot.Pane{
			Index: p.paneIndex, ID: p.paneID, Cwd: p.cwd, Active: p.paneActive,
			Program: program(p.pid, programs), Command: p.command,
		})
	}
	return snap
}
