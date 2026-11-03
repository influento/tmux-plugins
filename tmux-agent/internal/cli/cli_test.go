package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
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
// user's own, stay.
func TestTmuxInit(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	s.InPane(t, s.Display(t, "proj-a:0.0", "#{pane_id}"))
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
		if len(ours) != 1 || !strings.HasPrefix(ours[0], ev+"[10] run-shell -b ") {
			t.Errorf("%s hooks with tmux-agent = %q, want one at [10] that runs in the background", ev, ours)
		}
	}
	out := s.MustTmux(t, "show-hooks", "-g", "pane-focus-in")
	for _, want := range []string{"pane-focus-in[0] display-message mine", "pane-focus-in[20] display-message revive"} {
		if !strings.Contains(out, want) {
			t.Errorf("pane-focus-in hooks = %q, want %q kept", out, want)
		}
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
