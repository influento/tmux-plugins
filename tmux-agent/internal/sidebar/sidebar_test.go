package sidebar_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/sidebar"
)

// env is a private server with one attached client on proj-a, 120×31
// (one status line: windows are 120×30).
type env struct {
	*tmuxtest.Server
	side   *sidebar.Sidebar
	client string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tmuxtest.Require(t, "tmux")
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a", Width: 120, Height: 30})
	s.Attach(t, "proj-a", 120, 31)
	client := s.MustTmux(t, "list-clients", "-F", "#{client_name}")
	side := &sidebar.Sidebar{
		Tmux:    s.Client,
		Command: func(client, origin string) string { return "exec sleep 600" },
	}
	return &env{Server: s, side: side, client: client}
}

func (e *env) ctx() context.Context { return context.Background() }

// panel is the client's panel pane, "" for none.
func (e *env) panel(t *testing.T) string {
	t.Helper()
	p, err := e.side.PanelOf(e.ctx(), e.client)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type geo struct{ left, top, width, height int }

func (e *env) geo(t *testing.T, pane string) geo {
	t.Helper()
	f := strings.Fields(e.Display(t, pane, "#{pane_left} #{pane_top} #{pane_width} #{pane_height}"))
	n := func(i int) int { v, _ := strconv.Atoi(f[i]); return v }
	return geo{n(0), n(1), n(2), n(3)}
}

// wantPanel checks the panel sits in window w at the left edge, full
// height, 40 wide, and the main panes fill the rest.
func (e *env) wantPanel(t *testing.T, w string) {
	t.Helper()
	p := e.panel(t)
	if p == "" {
		t.Fatal("no panel")
	}
	if got := e.Display(t, p, "#{window_id}"); got != w {
		t.Fatalf("panel in %s, want %s", got, w)
	}
	g := e.geo(t, p)
	height, _ := strconv.Atoi(e.Display(t, w, "#{window_height}"))
	width, _ := strconv.Atoi(e.Display(t, w, "#{window_width}"))
	if g != (geo{0, 0, 40, height}) {
		t.Errorf("panel at %+v, want the left edge, 40 wide, %d high", g, height)
	}
	for _, id := range strings.Fields(e.MustTmux(t, "list-panes", "-t", w, "-F", "#{pane_id}")) {
		if id == p {
			continue
		}
		g := e.geo(t, id)
		if g.left < 41 || g.left+g.width > width || g.width < 5 {
			t.Errorf("main pane %s at %+v: not in the 41..%d columns", id, g, width)
		}
	}
}

// wantFull checks window w has no panel and its panes span its width.
func (e *env) wantFull(t *testing.T, w string) {
	t.Helper()
	width, _ := strconv.Atoi(e.Display(t, w, "#{window_width}"))
	right := 0
	for _, id := range strings.Fields(e.MustTmux(t, "list-panes", "-t", w, "-F", "#{pane_id}")) {
		if e.Display(t, id, "#{"+sidebar.OptClient+"}") != "" {
			t.Fatalf("window %s still has the panel %s", w, id)
		}
		g := e.geo(t, id)
		right = max(right, g.left+g.width)
		if g.left == 0 && g.width < 20 {
			t.Errorf("pane %s squeezed to %+v", id, g)
		}
	}
	if right != width {
		t.Errorf("panes of %s end at column %d, want %d", w, right, width)
	}
}

func (e *env) where(t *testing.T) string {
	t.Helper()
	return e.MustTmux(t, "list-clients", "-F", "#{window_id}")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// The key opens the panel at the left edge with the focus, the main panes
// keeping their proportions, and closes it again, the window as before.
func TestToggle(t *testing.T) {
	e := newEnv(t)
	w := e.Display(t, "=proj-a:", "#{window_id}")
	e.MustTmux(t, "split-window", "-h", "-t", w)
	before := e.Display(t, w, "#{window_layout}")
	must(t, e.side.Toggle(e.ctx(), e.client))
	e.wantPanel(t, w)
	p := e.panel(t)
	if got := e.Display(t, w, "#{pane_id}"); got != p {
		t.Errorf("active pane = %s, want the panel %s", got, p)
	}
	if got := e.Display(t, p, "#{@revive_skip}"); got != "1" {
		t.Errorf("@revive_skip = %q, want 1", got)
	}
	if got := e.MustTmux(t, "show-options", "-gqv", sidebar.OptOpen); got != "1" {
		t.Errorf("%s = %q, want 1", sidebar.OptOpen, got)
	}
	var widths []int
	for _, id := range strings.Fields(e.MustTmux(t, "list-panes", "-t", w, "-F", "#{pane_id}")) {
		if id != p {
			widths = append(widths, e.geo(t, id).width)
		}
	}
	if len(widths) != 2 || widths[0]-widths[1] > 1 || widths[1]-widths[0] > 1 {
		t.Errorf("main pane widths = %v, want two equal", widths)
	}

	must(t, e.side.Toggle(e.ctx(), e.client))
	if p := e.panel(t); p != "" {
		t.Fatalf("panel %s still open", p)
	}
	if after := e.Display(t, w, "#{window_layout}"); after != before {
		t.Errorf("layout after close =\n%s\nwant as before\n%s", after, before)
	}
	if got := e.MustTmux(t, "show-options", "-gqv", sidebar.OptOpen); got != "" {
		t.Errorf("%s = %q after the last panel closed", sidebar.OptOpen, got)
	}
}

// Go takes the panel along to another window and another session, keeps
// the focus where it was, and gives the window it left its width back.
func TestGoMovesThePanel(t *testing.T) {
	e := newEnv(t)
	w1 := e.Display(t, "=proj-a:", "#{window_id}")
	e.MustTmux(t, "split-window", "-h", "-t", w1)
	w2 := e.MustTmux(t, "new-window", "-d", "-t", "=proj-a:", "-P", "-F", "#{window_id}")
	e.MustTmux(t, "split-window", "-v", "-t", w2)
	w3 := e.MustTmux(t, "new-session", "-d", "-s", "proj-b", "-x", "80", "-y", "20", "-P", "-F", "#{window_id}")
	must(t, e.side.Toggle(e.ctx(), e.client))
	p := e.panel(t)

	must(t, e.side.Go(e.ctx(), e.client, w2))
	e.wantPanel(t, w2)
	e.wantFull(t, w1)
	if got := e.where(t); got != w2 {
		t.Errorf("client on %s, want %s", got, w2)
	}
	if got := e.Display(t, w2, "#{pane_id}"); got != p {
		t.Errorf("active pane in %s = %s, want the panel", w2, got)
	}

	// Another session, made at 80×20: it takes the client's size.
	must(t, e.side.Go(e.ctx(), e.client, "=proj-b:0"))
	e.wantPanel(t, w3)
	e.wantFull(t, w2)

	// With the focus in a main pane, the panel comes along without it.
	main := e.MustTmux(t, "list-panes", "-t", w3, "-F", "#{?#{"+sidebar.OptClient+"},,#{pane_id}}")
	e.MustTmux(t, "select-pane", "-t", strings.TrimSpace(main))
	must(t, e.side.Go(e.ctx(), e.client, w1))
	e.wantPanel(t, w1)
	if got := e.Display(t, w1, "#{pane_id}"); got == p {
		t.Error("the panel took the focus it did not have")
	}
}

// Step goes to the next, previous or numbered window, panel and all.
func TestStep(t *testing.T) {
	e := newEnv(t)
	w0 := e.Display(t, "=proj-a:", "#{window_id}")
	w1 := e.MustTmux(t, "new-window", "-d", "-t", "=proj-a:1", "-P", "-F", "#{window_id}")
	must(t, e.side.Toggle(e.ctx(), e.client))
	must(t, e.side.Step(e.ctx(), e.client, "+"))
	e.wantPanel(t, w1)
	must(t, e.side.Step(e.ctx(), e.client, "+")) // wraps
	e.wantPanel(t, w0)
	must(t, e.side.Step(e.ctx(), e.client, "1"))
	e.wantPanel(t, w1)
	must(t, e.side.Step(e.ctx(), e.client, "-"))
	e.wantPanel(t, w0)
}

// Keep puts the panel back after a split, a resize of the window and a
// preset layout, and leaves a window it laid out alone.
func TestKeep(t *testing.T) {
	e := newEnv(t)
	w := e.Display(t, "=proj-a:", "#{window_id}")
	must(t, e.side.Toggle(e.ctx(), e.client))
	p := e.panel(t)
	main := strings.TrimSpace(e.MustTmux(t, "list-panes", "-t", w, "-F", "#{?#{"+sidebar.OptClient+"},,#{pane_id}}"))

	e.MustTmux(t, "split-window", "-h", "-t", main)
	must(t, e.side.Keep(e.ctx()))
	e.wantPanel(t, w)

	e.MustTmux(t, "resize-pane", "-t", p, "-x", "60")
	must(t, e.side.Keep(e.ctx()))
	e.wantPanel(t, w)

	must(t, e.side.Preset(e.ctx(), e.client, "even-vertical"))
	must(t, e.side.Keep(e.ctx()))
	e.wantPanel(t, w)
	var tops []int
	for _, id := range strings.Fields(e.MustTmux(t, "list-panes", "-t", w, "-F", "#{pane_id}")) {
		if id != p {
			tops = append(tops, e.geo(t, id).top)
		}
	}
	if len(tops) != 2 || tops[0] == tops[1] {
		t.Errorf("main pane tops = %v, want one above the other", tops)
	}

	before := e.Display(t, w, "#{window_layout}")
	must(t, e.side.Keep(e.ctx()))
	if after := e.Display(t, w, "#{window_layout}"); after != before {
		t.Errorf("keep changed a window it laid out:\n%s\n%s", before, after)
	}
}

// A panel left alone in its window, its last other pane closed, moves to
// another window with its client; the empty window goes.
func TestAlonePanelMoves(t *testing.T) {
	e := newEnv(t)
	w0 := e.Display(t, "=proj-a:", "#{window_id}")
	w1 := e.MustTmux(t, "new-window", "-d", "-t", "=proj-a:1", "-P", "-F", "#{window_id}")
	must(t, e.side.Toggle(e.ctx(), e.client))
	main := strings.TrimSpace(e.MustTmux(t, "list-panes", "-t", w0, "-F", "#{?#{"+sidebar.OptClient+"},,#{pane_id}}"))
	e.MustTmux(t, "kill-pane", "-t", main)
	must(t, e.side.Keep(e.ctx()))
	e.wantPanel(t, w1)
	if got := e.where(t); got != w1 {
		t.Errorf("client on %s, want %s", got, w1)
	}
	if ws := e.MustTmux(t, "list-windows", "-a", "-F", "#{window_id}"); strings.Contains(ws, w0) {
		t.Errorf("window %s is still there: %s", w0, ws)
	}
}

// Follow moves the panel after a switch that did not go through Go, and
// closes the panel of a client that is gone.
func TestFollow(t *testing.T) {
	e := newEnv(t)
	w1 := e.MustTmux(t, "new-window", "-d", "-t", "=proj-a:1", "-P", "-F", "#{window_id}")
	must(t, e.side.Toggle(e.ctx(), e.client))
	e.MustTmux(t, "select-window", "-t", w1)
	must(t, e.side.Follow(e.ctx()))
	e.wantPanel(t, w1)

	e.MustTmux(t, "detach-client", "-t", e.client)
	tmuxtest.WaitFor(t, 0, "the client to go", func() bool {
		out, _ := e.Tmux("list-clients")
		return out == ""
	})
	must(t, e.side.Follow(e.ctx()))
	if p := e.panel(t); p != "" {
		t.Errorf("panel %s of a gone client still open", p)
	}
	e.wantFull(t, w1)
}

// Before its window or session is killed, the panel moves out, with its
// client: the window's last window, else another session.
func TestEvacuate(t *testing.T) {
	e := newEnv(t)
	w0 := e.Display(t, "=proj-a:", "#{window_id}")
	w1 := e.MustTmux(t, "new-window", "-d", "-t", "=proj-a:1", "-P", "-F", "#{window_id}")
	wb := e.MustTmux(t, "new-session", "-d", "-s", "proj-b", "-P", "-F", "#{window_id}")
	must(t, e.side.Toggle(e.ctx(), e.client))
	must(t, e.side.Evacuate(e.ctx(), "=proj-a:0"))
	e.wantPanel(t, w1)
	e.wantFull(t, w0)
	must(t, e.side.Evacuate(e.ctx(), "=proj-a:"))
	e.wantPanel(t, wb)
	if got := e.where(t); got != wb {
		t.Errorf("client on %s, want %s", got, wb)
	}
}

// A split key pressed with the focus in the panel splits the panel: the
// new pane moves to the main pane that had the focus last, split the same
// way at half its size, its shell started again in that pane's folder.
func TestSplitFromThePanel(t *testing.T) {
	e := newEnv(t)
	w := e.Display(t, "=proj-a:", "#{window_id}")
	main := e.Display(t, w, "#{pane_id}")
	cwd := e.Display(t, main, "#{pane_current_path}")
	must(t, e.side.Toggle(e.ctx(), e.client))
	p := e.panel(t)

	right := e.MustTmux(t, "split-window", "-h", "-c", "/", "-t", p, "-P", "-F", "#{pane_id}")
	must(t, e.side.Keep(e.ctx()))
	e.wantPanel(t, w)
	m, r := e.geo(t, main), e.geo(t, right)
	if m.top != r.top || m.height != r.height || r.left != m.left+m.width+1 || m.width-r.width > 1 || r.width-m.width > 1 {
		t.Errorf("after | from the panel: main %+v, new %+v; want two halves side by side", m, r)
	}
	tmuxtest.WaitFor(t, 5*time.Second, "the new shell in the main pane's folder", func() bool {
		return e.Display(t, right, "#{pane_current_path}") == cwd
	})

	e.MustTmux(t, "select-pane", "-t", p)
	below := e.MustTmux(t, "split-window", "-v", "-t", p, "-P", "-F", "#{pane_id}")
	must(t, e.side.Keep(e.ctx()))
	e.wantPanel(t, w)
	m, b := e.geo(t, main), e.geo(t, below)
	if b.left != m.left || b.width != m.width || b.top != m.top+m.height+1 {
		t.Errorf("after - from the panel: main %+v, new %+v; want the new one below the main pane", m, b)
	}
}

// The README's | and - keys split the main pane that had the focus last
// when the focus is in the panel, in that pane's folder, with no hook
// after: the panel never splits, so tmux draws once. With the focus in a
// main pane they split it as tmux's own keys do.
func TestSplitKeys(t *testing.T) {
	e := newEnv(t)
	readme, err := os.ReadFile("../../../README.md")
	must(t, err)
	var binds []string
	for l := range strings.Lines(string(readme)) {
		if strings.HasPrefix(l, "bind | ") || strings.HasPrefix(l, "bind - ") {
			binds = append(binds, l)
		}
	}
	if len(binds) != 2 {
		t.Fatalf("README has %d of the | and - lines, want 2", len(binds))
	}
	conf := filepath.Join(t.TempDir(), "split.conf")
	must(t, os.WriteFile(conf, []byte(strings.Join(binds, "")), 0o600))
	e.MustTmux(t, "source-file", conf)
	prefix := e.MustTmux(t, "show-options", "-gv", "prefix")
	press := func(key string) string {
		t.Helper()
		before := strings.Fields(e.MustTmux(t, "list-panes", "-a", "-F", "#{pane_id}"))
		e.MustTmux(t, "send-keys", "-K", "-c", e.client, prefix, key)
		var id string
		tmuxtest.WaitFor(t, 5*time.Second, "a new pane after prefix "+key, func() bool {
			id = e.MustTmux(t, "display-message", "-p", "-c", e.client, "#{pane_id}")
			return !slices.Contains(before, id)
		})
		return id
	}

	w := e.Display(t, "=proj-a:", "#{window_id}")
	main := e.Display(t, w, "#{pane_id}")
	dir := t.TempDir()
	e.MustTmux(t, "respawn-pane", "-k", "-c", dir, "-t", main)
	must(t, e.side.Toggle(e.ctx(), e.client))
	p := e.panel(t)

	right := press("|")
	e.wantPanel(t, w)
	m, r := e.geo(t, main), e.geo(t, right)
	if m.top != r.top || m.height != r.height || r.left != m.left+m.width+1 || m.width-r.width > 1 || r.width-m.width > 1 {
		t.Errorf("after | from the panel: main %+v, new %+v; want two halves side by side", m, r)
	}
	tmuxtest.WaitFor(t, 5*time.Second, "the new shell in the main pane's folder", func() bool {
		return e.Display(t, right, "#{pane_current_path}") == dir
	})

	e.MustTmux(t, "select-pane", "-t", p)
	below := press("-")
	e.wantPanel(t, w)
	m, b := e.geo(t, main), e.geo(t, below)
	if b.left != m.left || b.width != m.width || b.top != m.top+m.height+1 {
		t.Errorf("after - from the panel: main %+v, new %+v; want the new one below the main pane", m, b)
	}

	// From a main pane: tmux's own split of that pane.
	e.MustTmux(t, "select-pane", "-t", right)
	under := press("-")
	r, u := e.geo(t, right), e.geo(t, under)
	if u.left != r.left || u.width != r.width || u.top != r.top+r.height+1 {
		t.Errorf("after - from a main pane: %+v, new %+v; want the new one below it", r, u)
	}
}

// The panel takes @sidebar_style, and @sidebar_active_style while it has
// the focus; with only the first, it serves both.
func TestPanelStyles(t *testing.T) {
	for _, c := range []struct {
		name, active, want string
	}{
		{"one style", "", "bg=#181825"},
		{"a focused style", "bg=#181825,fg=#cdd6f4", "bg=#181825,fg=#cdd6f4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.MustTmux(t, "set-option", "-g", sidebar.OptStyle, "bg=#181825")
			if c.active != "" {
				e.MustTmux(t, "set-option", "-g", sidebar.OptActiveStyle, c.active)
			}
			must(t, e.side.Toggle(e.ctx(), e.client))
			p := e.panel(t)
			if got := e.MustTmux(t, "show-options", "-pqv", "-t", p, "window-style"); got != "bg=#181825" {
				t.Errorf("window-style = %q, want bg=#181825", got)
			}
			if got := e.MustTmux(t, "show-options", "-pqv", "-t", p, "window-active-style"); got != c.want {
				t.Errorf("window-active-style = %q, want %q", got, c.want)
			}
		})
	}
}

// seenBySaves runs every tmux call of the sidebar, then lists the panes
// as a save from another client would see them between two calls: the
// panes without @revive_skip.
type seenBySaves struct {
	sidebar.Tmux
	t     *testing.T
	s     *tmuxtest.Server
	marks []string // per call: the panes left unmarked after it
}

func (o *seenBySaves) look() {
	o.marks = append(o.marks, o.s.MustTmux(o.t, "list-panes", "-a", "-f", "#{?#{"+sidebar.OptReviveSkip+"},0,1}", "-F", "#{pane_id}"))
}

func (o *seenBySaves) Run(ctx context.Context, args ...string) (string, error) {
	defer o.look()
	return o.Tmux.Run(ctx, args...)
}

func (o *seenBySaves) Output(ctx context.Context, args ...string) (string, error) {
	defer o.look()
	return o.Tmux.Output(ctx, args...)
}

// tmux-revive saves in the background after every split, so a save can
// land between any two tmux calls the sidebar makes: the panel is never
// there without @revive_skip, or a save keeps it as a pane of its window.
func TestPanelHiddenFromSaves(t *testing.T) {
	e := newEnv(t)
	seen := &seenBySaves{Tmux: e.side.Tmux, t: t, s: e.Server}
	e.side.Tmux = seen
	must(t, e.side.Toggle(e.ctx(), e.client))
	p := e.panel(t)
	if p == "" {
		t.Fatal("no panel")
	}
	for i, unmarked := range seen.marks {
		if slices.Contains(strings.Fields(unmarked), p) {
			t.Errorf("after tmux call %d of %d the panel %s has no @revive_skip", i+1, len(seen.marks), p)
		}
	}
}
