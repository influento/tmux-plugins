package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// The pane under test prints these rows, then waits. The empty row and the
// short rows are what a flat offset got wrong: it assumed one cursor-right
// step per line break, which holds for neither mode-keys setting.
var jumpRows = []string{
	"alpha beta",
	"",
	"zeta foo one",
	"  foo two",
	"éx foo", // a combining mark shares the cell of its base letter
	"宽 foo",   // a wide rune takes two screen columns
}

const jumpScript = `printf '%b\n' 'alpha beta' '' 'zeta foo one' '  foo two' 'e\0314\0201x foo' '宽 foo'; exec cat`

// startJumpPane runs jumpScript in a private server and points this process's
// tmux calls at its pane.
func startJumpPane(t *testing.T, modeKeys string) *tmuxtest.Server {
	t.Helper()
	srv := tmuxtest.Start(t, tmuxtest.Options{
		Config:  tmuxtest.DefaultConfig + "set -g mode-keys " + modeKeys + "\n",
		Command: jumpScript,
		Width:   80, Height: 20,
	})
	srv.WaitForContent(t, "main", "宽 foo")
	srv.InPane(t, srv.Display(t, "main", "#{pane_id}"))
	return srv
}

// jumpTargets are (rune column, row, screen column) of each "foo" and of a
// few other cells; the screen column differs from the rune column only on the
// wide-rune row and the combining-mark row.
var jumpTargets = []struct{ x, y, col int }{
	{0, 0, 0},
	{6, 0, 6},
	{5, 2, 5},
	{2, 3, 2},
	{4, 4, 3},
	{2, 5, 3},
}

func TestJumpLandsOnTarget(t *testing.T) {
	for _, mode := range []string{"vi", "emacs"} {
		t.Run(mode, func(t *testing.T) {
			srv := startJumpPane(t, mode)
			for _, tc := range jumpTargets {
				ps, err := capturePaneState()
				if err != nil {
					t.Fatal(err)
				}
				content, err := captureContent(ps)
				if err != nil {
					t.Fatal(err)
				}
				if err := jumpToPosition(ps, content, tc.x, tc.y); err != nil {
					t.Fatal(err)
				}
				got := srv.Display(t, ps.PaneID, "#{pane_in_mode} #{copy_cursor_x},#{copy_cursor_y}")
				if want := fmt.Sprintf("1 %d,%d", tc.col, tc.y); got != want {
					t.Errorf("jump to rune %d of row %d %q: copy mode and cursor = %q, want %q",
						tc.x, tc.y, jumpRows[tc.y], got, want)
				}
				srv.MustTmux(t, "send-keys", "-X", "-t", ps.PaneID, "cancel")
			}
		})
	}
}

// A single match jumps without the overlay, so the whole search path runs
// without an attached client.
func TestSingleMatchJumps(t *testing.T) {
	srv := startJumpPane(t, "vi")
	srv.MustTmux(t, "set-option", "-g", optQuery, "two")
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if got := srv.Display(t, "main", "#{copy_cursor_x},#{copy_cursor_y}"); got != "6,3" {
		t.Errorf("cursor = %s, want 6,3 (the 't' of \"two\")", got)
	}
	if _, err := srv.Tmux("show-options", "-gv", optQuery); err == nil {
		t.Error("the query option is still set after the run")
	}
}

