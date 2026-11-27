package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// An agent runs under its window's name (Namer). The launcher starts it
// under the window's name, or, in a window tmux names (automatic-rename),
// under `<repo>-<agent>`, which the window then takes as a name an agent
// gave it. A rename of the window renames the running agent: the
// window-renamed hook puts the new name on the pane (Renamed), and the
// pane's watcher types it into the agent once it idles at an empty input
// box (typeName). No two agents, and no agent and another window, share a
// name: the later one gets -2, -3. The window's name is its first agent
// pane's; another agent split into the window gets a number.

// nameRow is one pane and its window, as names are read.
type nameRow struct {
	window, windowName, pane, name, given string
	auto                                  bool
}

// nameTable is every pane of the server, each once.
type nameTable []nameRow

func (e *Env) names(ctx context.Context) (nameTable, error) {
	format := strings.Join([]string{"#{window_id}", "#{window_name}", "#{pane_id}", "#{" + OptName + "}", "#{automatic-rename}", "#{" + OptNameGiven + "}"}, "\x1f")
	out, err := e.Tmux.Output(ctx, "list-panes", "-a", "-F", format)
	if err != nil {
		return nil, err
	}
	var t nameTable
	seen := map[string]bool{}
	for l := range strings.SplitSeq(out, "\n") {
		f := strings.SplitN(l, "\x1f", 6)
		if len(f) < 6 || seen[f[2]] { // a linked window lists its panes again
			continue
		}
		seen[f[2]] = true
		t = append(t, nameRow{window: f[0], windowName: f[1], pane: f[2], name: f[3], auto: f[4] == "1", given: f[5]})
	}
	return t, nil
}

// taken reports whether another window, or the agent of another pane,
// has name.
func (t nameTable) taken(name, pane, window string) bool {
	for _, r := range t {
		if (r.window != window && r.windowName == name) || (r.pane != pane && r.name == name) {
			return true
		}
	}
	return false
}

// free is want, or want with the first number that makes it free.
func (t nameTable) free(want, pane, window string) string {
	name := want
	for n := 2; t.taken(name, pane, window); n++ {
		name = want + "-" + strconv.Itoa(n)
	}
	return name
}

// row is pane's row.
func (t nameTable) row(pane string) (nameRow, bool) {
	for _, r := range t {
		if r.pane == pane {
			return r, true
		}
	}
	return nameRow{}, false
}

// otherAgent reports whether an agent of another pane in window has a
// name.
func (t nameTable) otherAgent(pane, window string) bool {
	for _, r := range t {
		if r.window == window && r.pane != pane && r.name != "" {
			return true
		}
	}
	return false
}

// nameRun picks the name the launcher starts the pane's agent under: the
// one its arguments give, else its window's, else the name the session it
// resumes gave its window, else `<repo>-<agent>`, made free. The pane's
// window takes it, unless another agent's pane holds the window. The
// caller holds the lock.
func (e *Env) nameRun(ctx context.Context, a Agent, given, resume string) (string, error) {
	l, err := e.readLive(ctx)
	if err != nil {
		return "", err
	}
	t, err := e.names(ctx)
	if err != nil {
		return "", err
	}
	r, ok := t.row(e.Pane)
	if !ok {
		return "", errors.New("the pane is gone")
	}
	want := given
	if want == "" && !l.autoRename {
		want = l.windowName
	}
	if want == "" {
		want = e.recordedTitle(a.Name(), resume)
	}
	if want == "" {
		want = e.projectName(ctx) + "-" + a.Name()
	}
	name := t.free(want, e.Pane, r.window)
	if err := e.paneOpts(ctx, map[string]string{OptName: name, OptNameGiven: name}); err != nil {
		return "", err
	}
	if t.otherAgent(e.Pane, r.window) || (name == l.windowName && !l.autoRename) {
		return name, nil
	}
	if l.autoRename || l.title == l.windowName {
		// A name an agent gave: it goes when the run ends.
		if err := e.applyTitle(ctx, name); err != nil {
			return "", err
		}
		return name, e.paneOpts(ctx, map[string]string{optNamed: name})
	}
	_, err = e.Tmux.Run(ctx, "rename-window", "-t", r.window, name)
	return name, err
}

