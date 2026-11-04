package revive

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// Capture saves the scrollback of every pane that has an id (a save gives
// it one), one file per pane, replaced each time and kept out of the
// snapshots. A pane showing a full-screen program (the alternate screen)
// saves what the program covers, not the program's screen: that is not
// scrollback, and restore starts the program again anyway. Files of panes
// that are neither live nor in a kept snapshot go.
func (r *Reviver) Capture(ctx context.Context) error {
	lines := r.intOption(ctx, OptScrollbackLines, DefaultScrollbackLines)
	if lines == 0 {
		return nil
	}
	out, err := r.Tmux.Output(ctx, "list-panes", "-a", "-F", strings.Join([]string{
		"#{pane_id}", "#{" + OptPaneID + "}", "#{alternate_on}", "#{cursor_y}", "#{history_size}", "#{" + OptReady + "}",
		"#{alternate_saved_y}",
	}, sep))
	if err := noServer(err); errors.Is(err, errNoServer) {
		return nil
	} else if err != nil {
		return err
	}
	live := map[string]bool{}
	var errs []error
	for _, line := range outLines(out) {
		f := strings.Split(line, sep)
		if len(f) != 7 || f[1] == "" {
			continue
		}
		if f[5] != "1" {
			return nil // not restored yet: its panes have no ids worth keeping
		}
		live[f[1]] = true
		var text string
		var err error
		if f[2] == "1" {
			text, err = r.captureCovered(ctx, f[0], lines, atoi(f[4]), atoi(f[6]))
		} else if y := atoi(f[3]); y > 0 || atoi(f[4]) > 0 {
			// Up to the line above the cursor: the cursor's line is the
			// prompt, and the restored shell draws a new one. With the cursor
			// on the top line and no history there is nothing above it, and
			// tmux would read -E -1 as the top line itself.
			text, err = r.Tmux.Output(ctx, "capture-pane", "-p", "-e", "-J", "-t", f[0],
				"-S", "-"+strconv.Itoa(lines), "-E", strconv.Itoa(y-1))
		}
		if err != nil {
			continue // the pane closed, or left the alternate screen, since list-panes
		}
		if _, err := r.Store.WriteScrollback(f[1], cleanScrollback(text)); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, r.Store.PruneScrollback(live))
	return errors.Join(errs...)
}

// captureCovered captures what a full-screen program covers in pane, which
// is on the alternate screen: the history, then the screen as it was when
// the program started, up to the line above its cursor (savedY). The
// program draws its own screen again when it starts.
func (r *Reviver) captureCovered(ctx context.Context, pane string, lines, history, savedY int) (string, error) {
	var text string
	if history > 0 {
		h, err := r.Tmux.Output(ctx, "capture-pane", "-p", "-e", "-J", "-t", pane,
			"-S", "-"+strconv.Itoa(lines), "-E", "-1")
		if err != nil {
			return "", err
		}
		text = h + "\n"
	}
	if savedY > 0 {
		saved, err := r.Tmux.Output(ctx, "capture-pane", "-p", "-e", "-J", "-a", "-t", pane,
			"-S", "0", "-E", strconv.Itoa(savedY-1))
		if err != nil {
			return "", err
		}
		text += saved
	}
	return text, nil
}

// cleanScrollback drops trailing blanks on each line and the empty lines
// at the end.
func cleanScrollback(text string) []byte {
	ls := strings.Split(text, "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " ")
	}
	for len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	if len(ls) == 0 {
		return nil
	}
	return []byte(strings.Join(ls, "\n") + "\n")
}

// Paint prints a pane's saved scrollback, then resets the colours, so the
// shell that starts next draws its prompt below it. A missing file prints
// nothing.
func Paint(w io.Writer, path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\x1b[0m")
	return err
}
