// Package sidebar keeps the panel (prefix f) as a tmux pane at the left
// edge, full height, that follows its client to every window and session.
//
// One pane per client: the pane option @sidebar_client names the client.
// The pane moves with join-pane into each window the client goes to. Each
// window keeps the layout of its main area (its panes without the panel)
// in @sidebar_main, and the panel's arrival, its departure and every
// resize lay out the whole window again from it: tmux's own resize hands
// the space out unevenly. Moves and layouts go in one tmux command list,
// so tmux draws once.
package sidebar

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/influento/tmux-plugins/internal/layout"
	"github.com/influento/tmux-plugins/internal/lockfile"
)

// Options the sidebar keeps.
const (
	OptClient = "@sidebar_client" // pane: the client whose panel it is
	OptMain   = "@sidebar_main"   // window: its main area's layout
	OptSize   = "@sidebar_size"   // window: "W H", the size the window was laid out for
	OptMark   = "@sidebar_mark"   // window: its panes' places as the sidebar left them
	OptLast   = "@sidebar_last"   // window: the main pane that had the focus last
	OptOpen   = "@sidebar_open"   // global: 1 while a panel is open; the hooks run only then
	OptStyle  = "@sidebar_style"  // global, from tmux.conf: the panel's window-style
	// OptReviveSkip keeps the panel out of tmux-revive's saves.
	OptReviveSkip = "@revive_skip"
)

// DefaultWidth is the panel's width in columns.
const DefaultWidth = 40

// RecordLast is the tmux command an after-select-pane hook runs: while a
// panel is open, it records a selected pane that is not a panel as its
// window's OptLast. tmux runs it without a process.
const RecordLast = `if-shell -F "#{&&:#{` + OptOpen + `},#{?#{` + OptClient + `},0,1}}" ` +
	`"set-option -wF ` + OptLast + ` \"#{pane_id}\""`

// ReloadKey is the key Reload sends a panel: its list reads the windows
// again.
const ReloadKey = "F12"

// Tmux is what the sidebar needs of a tmux client.
type Tmux interface {
	Run(ctx context.Context, args ...string) (string, error)
	Output(ctx context.Context, args ...string) (string, error)
}

// Sidebar opens, closes and moves the panels of one tmux server.
type Sidebar struct {
	Tmux Tmux
	// Width is the panel's width; DefaultWidth when 0.
	Width int
	// LockPath is the file whose lock each change holds; none when "".
	LockPath string
	// Command is the shell command the panel runs for client, opened from
	// the pane origin.
	Command func(client, origin string) string
	// Skip reports a session the panel never goes to; none when nil.
	Skip func(session string) bool
}

const sep = "\x1f"

// panel is one open panel.
type panel struct {
	pane, client, window, session string
	active                        bool
}

// client is one tmux client.
type client struct {
	name, window, session string
	pane                  string // the active pane of the window it shows
	index                 string // its window's index
	width, height         int
}

// window is what the sidebar reads of a window.
type window struct {
	id, session   string
	active, last  string // its active pane; OptLast
	layout        string
	width, height int
	zoomed        bool
	main, size    string
	mark          string
}

func (s *Sidebar) width() int {
	if s.Width > 0 {
		return s.Width
	}
	return DefaultWidth
}

// widthIn is the panel's width in a window w columns wide: at most half.
func (s *Sidebar) widthIn(w int) int {
	return max(1, min(s.width(), (w-1)/2))
}

func (s *Sidebar) lock(ctx context.Context) (func(), error) {
	if s.LockPath == "" {
		return func() {}, nil
	}
	return lockfile.Lock(ctx, s.LockPath)
}

func (s *Sidebar) skip(session string) bool { return s.Skip != nil && s.Skip(session) }

func split(line string, n int) []string {
	f := strings.SplitN(line, sep, n)
	for len(f) < n {
		f = append(f, "")
	}
	return f
}