// buildWarp builds the binary next to a copy of the wrapper, as installs
// place them.
func buildWarp(t *testing.T) string {
	t.Helper()
	tmuxtest.Require(t, "go")
	dir := t.TempDir()
	bin := filepath.Join(dir, "tmux-warp")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	wrapper, err := os.ReadFile("tmux-warp.sh")
	if err != nil {
		t.Fatal(err)
	}
	sh := filepath.Join(dir, "tmux-warp.sh")
	if err := os.WriteFile(sh, wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	return sh
}

// The key binding works in the foreground and with run-shell -b on the tmux
// this repo targets: the query prompt, the labels and the label prompt all
// reach the client either way.
func TestBindingModes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	sh := buildWarp(t)
	for _, flag := range []string{"", "-b"} {
		name := "foreground"
		if flag != "" {
			name = "background"
		}
		t.Run(name, func(t *testing.T) {
			srv := tmuxtest.Start(t, tmuxtest.Options{Command: jumpScript, Width: 80, Height: 20})
			srv.WaitForContent(t, "main", "宽 foo")
			bind := []string{"bind-key", "s", "run-shell"}
			if flag != "" {
				bind = append(bind, flag)
			}
			srv.MustTmux(t, append(bind, sh)...)
			c := srv.Attach(t, "main", 80, 20)

			c.Type(t, "\x02s") // prefix (C-b), then s
			tmuxtest.WaitFor(t, 5*time.Second, "the query prompt", func() bool {
				return strings.Contains(c.Output(), "warp:")
			})
			c.Type(t, "foo\r")
			tmuxtest.WaitFor(t, 5*time.Second, "the label prompt", func() bool {
				return strings.Contains(c.Output(), "label:")
			})
			// Labels go to the nearest match first; the cursor sits on the
			// last row, so the first label is the bottom "foo".
			label := firstLabel(t, srv)
			c.Type(t, label)
			// The jump enters copy mode first and moves the cursor after,
			// so wait for the position, not for the mode.
			var got string
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				got, _ = srv.Tmux("display-message", "-p", "-t", "main",
					"#{pane_in_mode} #{copy_cursor_x},#{copy_cursor_y}")
				if got == "1 3,5" {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if got != "1 3,5" {
				t.Errorf("after label %q: copy mode and cursor = %q, want \"1 3,5\"", label, got)
			}
		})
	}
}

// firstLabel returns the label the overlay painted on the last "foo" row.
func firstLabel(t *testing.T, srv *tmuxtest.Server) string {
	t.Helper()
	var label string
	tmuxtest.WaitFor(t, 5*time.Second, "labels on the pane", func() bool {
		out, err := srv.Tmux("capture-pane", "-p", "-t", "main")
		if err != nil {
			return false
		}
		rows := strings.Split(out, "\n")
		if len(rows) < 6 || !strings.HasPrefix(rows[5], "宽 ") {
			return false
		}
		rest := []rune(strings.TrimPrefix(rows[5], "宽 "))
		if len(rest) == 0 || strings.HasPrefix(string(rest), "foo") {
			return false
		}
		label = string(rest[0])
		return true
	})
	return label
}

// altScript draws rows on the alternate screen, then puts its terminal in
// raw mode without output processing, as full-screen programs such as vim
// do, and waits.
const altScript = `printf '\033[?1049h\033[H'; printf 'one foo\r\ntwo foo\r\nthree'; stty raw -echo -opost; exec sleep 3600`

// Warp over a full-screen program puts that program's screen back as it was
// when no label is picked.
func TestAltScreenComesBack(t *testing.T) {
	sh := buildWarp(t)
	srv := tmuxtest.Start(t, tmuxtest.Options{Command: altScript, Width: 80, Height: 20})
	srv.WaitForContent(t, "main", "three")
	tmuxtest.WaitFor(t, 5*time.Second, "raw mode", func() bool {
		return srv.Display(t, "main", "#{pane_current_command}") == "sleep"
	})
	srv.MustTmux(t, "bind-key", "s", "run-shell", sh)
	c := srv.Attach(t, "main", 80, 20)
	// After the attach: the status line takes a row from the pane.
	before := srv.MustTmux(t, "capture-pane", "-p", "-t", "main")

	c.Type(t, "\x02s")
	tmuxtest.WaitFor(t, 5*time.Second, "the query prompt", func() bool {
		return strings.Contains(c.Output(), "warp:")
	})
	c.Type(t, "foo\r")
	tmuxtest.WaitFor(t, 5*time.Second, "the label prompt", func() bool {
		return strings.Contains(c.Output(), "label:")
	})
	c.Type(t, "x") // no label: two matches are labelled j and f

	var got string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got = srv.MustTmux(t, "capture-pane", "-p", "-t", "main"); got == before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("screen after warp:\n%q\nwant:\n%q", got, before)
}
