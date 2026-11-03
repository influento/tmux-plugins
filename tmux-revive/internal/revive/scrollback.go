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
// keeps its last capture: the program's screen is not scrollback, and
// restore starts the program again anyway. Files of panes that are
// neither live nor in a kept snapshot go.
func (r *Reviver) Capture(ctx context.Context) error {
	lines := r.intOption(ctx, OptScrollbackLines, DefaultScrollbackLines)
	if lines == 0 {
		return nil
	}
	out, err := r.Tmux.Output(ctx, "list-panes", "-a", "-F", strings.Join([]string{
		"#{pane_id}", "#{" + OptPaneID + "}", "#{alternate_on}", "#{cursor_y}", "#{" + OptReady + "}",
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
		if len(f) != 5 || f[1] == "" {
			continue
		}
		if f[4] != "1" {
			return nil // not restored yet: its panes have no ids worth keeping
		}
		live[f[1]] = true
		if f[2] == "1" {
			continue
		}
		// Up to the line above the cursor: the cursor's line is the prompt,
		// and the restored shell draws a new one.
		text, err := r.Tmux.Output(ctx, "capture-pane", "-p", "-e", "-J", "-t", f[0],
			"-S", "-"+strconv.Itoa(lines), "-E", strconv.Itoa(atoi(f[3])-1))
		if err != nil {
			continue // the pane closed since list-panes
		}
		if _, err := r.Store.WriteScrollback(f[1], cleanScrollback(text)); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, r.Store.PruneScrollback(live))
	return errors.Join(errs...)
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