func lines(out string) []string {
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (s *Sidebar) panels(ctx context.Context) ([]panel, error) {
	out, err := s.Tmux.Output(ctx, "list-panes", "-a", "-F",
		strings.Join([]string{"#{pane_id}", "#{" + OptClient + "}", "#{window_id}", "#{session_name}", "#{pane_active}"}, sep))
	if err != nil {
		return nil, err
	}
	var ps []panel
	seen := map[string]bool{}
	for _, l := range lines(out) {
		f := split(l, 5)
		if f[1] == "" || seen[f[0]] { // a linked window lists its panes again
			continue
		}
		seen[f[0]] = true
		ps = append(ps, panel{pane: f[0], client: f[1], window: f[2], session: f[3], active: f[4] == "1"})
	}
	return ps, nil
}

func (s *Sidebar) clients(ctx context.Context) (map[string]client, error) {
	out, err := s.Tmux.Output(ctx, "list-clients", "-F",
		strings.Join([]string{"#{client_name}", "#{window_id}", "#{session_name}", "#{client_width}", "#{client_height}",
			"#{pane_id}", "#{window_index}"}, sep))
	if err != nil {
		return nil, err
	}
	cs := map[string]client{}
	for _, l := range lines(out) {
		f := split(l, 7)
		w, _ := strconv.Atoi(f[3])
		h, _ := strconv.Atoi(f[4])
		cs[f[0]] = client{name: f[0], window: f[1], session: f[2], width: w, height: h, pane: f[5], index: f[6]}
	}
	return cs, nil
}

func (s *Sidebar) window(ctx context.Context, target string) (window, error) {
	out, err := s.Tmux.Output(ctx, "display-message", "-p", "-t", target, strings.Join([]string{
		"#{window_id}", "#{session_name}", "#{window_layout}", "#{window_width}", "#{window_height}",
		"#{window_zoomed_flag}", "#{" + OptMain + "}", "#{" + OptSize + "}", "#{" + OptMark + "}",
		"#{pane_id}", "#{" + OptLast + "}",
	}, sep))
	if err != nil {
		return window{}, err
	}
	f := split(out, 11)
	w, _ := strconv.Atoi(f[3])
	h, _ := strconv.Atoi(f[4])
	return window{id: f[0], session: f[1], layout: f[2], width: w, height: h, zoomed: f[5] == "1",
		main: f[6], size: f[7], mark: f[8], active: f[9], last: f[10]}, nil
}

// mark is where the window's panes are now, to tell the sidebar's own
// layout from a change.
func (s *Sidebar) mark(ctx context.Context, w string) (string, error) {
	out, err := s.Tmux.Output(ctx, "list-panes", "-t", w, "-F", "#{pane_id}:#{pane_left},#{pane_top},#{pane_width},#{pane_height}")
	return strings.ReplaceAll(out, "\n", " "), err
}

func (s *Sidebar) setMark(ctx context.Context, w string) error {
	m, err := s.mark(ctx, w)
	if err != nil {
		return err
	}
	_, err = s.Tmux.Run(ctx, "set-option", "-w", "-t", w, OptMark, m)
	return err
}

func (s *Sidebar) firstPane(ctx context.Context, w string) (string, error) {
	out, err := s.Tmux.Output(ctx, "list-panes", "-t", w, "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(out, "\n")
	return first, nil
}

// statusLines is how many rows the status line takes from a client.
func (s *Sidebar) statusLines(ctx context.Context) int {
	v, err := s.Tmux.Output(ctx, "show-options", "-gv", "status")
	if err != nil {
		return 1
	}
	switch v {
	case "off":
		return 0
	case "on":
		return 1
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return 1
}

// mainNow is the window's layout without the panel ("" for none): its
// main area as it is. ok is false when the panel is all there is.
func mainNow(w window, pane string) (layout.Layout, bool) {
	l, err := layout.Parse(w.layout)
	if err != nil {
		return layout.Layout{}, false
	}
	if pane == "" {
		return l, true
	}
	return l.Without(pane)
}

// mainSaved is the main area to lay the window out from: the saved one
// while it has the same panes, else the one there now (a pane was split
// or closed).
func mainSaved(w window, pane string) (layout.Layout, bool) {
	now, ok := mainNow(w, pane)
	if !ok {
		return now, false
	}
	saved, err := layout.Parse(w.main)
	if err != nil || strings.Join(saved.Panes(), " ") != strings.Join(now.Panes(), " ") {
		return now, true
	}
	return saved, true
}

// craft is the whole window: the panel at the left, full height, active
// when focus is true, and the main area scaled into the rest.
func craft(main layout.Layout, w, h int, pane string, width int, focus bool) layout.Layout {
	zero := 0
	left := layout.Cell{Type: layout.Pane, W: width, H: h, Index: &zero, ID: pane, Active: focus}
	if focus {
		main = main.Inactive()
	}
	right := layout.ScaleCell(main.Root, width+1, 0, w-width-1, h)
	root := layout.Cell{Type: layout.Horizontal, W: w, H: h, Cells: []layout.Cell{left, right}}
	if right.Type == layout.Horizontal {
		// One row of panes joins the root's children, as tmux lays it out.
		root.Cells = append([]layout.Cell{left}, right.Cells...)
	}
	return layout.Layout{Version: main.Version, Root: root}.Numbered()
}

func size(w, h int) string { return strconv.Itoa(w) + " " + strconv.Itoa(h) }

// Toggle opens the client's panel, or closes it when it is open.
func (s *Sidebar) Toggle(ctx context.Context, clientName string) error {
	if clientName == "" {
		return errors.New("sidebar: no client")
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	ps, err := s.panels(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.client == clientName {
			return s.close(ctx, p, len(ps) == 1, "")
		}
	}
	cs, err := s.clients(ctx)
	if err != nil {
		return err
	}
	c, ok := cs[clientName]
	if !ok {
		return fmt.Errorf("sidebar: no client %q", clientName)
	}
	return s.open(ctx, clientName, c.pane)
}

func (s *Sidebar) open(ctx context.Context, clientName, origin string) error {
	if s.Command == nil {
		return errors.New("sidebar: no panel command")
	}
	w, err := s.window(ctx, origin)
	if err != nil {
		return err
	}
	main, ok := mainNow(w, "")
	if !ok {
		return fmt.Errorf("sidebar: cannot read the layout of %s", w.id)
	}
	first, err := s.firstPane(ctx, w.id)
	if err != nil {
		return err
	}
	width := s.widthIn(w.width)
	pane, err := s.Tmux.Output(ctx, "split-window", "-h", "-b", "-f", "-l", strconv.Itoa(width),
		"-t", first, "-P", "-F", "#{pane_id}", s.Command(clientName, origin))
	if err != nil {
		return err
	}
	args := []string{
		"set-option", "-p", "-t", pane, OptClient, clientName, ";",
		"set-option", "-p", "-t", pane, OptReviveSkip, "1", ";",
		"select-pane", "-t", pane, "-T", "panel", ";",
		"set-option", "-w", "-t", w.id, OptMain, main.String(), ";",
		"set-option", "-w", "-t", w.id, OptSize, size(w.width, w.height), ";",
		"set-option", "-w", "-t", w.id, OptLast, origin, ";",
		"set-option", "-g", OptOpen, "1", ";",
	}
	if style, err := s.Tmux.Output(ctx, "show-options", "-gqv", OptStyle); err == nil && style != "" {
		args = append(args,
			"set-option", "-p", "-t", pane, "window-style", style, ";",
			"set-option", "-p", "-t", pane, "window-active-style", style, ";")
	}
	args = append(args, "select-layout", "-t", w.id, craft(main, w.width, w.height, pane, width, true).String())
	if _, err := s.Tmux.Run(ctx, args...); err != nil {
		return err
	}
	return s.setMark(ctx, w.id)
}

// Close closes the client's panel, if it has one.
func (s *Sidebar) Close(ctx context.Context, clientName string) error {
	return s.closeWhere(ctx, func(p panel) bool { return p.client == clientName }, "")
}

// ClosePane closes the panel in pane, and gives focus the focus when it is
// not "". The panel's own process ends that way, so it must be the last
// thing that process asks of tmux.
func (s *Sidebar) ClosePane(ctx context.Context, pane, focus string) error {
	return s.closeWhere(ctx, func(p panel) bool { return p.pane == pane }, focus)
}

// Landing is the pane that gets the focus when the panel in pane closes:
// the active pane of its window's main area, else its first pane.
func (s *Sidebar) Landing(ctx context.Context, pane string) (string, error) {
	w, err := s.window(ctx, pane)
	if err != nil {
		return "", err
	}
	main, ok := mainSaved(w, pane)
	if !ok {
		return "", nil
	}
	if id := main.ActivePane(); id != "" {
		return id, nil
	}
	return main.Panes()[0], nil
}

func (s *Sidebar) closeWhere(ctx context.Context, which func(panel) bool, focus string) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	ps, err := s.panels(ctx)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if which(p) {
			return s.close(ctx, p, len(ps) == 1, focus)
		}
	}
	return nil
}

// close kills the panel and lays its window out from the main area, then
// gives focus the focus, unless it is "". The hooks stop with the last
// panel.
func (s *Sidebar) close(ctx context.Context, p panel, last bool, focus string) error {
	w, err := s.window(ctx, p.window)
	if err != nil {
		return err
	}
	var args []string
	if last {
		args = append(args, "set-option", "-gu", OptOpen, ";")
	}
	args = append(args, forget(w.id)...)
	args = append(args, "kill-pane", "-t", p.pane)
	if main, ok := mainSaved(w, p.pane); ok {
		args = append(args, ";", "select-layout", "-t", w.id, main.Scale(w.width, w.height).String())
	}
	if focus != "" {
		args = append(args, ";", "select-pane", "-t", focus)
	}
	_, err = s.Tmux.Run(ctx, args...)
	return err
}

// forget is the commands that drop the sidebar's options of a window.
func forget(w string) []string {
	var args []string
	for _, o := range []string{OptMain, OptSize, OptMark, OptLast} {
		args = append(args, "set-option", "-wu", "-t", w, o, ";")
	}
	return args
}

// Go shows target (a window or a pane) on the client. The client's panel
// moves there first, in the same command list, so tmux draws once. With
// a panel the client goes to target's window and the focus stays where
// it was; without one, to target itself.
func (s *Sidebar) Go(ctx context.Context, clientName, target string) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	ps, err := s.panels(ctx)
	if err != nil {
		return err
	}
	cs, err := s.clients(ctx)
	if err != nil {
		return err
	}
	c, ok := cs[clientName]
	for _, p := range ps {
		if ok && p.client == clientName {
			return s.move(ctx, c, p, target)
		}
	}
	args := []string{"switch-client"}
	if clientName != "" {
		args = append(args, "-c", clientName)
	}
	_, err = s.Tmux.Run(ctx, append(args, "-t", target)...)
	return err
}

// move joins the panel into target's window and switches the client
// there. The window it leaves is laid out from its main area; the window
// it joins, for the client's size, which the window takes as the client
// switches to it.
func (s *Sidebar) move(ctx context.Context, c client, p panel, target string) error {
	to, err := s.window(ctx, target)
	if err != nil {
		return err
	}
	if to.id == p.window {
		_, err := s.Tmux.Run(ctx, "switch-client", "-c", c.name, "-t", to.id)
		return err
	}
	from, err := s.window(ctx, p.window)
	if err != nil {
		return err
	}
	toMain, ok := mainNow(to, "")
	if !ok {
		return fmt.Errorf("sidebar: cannot read the layout of %s", to.id)
	}
	first, err := s.firstPane(ctx, to.id)
	if err != nil {
		return err
	}
	w, h := c.width, c.height-s.statusLines(ctx)
	if w < 3 || h < 1 { // no size known: the window's own
		w, h = to.width, to.height
	}
	width := s.widthIn(w)
	args := []string{"join-pane"}
	if !p.active {
		args = append(args, "-d")
	}
	args = append(args, "-h", "-b", "-f", "-l", strconv.Itoa(width), "-s", p.pane, "-t", first, ";")
	if fromMain, ok := mainSaved(from, p.pane); ok {
		args = append(args, forget(from.id)...)
		args = append(args, "select-layout", "-t", from.id, fromMain.Scale(from.width, from.height).String(), ";")
	}
	args = append(args,
		"switch-client", "-c", c.name, "-t", to.id, ";",
		"set-option", "-w", "-t", to.id, OptMain, toMain.String(), ";",
		"set-option", "-w", "-t", to.id, OptSize, size(w, h), ";",
		"set-option", "-w", "-t", to.id, OptLast, to.active, ";",
		"select-layout", "-t", to.id, craft(toMain, w, h, p.pane, width, p.active).String())
	if _, err := s.Tmux.Run(ctx, args...); err != nil {
		return err
	}
	return s.setMark(ctx, to.id)
}

// Step goes to the next (+) or previous (-) window of the client's
// session, or to the one with index n, panel and all.
func (s *Sidebar) Step(ctx context.Context, clientName, spec string) error {
	cs, err := s.clients(ctx)
	if err != nil {
		return err
	}
	c, ok := cs[clientName]
	if !ok {
		return fmt.Errorf("sidebar: no client %q", clientName)
	}
	out, err := s.Tmux.Output(ctx, "list-windows", "-t", c.window, "-F", "#{window_id}"+sep+"#{window_index}")
	if err != nil {
		return err
	}
	var ids, indexes []string
	at := 0
	for i, l := range lines(out) {
		f := split(l, 2)
		ids, indexes = append(ids, f[0]), append(indexes, f[1])
		if f[0] == c.window {
			at = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	target := ""
	switch spec {
	case "+":
		target = ids[(at+1)%len(ids)]
	case "-":
		target = ids[(at+len(ids)-1)%len(ids)]
	default:
		for i, n := range indexes {
			if n == spec {
				target = ids[i]
			}
		}
	}
	if target == "" {
		_, err := s.Tmux.Run(ctx, "display-message", "-c", clientName, "no window "+spec)
		return err
	}
	return s.Go(ctx, clientName, target)
}

// Preset applies a preset layout (a name, next or previous) to the main
// panes of the client's window: the panel steps out of the window, the
// preset applies to the rest, and the panel joins again at the left, in
// one command list.
func (s *Sidebar) Preset(ctx context.Context, clientName, what string) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	cs, err := s.clients(ctx)
	if err != nil {
		return err
	}
	c, ok := cs[clientName]
	if !ok {
		return fmt.Errorf("sidebar: no client %q", clientName)
	}
	var step []string
	switch what {
	case "next":
		step = []string{"next-layout", "-t", c.window}
	case "previous":
		step = []string{"previous-layout", "-t", c.window}
	default:
		step = []string{"select-layout", "-t", c.window, what}
	}
	ps, err := s.panels(ctx)
	if err != nil {
		return err
	}
	var p *panel
	for i := range ps {
		if ps[i].client == clientName && ps[i].window == c.window {
			p = &ps[i]
		}
	}
	if p == nil {
		_, err := s.Tmux.Run(ctx, step...)
		return err
	}
	out, err := s.Tmux.Output(ctx, "list-panes", "-t", c.window, "-F", "#{pane_id}")
	if err != nil {
		return err
	}
	main1 := ""
	for _, id := range lines(out) {
		if id != p.pane {
			main1 = id
			break
		}
	}
	if main1 == "" {
		return nil
	}
	w, err := s.window(ctx, c.window)
	if err != nil {
		return err
	}
	join := []string{"join-pane"}
	if !p.active {
		join = append(join, "-d")
	}
	join = append(join, "-h", "-b", "-f", "-l", strconv.Itoa(s.widthIn(w.width)), "-s", p.pane, "-t", main1)
	args := append([]string{"break-pane", "-d", "-s", p.pane, ";"}, step...)
	args = append(append(args, ";"), join...)
	_, err = s.Tmux.Run(ctx, args...)
	return err
}

// Follow moves each panel to its client's window, after a switch that
// did not go through Go (a click, choose-tree, a key of tmux's own). A
// panel whose client is gone closes.
func (s *Sidebar) Follow(ctx context.Context) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	ps, err := s.panels(ctx)
	if err != nil {
		return err
	}
	cs, err := s.clients(ctx)
	if err != nil {
		return err
	}
	var errs []error
	left := len(ps)
	for _, p := range ps {
		c, ok := cs[p.client]
		switch {
		case !ok:
			errs = append(errs, s.close(ctx, p, left == 1, ""))
			left--
		case c.window != p.window:
			errs = append(errs, s.move(ctx, c, p, c.window))
		}
	}
	return errors.Join(errs...)
}

// Keep puts each panel back at the left, full height, at its width,
// after a layout change or a resize:
//
//   - the window's size changed: laid out again from the saved main area,
//     whose proportions tmux's own resize does not keep;
//   - the panel is in place at its width: the main area changed on
//     purpose (a split, a resize of a main pane), and is saved;
//   - in place at another width: back to its width, from the saved main
//     area;
//   - out of place (a preset, a rotation, a swap): the main area is what
//     is there without the panel.
//
// A window that looks as the sidebar left it is not touched, nor one
// zoomed. A panel left alone in its window (its last other pane closed)
// moves to another window, and the client with it.
func (s *Sidebar) Keep(ctx context.Context) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	ps, err := s.panels(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range ps {
		errs = append(errs, s.keep(ctx, p, len(ps)))
	}
	return errors.Join(errs...)
}

func (s *Sidebar) keep(ctx context.Context, p panel, open int) error {
	w, err := s.window(ctx, p.window)
	if err != nil {
		return err
	}
	if w.zoomed {
		return nil
	}
	if _, ok := mainNow(w, p.pane); !ok {
		return s.alone(ctx, p, open)
	}
	adopted, err := s.adopt(ctx, w, p)
	if err != nil {
		return err
	}
	if adopted {
		if w, err = s.window(ctx, p.window); err != nil {
			return err
		}
	}
	m, err := s.mark(ctx, w.id)
	if err != nil || m == w.mark {
		return err
	}
	geo, err := s.Tmux.Output(ctx, "display-message", "-p", "-t", p.pane,
		"#{pane_at_left}#{pane_at_top}#{pane_at_bottom}"+sep+"#{pane_width}")
	if err != nil {
		return err
	}
	f := split(geo, 2)
	inPlace := f[0] == "111"
	width := s.widthIn(w.width)
	var main layout.Layout
	switch {
	case w.size != size(w.width, w.height):
		main, _ = mainSaved(w, p.pane)
	case inPlace && f[1] == strconv.Itoa(width):
		now, _ := mainNow(w, p.pane)
		_, err := s.Tmux.Run(ctx, "set-option", "-w", "-t", w.id, OptMain, now.String(), ";",
			"set-option", "-w", "-t", w.id, OptMark, m)
		return err
	case inPlace:
		main, _ = mainSaved(w, p.pane)
	default:
		main, _ = mainNow(w, p.pane)
	}
	if _, err := s.Tmux.Run(ctx,
		"set-option", "-w", "-t", w.id, OptMain, main.String(), ";",
		"set-option", "-w", "-t", w.id, OptSize, size(w.width, w.height), ";",
		"select-layout", "-t", w.id, craft(main, w.width, w.height, p.pane, width, p.active).String()); err != nil {
		return err
	}
	return s.setMark(ctx, w.id)
}

// adopt moves the panes split off the panel (a split key pressed with the
// focus in it: tmux splits the active pane) to the main pane that had the
// focus last, split the same way, at half its size. A new pane that runs
// the default shell starts again in that pane's folder. It reports
// whether it moved any.
func (s *Sidebar) adopt(ctx context.Context, w window, p panel) (bool, error) {
	saved, err := layout.Parse(w.main)
	if err != nil {
		return false, nil
	}
	known := saved.Panes()
	out, err := s.Tmux.Output(ctx, "list-panes", "-t", w.id, "-F",
		strings.Join([]string{"#{pane_id}", "#{pane_left}", "#{pane_start_command}", "#{pane_current_path}"}, sep))
	if err != nil {
		return false, err
	}
	type live struct {
		id    string
		left  int
		start string
		cwd   string
	}
	var mains, fresh []live
	for _, l := range lines(out) {
		f := split(l, 4)
		left, _ := strconv.Atoi(f[1])
		pn := live{id: f[0], left: left, start: f[2], cwd: f[3]}
		switch {
		case pn.id == p.pane:
		case slices.Contains(known, pn.id):
			mains = append(mains, pn)
		case left < s.widthIn(w.width):
			fresh = append(fresh, pn) // in the panel's column: split off it
		}
	}
	if len(fresh) == 0 || len(mains) == 0 {
		return false, nil
	}
	target := mains[0]
	for _, want := range []string{w.last, saved.ActivePane()} {
		if i := slices.IndexFunc(mains, func(m live) bool { return m.id == want }); want != "" && i >= 0 {
			target = mains[i]
			break
		}
	}
	var args []string
	for _, pn := range fresh {
		dir := "-h" // beside the panel: a split side by side
		if pn.left == 0 {
			dir = "-v" // below it, at the same left edge: one above the other
		}
		args = append(args, "move-pane", dir, "-l", "50%", "-s", pn.id, "-t", target.id, ";")
		if pn.start == "" {
			args = append(args, "respawn-pane", "-k", "-t", pn.id, "-c", target.cwd, ";")
		}
	}
	if _, err := s.Tmux.Run(ctx, args[:len(args)-1]...); err != nil {
		return false, err
	}
	// tmux gave the room the new pane left to its neighbour before the
	// split: lay the window out with each new pane and its target even.
	if w, err = s.window(ctx, w.id); err != nil {
		return false, err
	}
	main, ok := mainNow(w, p.pane)
	if !ok {
		return true, nil
	}
	for _, pn := range fresh {
		main = main.Even(target.id, pn.id)
	}
	_, err = s.Tmux.Run(ctx,
		"set-option", "-w", "-t", w.id, OptMain, main.String(), ";",
		"set-option", "-w", "-t", w.id, OptSize, size(w.width, w.height), ";",
		"select-layout", "-t", w.id, craft(main, w.width, w.height, p.pane, s.widthIn(w.width), w.active == p.pane).String())
	return true, err
}

// alone moves a panel that is all its window has left to another window,
// with its client, which tmux would have moved there. With nowhere to
// go, or no client, the panel closes.
func (s *Sidebar) alone(ctx context.Context, p panel, open int) error {
	cs, err := s.clients(ctx)
	if err != nil {
		return err
	}
	c, ok := cs[p.client]
	if !ok {
		return s.close(ctx, p, open == 1, "")
	}
	to, err := s.elsewhere(ctx, p.window, "")
	if err != nil {
		return err
	}
	if to == "" {
		return s.close(ctx, p, open == 1, "")
	}
	return s.move(ctx, c, p, to)
}

// elsewhere is the window a panel goes to when its window goes: the last
// window of the same session, else another of its windows, else the
// current window of another session. Never the session avoid, nor one
// the sidebar skips. "" when there is none.
func (s *Sidebar) elsewhere(ctx context.Context, from, avoid string) (string, error) {
	out, err := s.Tmux.Output(ctx, "list-windows", "-a", "-F",
		strings.Join([]string{"#{window_id}", "#{session_name}", "#{window_active}", "#{window_last_flag}"}, sep))
	if err != nil {
		return "", err
	}
	type row struct{ id, session, active, last string }
	var rows []row
	session := ""
	for _, l := range lines(out) {
		f := split(l, 4)
		r := row{f[0], f[1], f[2], f[3]}
		rows = append(rows, r)
		if r.id == from && session == "" {
			session = r.session
		}
	}
	ok := func(r row) bool { return r.id != from && r.session != avoid && !s.skip(r.session) }
	if session != avoid {
		for _, want := range []func(row) bool{
			func(r row) bool { return r.last == "1" },
			func(row) bool { return true },
		} {
			for _, r := range rows {
				if r.session == session && ok(r) && want(r) {
					return r.id, nil
				}
			}
		}
	}
	for _, r := range rows {
		if r.session != session && r.active == "1" && ok(r) {
			return r.id, nil
		}
	}
	return "", nil
}

// Evacuate moves each panel out of what killing target (a session, a
// window or a pane, as the panel's rows name them) takes with it: the
// panel would die with the window, or be all that is left of it. The
// client goes along. A panel with nowhere to go closes.
func (s *Sidebar) Evacuate(ctx context.Context, target string) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	session, rest, _ := strings.Cut(strings.TrimPrefix(target, "="), ":")
	out, err := s.Tmux.Output(ctx, "display-message", "-p", "-t", target, "#{window_id}"+sep+"#{window_panes}")
	if err != nil {
		return err
	}
	f := split(out, 2)
	wid, panes := f[0], f[1]
	ps, err := s.panels(ctx)
	if err != nil {
		return err
	}
	cs, err := s.clients(ctx)
	if err != nil {
		return err
	}
	isSession, isPane := rest == "", strings.Contains(rest, ".")
	var errs []error
	left := len(ps)
	for _, p := range ps {
		hit := p.window == wid && (!isPane || panes == "2")
		avoid := ""
		if isSession {
			hit, avoid = p.session == session, session
		}
		if !hit {
			continue
		}
		c, ok := cs[p.client]
		to := ""
		if ok {
			if to, err = s.elsewhere(ctx, p.window, avoid); err != nil {
				return err
			}
		}
		if to == "" {
			errs = append(errs, s.close(ctx, p, left == 1, ""))
			left--
			continue
		}
		errs = append(errs, s.move(ctx, c, p, to))
	}
	return errors.Join(errs...)
}

// Reload tells every panel that windows came, went or were renamed.
func (s *Sidebar) Reload(ctx context.Context) error {
	ps, err := s.panels(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range ps {
		_, err := s.Tmux.Run(ctx, "send-keys", "-t", p.pane, ReloadKey)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ClientPane is the pane the client shows, and its window as
// "=session:index"; "" for a client that is not there. (display-message
// -c names the client to show a message on, not the pane to read.)
func (s *Sidebar) ClientPane(ctx context.Context, clientName string) (pane, window string, err error) {
	cs, err := s.clients(ctx)
	if err != nil {
		return "", "", err
	}
	c, ok := cs[clientName]
	if !ok {
		return "", "", nil
	}
	return c.pane, "=" + c.session + ":" + c.index, nil
}

// PanelOf is the client's panel pane; "" when it has none.
func (s *Sidebar) PanelOf(ctx context.Context, clientName string) (string, error) {
	ps, err := s.panels(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range ps {
		if p.client == clientName {
			return p.pane, nil
		}
	}
	return "", nil
}
