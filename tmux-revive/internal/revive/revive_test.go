package revive_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux"
	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-revive/internal/revive"
	"github.com/influento/tmux-plugins/tmux-revive/internal/selftest"
	"github.com/influento/tmux-plugins/tmux-revive/internal/snapshot"
)

func TestMain(m *testing.M) { selftest.Main(m) }

// baseConfig is tmux.conf as dotfiles writes it for tmux-revive, with the
// scrollback timer off: tests capture by hand. Shells keep no history
// file, which they would write into the sandbox as it is removed.
const baseConfig = `set -g default-shell /bin/sh
set-environment -g HISTFILE ''
set -g history-limit 5000
set -g base-index 0
set -g @revive_scrollback_interval 0
`

const initLine = "run-shell 'tmux-revive tmux-init'\n"

const sessionUUID = "11111111-2222-4333-8444-555555555555"

// env is a sandbox whose server runs on the default socket, so that
// tmux-revive, run like a terminal or a hook would run it, finds it, and a
// server it starts loads the sandbox's tmux.conf.
type env struct {
	*tmuxtest.Sandbox
	tmux  *tmux.Client
	store *snapshot.Store
}

func newEnv(t *testing.T, config string) *env {
	t.Helper()
	tmuxtest.Require(t, "tmux", "script")
	sb := tmuxtest.NewSandbox(t)
	selftest.Install(t, sb)
	dir := filepath.Join(sb.ConfigHome, "tmux")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tmux.conf"), []byte(baseConfig+config+initLine), 0o600); err != nil {
		t.Fatal(err)
	}
	sockDir := filepath.Join(sb.RuntimeDir, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	e := &env{
		Sandbox: sb,
		tmux:    &tmux.Client{Socket: filepath.Join(sockDir, "default"), Env: sb.Environ()},
		store:   &snapshot.Store{Dir: filepath.Join(sb.StateHome, "tmux-revive")},
	}
	t.Cleanup(func() { e.tmux.Run(context.Background(), "kill-server") })
	return e
}

