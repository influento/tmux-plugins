package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// The launcher starts an agent under its window's name. A window tmux
// names takes `<folder>-<agent>`, a second one there gets a number; a
// window the user named gives its name. A rename of the window renames
// the running agent, typed into its input box once it is empty; a name
// another window has gets a number, on the window too. It waits for a
// pause in the user's keys in the pane, and a rename a draft held is
// tried again when the user leaves the pane. A run's end gives
// a window that took the agent's name back to tmux. An agent with no name
// of its own gets none, and its window keeps tmux's name.
func TestRunNamesTheAgent(t *testing.T) {
	e := newEnv(t)
	e.MustTmux(t, "set-option", "-g", "focus-events", "on")
	if _, stderr, code := e.tmuxAgent(t, "tmux-init"); code != 0 {
		t.Fatalf("tmux-init: exit %d, %s", code, stderr)
	}
	proj := filepath.Join(e.Home, "proj-b")
	if err := os.Mkdir(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	sessions := 0
	var startIn func(dir, tag, window, agent string, split ...string) string
	// start runs agent in a new window, named window unless that is "",
	// or split into the window of pane split[0], with split-window's
	// flags split[1:].
	start := func(tag, window, agent string, split ...string) string {
		t.Helper()
		return startIn(proj, tag, window, agent, split...)
	}
	startIn = func(dir, tag, window, agent string, split ...string) string {
		t.Helper()
		args := []string{"new-window", "-d", "-t", "proj-a:", "-c", dir, "-P", "-F", "#{pane_id}"}
		if window != "" {
			args = append(args, "-n", window)
		}
		if len(split) > 0 {
			args = append([]string{"split-window", "-d", "-t", split[0], "-c", dir, "-P", "-F", "#{pane_id}"}, split[1:]...)
		}
		pane := e.MustTmux(t, args...)
		e.WaitForContent(t, pane, "$")
		sessions++
		session := fmt.Sprintf("11111111-2222-4333-8444-%012d", sessions)
		e.MustTmux(t, "send-keys", "-t", pane, "-l", "STUB_MODE=named STUB_TAG="+tag+" STUB_SESSION="+session+" tmux-agent run "+agent)
		e.MustTmux(t, "send-keys", "-t", pane, "Enter")
		e.file(t, tag+".pid")
		return pane
	}
	name := func(pane string) string { return e.Display(t, pane, "#{window_name}") }
	wantName := func(tag, pane, want string) {
		t.Helper()
		if args := e.args(t, tag); !slices.Equal(args, []string{"--name", want}) {
			t.Errorf("%s started with %q, want --name %s", tag, args, want)
		}
		if got := name(pane); got != want {
			t.Errorf("%s's window = %q, want %q", tag, got, want)
		}
	}
	renames := func(tag string) string {
		b, _ := os.ReadFile(filepath.Join(e.dir, tag+".renames"))
		return string(b)
	}
	waitRenames := func(tag, want string) {
		t.Helper()
		tmuxtest.WaitFor(t, 10*time.Second, tag+" renamed to "+want, func() bool { return renames(tag) == want })
	}
	// typeLine types a line into the stub's input and waits for its box.
	typeLine := func(tag, pane, line string) {
		t.Helper()
		drawn := filepath.Join(e.dir, tag+".drawn")
		if err := os.Remove(drawn); err != nil {
			t.Fatal(err)
		}
		e.MustTmux(t, "send-keys", "-t", pane, "-l", line)
		e.MustTmux(t, "send-keys", "-t", pane, "Enter")
		tmuxtest.WaitFor(t, 10*time.Second, tag+"'s box", func() bool {
			_, err := os.Stat(drawn)
			return err == nil
		})
	}
	// look writes the agent's transcript, which its watcher reads, and
	// waits for the context share it shows: the watcher looked.
	calls := 0
	look := func(tag, pane string) {
		t.Helper()
		calls++
		before := e.paneOpt(t, pane, "@agent_context")
		f, err := os.OpenFile(filepath.Join(e.dir, tag+".transcript"), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(f, `{"type":"assistant","isSidechain":false,"message":{"id":"msg_%d","usage":{"input_tokens":%d,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`+"\n", calls, 10_000*calls)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
		tmuxtest.WaitFor(t, 10*time.Second, tag+"'s watcher to look", func() bool {
			got := e.paneOpt(t, pane, "@agent_context")
			return got != "" && got != before
		})
	}

	a := start("agent-a", "", "claude")
	wantName("agent-a", a, "proj-b-claude")
	b := start("agent-b", "", "claude")
	wantName("agent-b", b, "proj-b-claude-2")
	c := start("agent-c", "ui", "claude")
	wantName("agent-c", c, "ui")

	t.Run("a rename renames the agent", func(t *testing.T) {
		e.MustTmux(t, "rename-window", "-t", a, "docs")
		waitRenames("agent-a", "docs\n")
	})
	t.Run("a name another window has gets a number", func(t *testing.T) {
		e.MustTmux(t, "rename-window", "-t", b, "ui")
		waitRenames("agent-b", "ui-2\n")
		if got := name(b); got != "ui-2" {
			t.Errorf("window = %q, want ui-2", got)
		}
		if got := name(c); got != "ui" {
			t.Errorf("the other window = %q, want ui", got)
		}
	})
	t.Run("a draft holds the rename", func(t *testing.T) {
		typeLine("agent-a", a, "draft hello")
		e.MustTmux(t, "rename-window", "-t", a, "notes")
		tmuxtest.WaitFor(t, 10*time.Second, "the pane's new name", func() bool { return e.paneOpt(t, a, "@agent_name") == "notes" })
		look("agent-a", a)
		if got := renames("agent-a"); got != "docs\n" {
			t.Fatalf("renames over a draft: %q, want docs only", got)
		}
		typeLine("agent-a", a, "clear")
		look("agent-a", a)
		waitRenames("agent-a", "docs\nnotes\n")
	})
	t.Run("an agent split into an agent's window gets a number", func(t *testing.T) {
		s := start("agent-s", "", "claude", a)
		if args := e.args(t, "agent-s"); !slices.Equal(args, []string{"--name", "notes-2"}) {
			t.Errorf("agent-s started with %q, want --name notes-2", args)
		}
		if got := name(a); got != "notes" {
			t.Errorf("window = %q, want notes", got)
		}
		e.MustTmux(t, "rename-window", "-t", a, "plan")
		waitRenames("agent-a", "docs\nnotes\nplan\n")
		waitRenames("agent-s", "plan-2\n")
		if got := e.paneOpt(t, s, "@agent_name"); got != "plan-2" {
			t.Errorf("@agent_name = %q, want plan-2", got)
		}
	})
	t.Run("start refuses a name an agent has", func(t *testing.T) {
		// agent-s, split into agent-a's window, is plan-2; no window is.
		if got := e.paneOpt(t, e.MustTmux(t, "list-panes", "-a", "-f", "#{==:#{@agent_name},plan-2}", "-F", "#{pane_id}"), "@agent_name"); got != "plan-2" {
			t.Fatalf("fixture: no agent named plan-2")
		}
		out, stderr, code := e.tmuxAgent(t, "start", "--name", "plan-2", "--json", "claude")
		if code != 3 {
			t.Errorf("start --name plan-2: exit %d, %s%s; want 3, the name is in use", code, out, stderr)
		}
	})
	t.Run("a rename keeps the window's name on the agent that holds it", func(t *testing.T) {
		h := start("agent-h", "fold", "claude")
		wantName("agent-h", h, "fold")
		i := start("agent-i", "", "claude", h, "-b") // the window's first pane now
		if args := e.args(t, "agent-i"); !slices.Equal(args, []string{"--name", "fold-2"}) {
			t.Fatalf("agent-i started with %q, want --name fold-2", args)
		}
		win := e.Display(t, h, "#{window_id}")
		names := func() string {
			return name(h) + " " + e.paneOpt(t, h, "@agent_name") + " " + e.paneOpt(t, i, "@agent_name")
		}
		// renamed runs as the hook does, and is done when it returns.
		renamed := func(to string) {
			t.Helper()
			e.MustTmux(t, "rename-window", "-t", win, to)
			if _, stderr, code := e.tmuxAgent(t, "renamed", win); code != 0 {
				t.Fatalf("renamed: exit %d, %s", code, stderr)
			}
		}
		renamed("fold") // the same name: nothing changes
		if got := names(); got != "fold fold fold-2" {
			t.Fatalf("window, agent-h, agent-i = %q after a rename to the same name, want fold fold fold-2", got)
		}
		renamed("gold")
		if got := names(); got != "gold gold gold-2" {
			t.Fatalf("window, agent-h, agent-i = %q, want gold gold gold-2", got)
		}
		waitRenames("agent-h", "gold\n")
		waitRenames("agent-i", "gold-2\n")
	})
	t.Run("names tmux would read as a flag or a format", func(t *testing.T) {
		m := start("agent-m", "plain", "claude")
		win := e.Display(t, m, "#{window_id}")
		renamed := func(to string) {
			t.Helper()
			e.MustTmux(t, "rename-window", "-t", win, "--", to) // as prefix , does
			if _, stderr, code := e.tmuxAgent(t, "renamed", win); code != 0 {
				t.Fatalf("renamed: exit %d, %s", code, stderr)
			}
		}
		for _, c := range []struct{ taken, rename, want string }{
			{"-x", "-x", "-x-2"},
			{"c#S", "c##S", "c#S-2"}, // ## is a literal #
		} {
			other := e.MustTmux(t, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "proj-a:")
			e.MustTmux(t, "rename-window", "-t", other, "--", c.rename)
			if got := e.Display(t, other, "#{window_name}"); got != c.taken {
				t.Fatalf("fixture: window %q, want %q", got, c.taken)
			}
			renamed(c.rename)
			if got := name(m) + " " + e.paneOpt(t, m, "@agent_name"); got != c.want+" "+c.want {
				t.Errorf("window, agent = %q after a rename to %q, want %s", got, c.taken, c.want)
			}
		}
		renamed("")
		if got := e.paneOpt(t, m, "@agent_name"); got != "c#S-2" {
			t.Errorf("agent = %q after a rename to nothing, want its name kept", got)
		}
	})
	t.Run("the end gives the window back", func(t *testing.T) {
		d := start("agent-d", "", "claude")
		wantName("agent-d", d, "proj-b-claude") // agent-a's name went with its rename
		e.MustTmux(t, "send-keys", "-t", d, "C-d")
		tmuxtest.WaitFor(t, 10*time.Second, "tmux's name back", func() bool {
			return e.Display(t, d, "#{automatic-rename}") == "1" && name(d) != "proj-b-claude"
		})
		e.MustTmux(t, "send-keys", "-t", c, "C-d")
		tmuxtest.WaitFor(t, 10*time.Second, "agent-c's run to end", func() bool { return e.paneOpt(t, c, "@agent_run") == "" })
		if got := name(c); got != "ui" {
			t.Errorf("a window the user named = %q after the run, want ui", got)
		}
	})
	t.Run("in git, the repository's name", func(t *testing.T) {
		tmuxtest.Require(t, "git")
		repo := filepath.Join(e.Home, "repo-c")
		for _, args := range [][]string{
			{"init", "-q", repo},
			{"-C", repo, "-c", "user.name=test", "-c", "user.email=test@test.invalid", "commit", "-q", "--allow-empty", "-m", "first"},
		} {
			if out, err := e.Cmd(t, "git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %q: %v: %s", args, err, out)
			}
		}
		sub := filepath.Join(repo, "sub")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		g := startIn(sub, "agent-g", "", "claude")
		wantName("agent-g", g, "repo-c-claude")
	})
	t.Run("a rename waits for a pause in the user's keys", func(t *testing.T) {
		k := start("agent-k", "keys", "claude")
		e.MustTmux(t, "select-window", "-t", k)
		client := e.Attach(t, "proj-a", 120, 40)
		keyAt := time.Now()     // the key reaches tmux after this
		client.Type(t, "x\x7f") // a key, and its erase: the box stays empty
		e.MustTmux(t, "rename-window", "-t", k, "keys-b")
		waitRenames("agent-k", "keys-b\n")
		at, err := strconv.ParseInt(e.file(t, "agent-k.renamed_at"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if gap := time.UnixMilli(at).Sub(keyAt); gap < time.Second {
			t.Errorf("typed %v after the user's key, want a pause of 1s or more", gap)
		}

		typeLine("agent-k", k, "draft hello")
		e.MustTmux(t, "rename-window", "-t", k, "keys-c")
		tmuxtest.WaitFor(t, 10*time.Second, "the pane's new name", func() bool { return e.paneOpt(t, k, "@agent_name") == "keys-c" })
		look("agent-k", k)
		typeLine("agent-k", k, "clear")
		if got := renames("agent-k"); got != "keys-b\n" {
			t.Fatalf("renames over a draft: %q", got)
		}
		e.MustTmux(t, "select-window", "-t", a)
		waitRenames("agent-k", "keys-b\nkeys-c\n")
	})
	t.Run("an agent with no name of its own", func(t *testing.T) {
		f := start("agent-f", "", "fakeagent")
		if args := e.args(t, "agent-f"); slices.Contains(args, "--name") {
			t.Errorf("fakeagent started with %q, want no name", args)
		}
		if got := e.Display(t, f, "#{automatic-rename}"); got != "1" {
			t.Errorf("automatic-rename = %q, want 1", got)
		}
	})
}