// projectName is the repository the launcher runs in, the main
// checkout's name in a worktree too; outside git, its folder's name.
func (e *Env) projectName(ctx context.Context) string {
	dir, err := os.Getwd()
	if err != nil {
		return "agent"
	}
	if h, err := e.headOf(ctx, dir); err == nil && h.Repo != "" {
		return h.Repo
	}
	return filepath.Base(dir)
}

// Renamed gives the agents of window its new name: the first agent pane
// the name, the others the name with a number. A name another window has
// renames the window too, which runs this again. The watcher of each pane
// whose agent's name is not typed in yet looks again: also when the user
// leaves the pane (pane-focus-out), which can leave a draft behind. A
// window tmux names keeps its agents' names: tmux renames it after the
// programs that run in it.
func (e *Env) Renamed(ctx context.Context, window string) error {
	defer e.lock(ctx)()
	t, err := e.names(ctx)
	if err != nil {
		return err
	}
	first := true
	for i, r := range t {
		if r.window != window || r.name == "" {
			continue
		}
		if r.auto {
			return nil
		}
		name := t.free(r.windowName, r.pane, window)
		if first && name != r.windowName {
			if _, err := e.Tmux.Run(ctx, "rename-window", "-t", window, name); err != nil {
				return err
			}
		}
		first = false
		if name != r.name {
			if err := e.optsOf(ctx, r.pane, map[string]string{OptName: name}); err != nil {
				return err
			}
			t[i].name = name
			e.debugf("rename %s: %s to %s", r.pane, r.name, name)
		}
		if name != r.given {
			e.wake(r.pane, wakeLook)
		}
	}
	return nil
}

// A rename waits for a pause in the user's keys in the pane, so a key
// pressed as the rename is typed never joins it: the rename of the window
// is keys too. tmux keeps a client's last key in whole seconds.
const (
	keyPause   = 2 * time.Second // since the last key of a client that shows the pane
	pauseRetry = time.Second     // when a look waits out the pause
)

// typeName types the pane's new name into its agent, once the agent
// idles at an empty input box and the user's keys there pause. Until then
// the name waits on the pane: each look tries again, and a look that
// waits on the pause alone asks for another. The caller holds the lock.
func (e *Env) typeName(ctx context.Context, w *watchState, l live) error {
	n, ok := w.a.(Namer)
	if !ok || l.name == "" || l.name == l.nameGiven || l.inMode || w.process <= 0 {
		return nil
	}
	text := n.RenameInput(l.name)
	if strings.ContainsFunc(text, unicode.IsControl) {
		return nil // the agent would act on it as it arrives
	}
	screen, err := e.Tmux.Output(ctx, "capture-pane", "-p", "-e", "-t", e.Pane)
	if err != nil {
		return err
	}
	if !n.Ready(w.process, l.session, screen, e.getenv) {
		e.debugf("rename %s to %s: the agent is not ready", e.Pane, l.name)
		return nil
	}
	typing, err := e.typing(ctx)
	if err != nil {
		return err
	}
	if typing {
		w.again.Reset(pauseRetry)
		return nil
	}
	_, err = e.Tmux.Run(ctx, "send-keys", "-t", e.Pane, "-l", text, ";",
		"send-keys", "-t", e.Pane, "Enter", ";",
		"set-option", "-p", "-t", e.Pane, OptNameGiven, l.name)
	if err == nil {
		e.debugf("rename %s: typed %q", e.Pane, text)
	}
	return err
}

// typing reports whether a client that shows the pane had a key within
// keyPause.
func (e *Env) typing(ctx context.Context) (bool, error) {
	out, err := e.Tmux.Output(ctx, "list-clients", "-F", "#{pane_id}\x1f#{client_activity}")
	if err != nil {
		return false, err
	}
	now := e.now().Unix()
	for l := range strings.SplitSeq(out, "\n") {
		pane, at, ok := strings.Cut(l, "\x1f")
		if !ok || pane != e.Pane {
			continue
		}
		if sec, err := strconv.ParseInt(at, 10, 64); err == nil && now-sec < int64(keyPause/time.Second) {
			return true, nil
		}
	}
	return false, nil
}
