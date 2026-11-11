package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
)

func run(t *testing.T, args ...string) (stdout string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Main("test", append([]string{"tmux-agent"}, args...), strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Logf("stderr: %s", errb.String())
	}
	return out.String(), code
}

// tmux-init sets one hook per event at [10], and running it again replaces
// them: no duplicates, and other indexes, such as tmux-revive's [20] and the
// user's own, stay. It also redraws the marks in the theme's colours, as
// after prefix r with new colours.
func TestTmuxInit(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	pane := s.Display(t, "proj-a:0.0", "#{pane_id}")
	s.InPane(t, pane)
	if _, code := run(t, "flag", "done", "--failed"); code != 0 {
		t.Fatalf("flag exit = %d", code)
	}
	s.MustTmux(t, "set-option", "-g", "@attention_fg_failed", "#aa0000")
	s.MustTmux(t, "set-hook", "-g", "pane-focus-in[0]", "display-message mine")
	s.MustTmux(t, "set-hook", "-g", "pane-focus-in[20]", "display-message revive")
	for range 2 {
		if _, code := run(t, "tmux-init"); code != 0 {
			t.Fatalf("tmux-init exit = %d", code)
		}
	}
	events := TmuxHookEvents()
	if len(events) == 0 {
		t.Fatal("no hook events")
	}
	for _, ev := range events {
		out := s.MustTmux(t, "show-hooks", "-g", ev)
		var ours []string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "tmux-agent") {
				ours = append(ours, l)
			}
		}
		inBackground := len(ours) == 1 && strings.Contains(ours[0], "run-shell -b ")
		if !inBackground || !(strings.HasPrefix(ours[0], ev+"[10] run-shell ") || strings.HasPrefix(ours[0], ev+"[10] if-shell -F ")) {
			t.Errorf("%s hooks with tmux-agent = %q, want one at [10] that runs in the background", ev, ours)
		}
	}
	out := s.MustTmux(t, "show-hooks", "-g", "pane-focus-in")
	for _, want := range []string{"pane-focus-in[0] display-message mine", "pane-focus-in[20] display-message revive"} {
		if !strings.Contains(out, want) {
			t.Errorf("pane-focus-in hooks = %q, want %q kept", out, want)
		}
	}
	marks := s.Display(t, pane, "#{@attention_mark}|#{@attention_win_mark}")
	if want := "#[fg=#aa0000]✗ |#[fg=#aa0000]✗ "; marks != want {
		t.Errorf("marks after tmux-init = %q, want %q", marks, want)
	}
}

// shell-init defines a function per agent, and per extra name, that goes
// through the launcher inside tmux and runs the program itself outside.
func TestShellInit(t *testing.T) {
	for _, shell := range []string{"zsh", "bash", "sh"} {
		for _, inTmux := range []bool{true, false} {
			name := shell + "/outside tmux"
			if inTmux {
				name = shell + "/inside tmux"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				tmuxtest.Require(t, shell)
				init, code := run(t, "shell-init", shell, "aider")
				if code != 0 {
					t.Fatalf("shell-init exit = %d", code)
				}
				sb := tmuxtest.NewSandbox(t)
				sb.Stub(t, "tmux-agent", tmuxtest.StubScript)
				sb.Stub(t, "aider", tmuxtest.StubScript)
				path := filepath.Join(sb.Dir, "init")
				if err := os.WriteFile(path, []byte(init), 0o600); err != nil {
					t.Fatal(err)
				}
				cmd := sb.Cmd(t, shell, "-c", `eval "$(cat "$1")"; claude a 'b c'; aider x`, shell, path)
				if inTmux {
					cmd.Env = append(cmd.Env, "TMUX=/nonexistent,1,0")
				}
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v\n%s", shell, err, out)
				}
				want := map[string][][]string{
					"tmux-agent": {{"run", "claude", "a", "b c"}, {"run", "aider", "x"}},
					"claude":     nil,
					"aider":      nil,
				}
				if !inTmux {
					want = map[string][][]string{
						"tmux-agent": nil,
						"claude":     {{"a", "b c"}},
						"aider":      {{"x"}},
					}
				}
				for prog, calls := range want {
					var got [][]string
					for _, c := range sb.Calls(prog) {
						got = append(got, c.Args)
					}
					if !slices.EqualFunc(got, calls, slices.Equal) {
						t.Errorf("%s calls = %q, want %q", prog, got, calls)
					}
				}
			})
		}
	}
}

func TestShellInitRejects(t *testing.T) {
	for _, args := range [][]string{
		{"shell-init"},
		{"shell-init", "fish"},
		{"shell-init", "zsh", "not a name"},
		{"shell-init", "zsh", "x;y"},
	} {
		if out, code := run(t, args...); code == 0 || out != "" {
			t.Errorf("%q = exit %d, output %q; want a failure and no functions", args, code, out)
		}
	}
}

// report sets a source's label on the pane, list --json shows it, and
// --clear removes it; a report with neither a label nor --clear, or with
// a bad source, fails.
func TestReport(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	pane := s.Display(t, "proj-a:0.0", "#{pane_id}")
	s.InPane(t, pane)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // the lock
	if _, code := run(t, "report", "--source", "tool-a", "--text", "review 2", "--token", "item=42", "--ttl", "1h"); code != 0 {
		t.Fatalf("report exit = %d", code)
	}
	out, code := run(t, "list", "--json")
	if code != 0 || !strings.Contains(out, `"source": "tool-a"`) || !strings.Contains(out, `"text": "review 2"`) || !strings.Contains(out, `"item": "42"`) {
		t.Errorf("list --json = %s", out)
	}
	if _, code := run(t, "report", "--source", "tool-a", "--clear", "--pane", pane); code != 0 {
		t.Fatalf("clear exit = %d", code)
	}
	if got := s.Display(t, pane, "#{@labels}"); got != "" {
		t.Errorf("@labels after --clear = %q", got)
	}
	for _, args := range [][]string{
		{"report", "--source", "tool-a"},
		{"report", "--source", "tool-a", "--text", "x", "--clear"},
		{"report", "--text", "x"},
		{"report", "--source", "tool-a", "--token", "novalue"},
	} {
		if _, code := run(t, args...); code != 2 {
			t.Errorf("%q exit = %d, want 2", args, code)
		}
	}
	if _, code := run(t, "report", "--source", "Tool A", "--text", "x"); code != 1 {
		t.Errorf("a bad source exit = %d, want 1", code)
	}
}

// the window's agent pane; else on its session's fold.
func TestStartRow(t *testing.T) {
	rows := []attention.PickRow{
		{Label: "proj-a", Session: "proj-a", Target: "=proj-a:"},
		{Session: "proj-a", Target: "=proj-a:1.1"},
		{Session: "proj-a", Target: "=proj-a:", Fold: "$1"},
		{Label: "proj-b", Session: "proj-b", Target: "=proj-b:"},
		{Session: "proj-b", Target: "=proj-b:10"},
	}
	for _, c := range []struct {
		window string
		want   int
	}{
		{"=proj-a:1", 2},
		{"=proj-a:3", 3},
		{"=proj-b:10", 5},
		{"=proj-b:1", 2},
		{"=host-c:0", 2},
	} {
		if got := startRow(rows, c.window); got != c.want {
			t.Errorf("startRow(%s) = %d, want %d", c.window, got, c.want)
		}
	}
}