func (e *env) mustTmux(t *testing.T, args ...string) string {
	t.Helper()
	out, err := e.tmux.Output(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// revive runs tmux-revive as a terminal would: outside tmux.
func (e *env) revive(t *testing.T, args ...string) string {
	t.Helper()
	out, err := e.Cmd(t, "tmux-revive", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux-revive %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// saveNow saves and returns what the save did. A save a hook started may
// be waiting already, and then this one is coalesced into it; saveNow
// waits for it and tries again.
func (e *env) saveNow(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if err := revive.WaitIdle(ctx, filepath.Join(e.RuntimeDir, "tmux-revive")); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(e.revive(t, "save", "-v", "--settle", "0")); got != "coalesced" {
			return got
		}
		if ctx.Err() != nil {
			t.Fatal("every save was coalesced")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *env) mkdir(t *testing.T, name string) string {
	t.Helper()
	d := filepath.Join(e.Home, name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

// start starts the server with one session, then marks it ready the way
// prefix r does on a live server: tmux-init again.
func (e *env) start(t *testing.T, session, dir string) {
	t.Helper()
	e.mustTmux(t, "new-session", "-d", "-s", session, "-c", dir, "-x", "120", "-y", "40")
	e.revive(t, "tmux-init")
}

func (e *env) latest(t *testing.T) (*snapshot.Snapshot, snapshot.Entry) {
	t.Helper()
	snap, en, ok, err := e.store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return nil, en
	}
	return snap, en
}

// waitSnapshot waits for the newest snapshot to pass cond.
func (e *env) waitSnapshot(t *testing.T, what string, cond func(*snapshot.Snapshot) bool) *snapshot.Snapshot {
	t.Helper()
	var snap *snapshot.Snapshot
	tmuxtest.WaitFor(t, 10*time.Second, what, func() bool {
		snap, _ = e.latest(t)
		return snap != nil && cond(snap)
	})
	return snap
}

// waitForeground waits for the pane's foreground program to be name.
func (e *env) waitForeground(t *testing.T, pane, name string) {
	t.Helper()
	tmuxtest.WaitFor(t, 10*time.Second, name+" in "+pane, func() bool {
		out, err := e.tmux.Output(context.Background(), "display-message", "-p", "-t", pane, "#{pane_current_command}")
		return err == nil && out == name
	})
}

func (e *env) waitContent(t *testing.T, pane, text string) {
	t.Helper()
	tmuxtest.WaitFor(t, 10*time.Second, fmt.Sprintf("%q in %s", text, pane), func() bool {
		out, err := e.tmux.Output(context.Background(), "capture-pane", "-p", "-S", "-", "-t", pane)
		return err == nil && strings.Contains(out, text)
	})
}

func (e *env) sessions(t *testing.T) []string {
	t.Helper()
	out, err := e.tmux.Output(context.Background(), "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return nil
	}
	names := strings.Split(out, "\n")
	slices.Sort(names)
	return names
}

func (e *env) serverRunning() bool {
	_, err := e.tmux.Run(context.Background(), "list-sessions")
	return err == nil
}

// paneIDsAre is a layout without its pane ids, which a restored window
// does not keep.
var paneIDsAre = regexp.MustCompile(`"I":"%[0-9]+"`)

// comparable is snap as a restore must bring it back: layouts without
// pane ids, and no attach times.
func comparable(snap *snapshot.Snapshot) string {
	c := *snap
	c.Time = time.Time{}
	c.Sessions = slices.Clone(snap.Sessions)
	for i := range c.Sessions {
		s := &c.Sessions[i]
		s.LastAttached = 0
		s.Windows = slices.Clone(s.Windows)
		for j := range s.Windows {
			s.Windows[j].Layout = paneIDsAre.ReplaceAllString(s.Windows[j].Layout, "")
		}
	}
	b, _ := json.MarshalIndent(c, "", "  ") // a Snapshot always marshals
	return string(b)
}

// Save, kill the server, restore, compare: sessions, windows, indexes,
// names, layouts, folders, the active window and pane, zoom, programs,
// @revive_cmd, window and pane ids, and scrollback.
func TestSaveKillRestore(t *testing.T) {
	e := newEnv(t, "set -g @revive_programs 'watchprog'\n")
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	e.Link(t, "watchprog", sleepPath)
	dirA, dirB := e.mkdir(t, "proj-a"), e.mkdir(t, "proj-a/sub dir#1")

	e.start(t, "proj-a", dirA)
	e.mustTmux(t, "rename-window", "-t", "=proj-a:0", "edit")
	e.mustTmux(t, "split-window", "-t", "=proj-a:0", "-c", dirB)
	e.mustTmux(t, "split-window", "-h", "-t", "=proj-a:0.0", "-c", dirA)
	e.mustTmux(t, "select-layout", "-t", "=proj-a:0", "main-vertical")
	e.mustTmux(t, "select-pane", "-t", "=proj-a:0.1")
	e.mustTmux(t, "new-window", "-d", "-t", "=proj-a:3", "-c", dirB)
	e.mustTmux(t, "split-window", "-t", "=proj-a:3", "-c", dirA)
	e.mustTmux(t, "resize-pane", "-Z", "-t", "=proj-a:3.0")
	e.mustTmux(t, "new-window", "-t", "=proj-a:1", "-c", dirA)
	e.mustTmux(t, "send-keys", "-t", "=proj-a:1", "watchprog 1000", "Enter")
	e.waitForeground(t, "=proj-a:1", "watchprog")

	e.mustTmux(t, "new-session", "-d", "-s", "proj-b", "-c", dirB, "-x", "100", "-y", "30")
	e.mustTmux(t, "rename-window", "-t", "=proj-b:0", "w#1")
	e.mustTmux(t, "set-option", "-p", "-t", "=proj-b:0.0", "@revive_cmd", "claude --resume "+sessionUUID)
	e.mustTmux(t, "send-keys", "-t", "=proj-b:0.0", "echo marker-$((6*7))", "Enter")
	e.waitContent(t, "=proj-b:0.0", "marker-42")

	e.saveNow(t) // gives the new panes their ids, which capture needs
	e.revive(t, "capture")
	before := e.waitSnapshot(t, "the full state saved", func(s *snapshot.Snapshot) bool {
		return s.Panes() == 7 && strings.Contains(comparable(s), `"watchprog"`) && strings.Contains(comparable(s), sessionUUID)
	})
	_, beforeEntry := e.latest(t)
	for _, want := range []string{`"name": "edit"`, `"name": "w#1"`, `"zoomed": true`, `"index": 3`, `"cwd": "` + dirB + `"`} {
		if !strings.Contains(comparable(before), want) {
			t.Errorf("snapshot lacks %s:\n%s", want, comparable(before))
		}
	}

	e.mustTmux(t, "kill-server")
	time.Sleep(500 * time.Millisecond) // for hooks run at shutdown
	if after, en := e.latest(t); en.Path != beforeEntry.Path || !snapshot.Equal(after, before) {
		t.Fatalf("kill-server changed the snapshot: %s → %s", beforeEntry.Path, en.Path)
	}

	out := e.revive(t, "restore")
	if !strings.Contains(out, "new sessions: proj-a, proj-b") {
		t.Errorf("restore said:\n%s", out)
	}
	tmuxtest.WaitFor(t, 10*time.Second, "the resume command to run", func() bool { return len(e.Calls("claude")) == 1 })
	e.waitForeground(t, "=proj-a:1", "watchprog")
	after := e.waitSnapshot(t, "the restored state saved", func(s *snapshot.Snapshot) bool {
		return comparable(s) == comparable(before)
	})
	if got, want := comparable(after), comparable(before); got != want {
		t.Errorf("restored:\n%s\nwant:\n%s", got, want)
	}
	if c := e.Calls("claude"); len(c) != 1 || !slices.Equal(c[0].Args, []string{"--resume", sessionUUID}) {
		t.Errorf("claude calls = %v, want one with --resume %s", c, sessionUUID)
	}
	e.waitContent(t, "=proj-b:0.0", "marker-42")
	if got := e.mustTmux(t, "display-message", "-p", "-t", "=proj-b:0", "#{window_name} #{automatic-rename}"); got != "w#1 0" {
		t.Errorf("window name, automatic-rename = %q, want %q", got, "w#1 0")
	}
}

// A save after kill-server or SIGTERM finds no server and writes nothing,
// whatever hooks tmux runs on the way down.
func TestServerStopKeepsTheSnapshot(t *testing.T) {
	for _, stop := range []string{"kill-server", "SIGTERM"} {
		t.Run(stop, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, "")
			e.start(t, "proj-a", e.Home)
			e.mustTmux(t, "new-session", "-d", "-s", "proj-b")
			e.mustTmux(t, "split-window", "-t", "=proj-b:0")
			before := e.waitSnapshot(t, "both sessions saved", func(s *snapshot.Snapshot) bool { return s.Panes() == 3 })
			entries, err := e.store.Entries()
			if err != nil {
				t.Fatal(err)
			}
			if stop == "SIGTERM" {
				pid := e.mustTmux(t, "display-message", "-p", "#{pid}")
				var n int
				fmt.Sscan(pid, &n)
				if err := syscall.Kill(n, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			} else {
				e.mustTmux(t, "kill-server")
			}
			tmuxtest.WaitFor(t, 5*time.Second, "the server to stop", func() bool { return !e.serverRunning() })
			time.Sleep(time.Second) // for hooks run at shutdown
			if got := e.saveNow(t); got != "no server" {
				t.Errorf("save after %s = %q, want no server", stop, got)
			}
			after, err := e.store.Entries()
			if err != nil {
				t.Fatal(err)
			}
			latest, _ := e.latest(t)
			if !slices.Equal(after, entries) || !snapshot.Equal(latest, before) {
				t.Errorf("%s changed the snapshots: %v → %v", stop, entries, after)
			}
		})
	}
}

// A save that would lose panes while systemd is stopping writes nothing:
// at shutdown, panes may die before tmux does.
func TestShutdownGuard(t *testing.T) {
	e := newEnv(t, "")
	state := filepath.Join(e.Home, "systemd-state")
	e.Stub(t, "systemctl", "#!/bin/sh\ncat \"$HOME/systemd-state\" 2>/dev/null\n")
	e.start(t, "proj-a", e.Home)
	e.mustTmux(t, "split-window", "-t", "=proj-a:0")
	e.mustTmux(t, "split-window", "-t", "=proj-a:0")
	e.waitSnapshot(t, "three panes saved", func(s *snapshot.Snapshot) bool { return s.Panes() == 3 })

	if err := os.WriteFile(state, []byte("stopping\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.mustTmux(t, "kill-pane", "-t", "=proj-a:0.2")
	if got := e.saveNow(t); got != "stopping" {
		t.Errorf("save while stopping = %q, want stopping", got)
	}
	if snap, _ := e.latest(t); snap.Panes() != 3 {
		t.Errorf("snapshot has %d panes, want the 3 from before the shutdown", snap.Panes())
	}
	if log, _ := os.ReadFile(filepath.Join(e.store.Dir, "log")); !strings.Contains(string(log), "save skipped: shutting down") {
		t.Errorf("log does not record the skipped save:\n%s", log)
	}

	if err := os.WriteFile(state, []byte("running\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.mustTmux(t, "kill-pane", "-t", "=proj-a:0.1")
	e.waitSnapshot(t, "the closed pane dropped", func(s *snapshot.Snapshot) bool { return s.Panes() == 1 })
}

// A server attach or restore has not marked never saves, so it cannot
// replace the snapshot a restore still needs.
func TestUnreadyServerSavesNothing(t *testing.T) {
	e := newEnv(t, "")
	e.mustTmux(t, "new-session", "-d", "-s", "proj-a")
	e.mustTmux(t, "new-window", "-t", "=proj-a:")
	if got := e.saveNow(t); got != "not ready" {
		t.Errorf("save = %q, want not ready", got)
	}
	if es, _ := e.store.Entries(); len(es) != 0 {
		t.Errorf("snapshots = %v, want none", es)
	}
}

// The hooks tmux-init sets save after structural changes and after
// @revive_cmd changes, without anyone running save.
func TestHooksSave(t *testing.T) {
	e := newEnv(t, "")
	e.start(t, "proj-a", e.Home)
	e.waitSnapshot(t, "the first save", func(s *snapshot.Snapshot) bool { return s.Panes() == 1 })
	e.mustTmux(t, "new-window", "-t", "=proj-a:")
	e.waitSnapshot(t, "a new window saved", func(s *snapshot.Snapshot) bool { return s.Windows() == 2 })
	e.mustTmux(t, "split-window", "-t", "=proj-a:1")
	e.waitSnapshot(t, "a split saved", func(s *snapshot.Snapshot) bool { return s.Panes() == 3 })
	e.mustTmux(t, "set-option", "-p", "-t", "=proj-a:1.0", "@revive_cmd", "nvim notes")
	e.waitSnapshot(t, "@revive_cmd saved", func(s *snapshot.Snapshot) bool {
		return strings.Contains(comparable(s), `"command": "nvim notes"`)
	})
	e.mustTmux(t, "set-option", "-p", "-t", "=proj-a:1.1", "@revive_cmd", "printf 'a\nb'")
	e.waitSnapshot(t, "a command with a newline saved", func(s *snapshot.Snapshot) bool {
		return strings.Contains(comparable(s), `"command": "printf 'a\nb'"`)
	})
	e.mustTmux(t, "rename-session", "-t", "=proj-a", "proj-z")
	e.waitSnapshot(t, "a rename saved", func(s *snapshot.Snapshot) bool { return s.Sessions[0].Name == "proj-z" })
	e.mustTmux(t, "kill-window", "-t", "=proj-z:0")
	e.waitSnapshot(t, "a closed window dropped", func(s *snapshot.Snapshot) bool { return s.Windows() == 1 })
}

// tmux-init sets its hooks at [20] only: running it again leaves no
// duplicates, and tmux-agent's [10] and tmux.conf's own [0] stay.
func TestTmuxInitOwnsIndex20(t *testing.T) {
	e := newEnv(t, "")
	e.mustTmux(t, "new-session", "-d", "-s", "proj-a")
	e.mustTmux(t, "set-hook", "-g", "session-created[10]", "display-message agent")
	e.mustTmux(t, "set-hook", "-g", "session-created[0]", "display-message mine")
	e.revive(t, "tmux-init")
	e.revive(t, "tmux-init")
	out := e.mustTmux(t, "show-hooks", "-g", "session-created")
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("session-created hooks:\n%s\nwant [0], [10] and [20]", out)
	}
	for i, idx := range []string{"[0]", "[10]", "[20]"} {
		if !strings.HasPrefix(lines[i], "session-created"+idx) {
			t.Errorf("hook %d = %q, want session-created%s", i, lines[i], idx)
		}
	}
	if !strings.Contains(lines[2], "tmux-revive save") {
		t.Errorf("[20] = %q, want tmux-revive save", lines[2])
	}
}

// writeSnapshot writes a snapshot of one session with one pane.
func (e *env) writeSnapshot(t *testing.T, sessions []snapshot.Session) {
	t.Helper()
	snap := &snapshot.Snapshot{Version: snapshot.Version, Time: time.Now().Add(-time.Minute), Sessions: sessions}
	if _, err := e.store.Write(snap, ""); err != nil {
		t.Fatal(err)
	}
}

func oneShell(name, id, dir, command string) snapshot.Session {
	return snapshot.Session{Name: name, Windows: []snapshot.Window{{
		ID: "w" + id, AutoRename: true, Width: 100, Height: 30, Active: true,
		Panes: []snapshot.Pane{{ID: "p" + id, Cwd: dir, Active: true, Command: command}},
	}}}
}

// slowShell starts like a zsh with a slow config: nothing on screen and no
// echo for a while, then it throws away whatever was typed, and only then
// runs a shell with a prompt. Keys sent too early are lost.
const slowShell = `#!/bin/bash
stty -echo
sleep 2.5
while read -r -t 0.2 -n 10000 _; do :; done
stty echo
exec /bin/sh
`

// A restore into a slow shell runs each resume command exactly once.
func TestSlowShellGetsTheCommandOnce(t *testing.T) {
	tmuxtest.Require(t, "bash")
	e := newEnv(t, "")
	shell := filepath.Join(e.Bin, "slowsh")
	if err := os.WriteFile(shell, []byte(slowShell), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(e.ConfigHome, "tmux", "tmux.conf")
	if err := os.WriteFile(conf, []byte(baseConfig+"set -g default-shell "+shell+"\n"+initLine), 0o600); err != nil {
		t.Fatal(err)
	}
	e.writeSnapshot(t, []snapshot.Session{
		oneShell("proj-a", "1", e.Home, "claude --resume "+sessionUUID),
		oneShell("proj-b", "2", e.Home, "claude --resume 99999999-2222-4333-8444-555555555555"),
	})
	out := e.revive(t, "restore")
	if !strings.Contains(out, "commands typed: 2") {
		t.Errorf("restore said:\n%s", out)
	}
	time.Sleep(time.Second) // a second run would show by now
	calls := e.Calls("claude")
	var ids []string
	for _, c := range calls {
		ids = append(ids, strings.Join(c.Args, " "))
	}
	slices.Sort(ids)
	want := []string{"--resume " + sessionUUID, "--resume 99999999-2222-4333-8444-555555555555"}
	if !slices.Equal(ids, want) {
		t.Errorf("claude ran with %q, want each resume once: %q", ids, want)
	}
}

// prefix X on the last session: forget-session, then kill-session ends
// the server, and the session does not come back.
func TestForgottenLastSessionStaysClosed(t *testing.T) {
	e := newEnv(t, "")
	e.start(t, "proj-a", e.Home)
	e.waitSnapshot(t, "the first save", func(s *snapshot.Snapshot) bool { return len(s.Sessions) == 1 })
	e.revive(t, "forget-session", "proj-a")
	e.mustTmux(t, "kill-session", "-t", "=proj-a")
	tmuxtest.WaitFor(t, 5*time.Second, "the server to exit", func() bool { return !e.serverRunning() })
	if snap, _ := e.latest(t); len(snap.Sessions) != 0 {
		t.Fatalf("snapshot still has %d sessions", len(snap.Sessions))
	}
	if out := e.revive(t, "restore"); !strings.Contains(out, "nothing to restore") {
		t.Errorf("restore said:\n%s", out)
	}
	if e.serverRunning() {
		t.Errorf("restore started a server with sessions %v", e.sessions(t))
	}
}

// prefix X with other sessions left: the session stays closed, the others
// come back after kill-server.
func TestForgottenSessionStaysClosed(t *testing.T) {
	e := newEnv(t, "")
	e.start(t, "proj-a", e.Home)
	e.mustTmux(t, "new-session", "-d", "-s", "proj-b")
	e.waitSnapshot(t, "both saved", func(s *snapshot.Snapshot) bool { return len(s.Sessions) == 2 })
	e.revive(t, "forget-session", "proj-b")
	e.mustTmux(t, "kill-session", "-t", "=proj-b")
	e.mustTmux(t, "kill-server")
	e.revive(t, "restore")
	if got := e.sessions(t); !slices.Equal(got, []string{"proj-a"}) {
		t.Errorf("sessions = %v, want [proj-a]", got)
	}
}

// Detaching the last client changes nothing in the snapshot.
func TestDetachKeepsEverything(t *testing.T) {
	e := newEnv(t, "")
	e.start(t, "proj-a", e.Home)
	e.mustTmux(t, "new-session", "-d", "-s", "proj-b")
	e.waitSnapshot(t, "both saved", func(s *snapshot.Snapshot) bool { return len(s.Sessions) == 2 })
	c := e.Terminal(t, "exec tmux attach-session -t =proj-a", 100, 30)
	tmuxtest.WaitFor(t, 5*time.Second, "the client", func() bool {
		out, _ := e.tmux.Output(context.Background(), "list-clients")
		return out != ""
	})
	c.Close()
	tmuxtest.WaitFor(t, 5*time.Second, "the client to go", func() bool {
		out, _ := e.tmux.Output(context.Background(), "list-clients")
		return out == ""
	})
	e.saveNow(t)
	if snap, _ := e.latest(t); len(snap.Sessions) != 2 || snap.Panes() != 2 {
		t.Errorf("after detach: %d sessions, %d panes; want 2 and 2", len(snap.Sessions), snap.Panes())
	}
}

// restore adds back only what the server lacks: a window closed since
// comes back from an older snapshot, windows the server has are left.
func TestRestoreAddsOnlyWhatIsMissing(t *testing.T) {
	e := newEnv(t, "")
	e.start(t, "proj-a", e.Home)
	e.mustTmux(t, "new-window", "-t", "=proj-a:")
	e.mustTmux(t, "rename-window", "-t", "=proj-a:1", "keep-me")
	e.waitSnapshot(t, "two windows saved", func(s *snapshot.Snapshot) bool {
		return strings.Contains(comparable(s), `"name": "keep-me"`)
	})
	es, err := e.store.Entries()
	if err != nil {
		t.Fatal(err)
	}
	withWindow := filepath.Base(es[len(es)-1].Path)
	e.mustTmux(t, "kill-window", "-t", "=proj-a:1")
	e.waitSnapshot(t, "the closed window dropped", func(s *snapshot.Snapshot) bool { return s.Windows() == 1 })

	if out := e.revive(t, "restore"); !strings.Contains(out, "nothing to restore") {
		t.Errorf("restore from the newest snapshot said:\n%s", out)
	}
	if out := e.revive(t, "restore", "--from", withWindow); !strings.Contains(out, "restored the snapshot") {
		t.Errorf("restore --from said:\n%s", out)
	}
	got := e.mustTmux(t, "list-windows", "-t", "=proj-a", "-F", "#{window_name}")
	if !strings.Contains(got, "keep-me") || strings.Count(got, "\n") != 1 {
		t.Errorf("windows after restore --from:\n%s\nwant the first and keep-me", got)
	}
}

func TestWaitIdleReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := revive.WaitIdle(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// A command with a newline in it is not typed: the shell would run its
// first line at once, before the check that the text arrived whole, and
// each retry would run it again.
func TestCommandWithNewlineIsNotTyped(t *testing.T) {
	e := newEnv(t, "")
	e.writeSnapshot(t, []snapshot.Session{oneShell("proj-a", "1", e.Home, "claude one\nclaude two")})
	out := e.revive(t, "restore")
	if !strings.Contains(out, "not typed") || !strings.Contains(out, "proj-a:0.0") {
		t.Errorf("restore said:\n%s", out)
	}
	time.Sleep(time.Second) // a run would show by now
	if c := e.Calls("claude"); len(c) != 0 {
		t.Errorf("claude ran %d times, want never: %v", len(c), c)
	}
}

// One window tmux refuses costs only that window: the windows after it
// and every split are still made.
func TestRestoreGoesOnPastARefusedWindow(t *testing.T) {
	e := newEnv(t, "")
	two := func(index int, id string) snapshot.Window {
		return snapshot.Window{Index: index, ID: "w" + id, AutoRename: true, Width: 100, Height: 30, Panes: []snapshot.Pane{
			{Index: 0, ID: "p" + id + "a", Cwd: e.Home, Active: true},
			{Index: 1, ID: "p" + id + "b", Cwd: e.Home},
		}}
	}
	refused := two(-1, "2") // tmux: "can't find window: -1"
	e.writeSnapshot(t, []snapshot.Session{{Name: "proj-a", Windows: []snapshot.Window{two(0, "1"), refused, two(2, "3")}}})
	out, err := e.Cmd(t, "tmux-revive", "restore").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "create windows") {
		t.Errorf("restore: err=%v, want the refused window reported:\n%s", err, out)
	}
	got := e.mustTmux(t, "list-panes", "-s", "-t", "=proj-a", "-F", "#{window_index} #{@revive_pane}")
	want := "0 p1a\n0 p1b\n2 p3a\n2 p3b"
	if got != want {
		t.Errorf("panes:\n%s\nwant:\n%s", got, want)
	}
}

// A restore whose typing runs past its deadline still marks the server
// ready: a server left unmarked never saves again.
func TestRestorePastItsDeadlineMarksReady(t *testing.T) {
	e := newEnv(t, "")
	sleeper := filepath.Join(e.Bin, "noprompt")
	if err := os.WriteFile(sleeper, []byte("#!/bin/sh\nexec sleep 1000\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(e.ConfigHome, "tmux", "tmux.conf")
	if err := os.WriteFile(conf, []byte(baseConfig+"set -g default-shell "+sleeper+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.writeSnapshot(t, []snapshot.Session{oneShell("proj-a", "1", e.Home, "claude --resume "+sessionUUID)})
	snap, en := e.latest(t)
	r := &revive.Reviver{Tmux: e.tmux, Store: e.store, LockDir: filepath.Join(e.RuntimeDir, "tmux-revive"),
		Stopping: func(context.Context) bool { return false }}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	rep, _ := r.Restore(ctx, snap, en)
	if len(rep.NotTyped) != 1 {
		t.Errorf("not typed = %v, want the one pane with no shell", rep.NotTyped)
	}
	if got := e.mustTmux(t, "show-options", "-gv", "@revive_ready"); got != "1" {
		t.Errorf("@revive_ready = %q, want 1", got)
	}
}

// A restored pane whose id a live pane already has (the pane moved to
// another window since) gets no id of its own: two panes with one id
// would write one scrollback file.
func TestRestoreKeepsPaneIDsUnique(t *testing.T) {
	e := newEnv(t, "")
	e.start(t, "proj-a", e.Home)
	e.mustTmux(t, "set-option", "-p", "-t", "=proj-a:0.0", "@revive_pane", "pdup")
	snap := &snapshot.Snapshot{Version: snapshot.Version, Time: time.Now().Add(-time.Hour),
		Sessions: []snapshot.Session{oneShell("proj-b", "dup", e.Home, "")}}
	en, err := e.store.Write(snap, "")
	if err != nil {
		t.Fatal(err)
	}
	e.revive(t, "restore", "--from", filepath.Base(en.Path))
	e.saveNow(t)
	got := strings.Split(e.mustTmux(t, "list-panes", "-a", "-F", "#{@revive_pane}"), "\n")
	slices.Sort(got)
	if len(got) != 2 || got[0] == got[1] || !slices.Contains(got, "pdup") {
		t.Errorf("pane ids = %q, want pdup and a new one", got)
	}
}

// A pane with nothing above its prompt has no scrollback: the prompt line
// is not saved, or a restore would show it twice.
func TestCaptureOfAFreshPaneIsEmpty(t *testing.T) {
	e := newEnv(t, "")
	e.start(t, "proj-a", e.Home)
	e.saveNow(t)
	id := e.mustTmux(t, "display-message", "-p", "-t", "=proj-a:0.0", "#{@revive_pane}")
	tmuxtest.WaitFor(t, 10*time.Second, "the prompt", func() bool {
		out, err := e.tmux.Output(context.Background(), "display-message", "-p", "-t", "=proj-a:0.0", "#{cursor_x}")
		return err == nil && out != "0"
	})
	e.revive(t, "capture")
	path, err := e.store.ScrollbackPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != 0 {
		t.Errorf("scrollback = %q, %v; want an empty file", b, err)
	}
}
