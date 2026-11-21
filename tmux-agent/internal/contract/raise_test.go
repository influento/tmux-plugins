package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// swayStub is a sway with two Ghostty windows, in tree order: tmux's,
// titled by tmux's set-titles-string, then one that xdg-open started
// for nvim. As sway does, a criteria's values are unanchored regexes,
// focus runs on every match, and the last match keeps the focus: its id
// goes to $STUB_DIR/focused.
const swayStub = "#!/bin/sh\n" + tmuxtest.RecordCall + `crit="$*"
app=$(printf '%s' "$crit" | sed -n 's/.*app_id="\([^"]*\)".*/\1/p')
title=$(printf '%s' "$crit" | sed -n 's/.*title="\([^"]*\)".*/\1/p')
case "$crit" in *focus) ;; *) exit 0 ;; esac
printf '1\tcom.mitchellh.ghostty\ttmux\n2\tcom.mitchellh.ghostty\tGhostty\n' |
while IFS="$(printf '\t')" read -r id a t; do
  printf '%s' "$a" | grep -qE -- "$app" || continue
  [ -z "$title" ] || printf '%s' "$t" | grep -qE -- "$title" || continue
  echo "$id" > "$STUB_DIR/focused"
done
`

// A notification's click raises tmux's terminal, not another window of
// the same terminal program: the window whose title tmux sets.
func TestClickRaisesTmuxTerminal(t *testing.T) {
	e := newEnv(t)
	e.Stub(t, "swaymsg", swayStub)
	e.Setenv("SWAYSOCK", e.RuntimeDir+"/sway.sock")
	e.MustTmux(t, "set-option", "-g", "set-titles", "on")
	e.MustTmux(t, "set-option", "-g", "set-titles-string", "tmux")
	e.Attach(t, "proj-a", 100, 30)

	if _, stderr, code := e.tmuxAgent(t, "jump", "--pane", e.pane); code != 0 {
		t.Fatalf("jump: exit %d, %s", code, stderr)
	}
	got, err := os.ReadFile(filepath.Join(e.dir, "focused"))
	if err != nil {
		t.Fatalf("no window focused: %v; swaymsg calls %v", err, e.Calls("swaymsg"))
	}
	if strings.TrimSpace(string(got)) != "1" {
		t.Errorf("focus went to window %s, want tmux's window 1; swaymsg calls %v", strings.TrimSpace(string(got)), e.Calls("swaymsg"))
	}
}
