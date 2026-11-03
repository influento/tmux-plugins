package revive

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Timing of typing into restored shells. Shells in restored panes start
// together and can be slow: zsh echoes late, may drop what is typed while
// it starts, and a new pane reports "tmux" as its command until its shell
// runs.
const (
	typeTimeout  = 30 * time.Second       // per pane, all attempts
	typeAttempts = 4                      // tries before giving up
	typeParallel = 8                      // panes typed into at once
	promptQuiet  = 300 * time.Millisecond // a prompt is drawn and still
	anyQuiet     = 2 * time.Second        // still, but no prompt seen
	echoWait     = 2 * time.Second        // for typed text to show
	startWait    = 3 * time.Second        // for the command to start
	pollEvery    = 40 * time.Millisecond
)

var errNotTyped = errors.New("the shell never showed the typed command")

// typeAll types each task's command into its pane, several panes at once.
// It returns how many were typed, and the labels of those that were not.
func (r *Reviver) typeAll(ctx context.Context, tasks []typeTask, shell string) (typed int, failed []string) {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, typeParallel)
	)
	for _, t := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := r.typeInto(ctx, t.pane, t.text, shell)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, t.label)
				r.logf("not typed into %s (%s): %v", t.label, t.pane, err)
				return
			}
			typed++
		}()
	}
	wg.Wait()
	return typed, failed
}

// cursor is where the cursor stood at a shell prompt.
type cursor struct {
	x, y, history int
}

// typeInto types text into the pane's shell and runs it, exactly once.
// Enter is pressed only after the whole text shows on the prompt line, so
// a command whose text the shell dropped is typed again, never run twice.
func (r *Reviver) typeInto(ctx context.Context, pane, text, shell string) error {
	ctx, cancel := context.WithTimeout(ctx, typeTimeout)
	defer cancel()
	for attempt := 1; ; attempt++ {
		at, err := r.waitPrompt(ctx, pane, shell)
		if err != nil {
			return err
		}
		if _, err := r.Tmux.Run(ctx, "send-keys", "-t", pane, "-l", "--", text); err != nil {
			return err
		}
		if r.waitEcho(ctx, pane, at, text) {
			if _, err := r.Tmux.Run(ctx, "send-keys", "-t", pane, "Enter"); err != nil {
				return err
			}
			r.waitStart(ctx, pane, shell)
			return nil
		}
		// Clear whatever part arrived before typing again.
		if _, err := r.Tmux.Run(ctx, "send-keys", "-t", pane, "C-u"); err != nil {
			return err
		}
		if attempt == typeAttempts {
			return errNotTyped
		}
	}
}

// paneInfo reads the pane's process and cursor, and the visible screen,
// in one call.
func (r *Reviver) paneInfo(ctx context.Context, pane string) (pid int, at cursor, screen string, err error) {
	out, err := r.Tmux.Run(ctx,
		"display-message", "-p", "-t", pane, "#{pane_pid} #{cursor_x} #{cursor_y} #{history_size}", ";",
		"capture-pane", "-p", "-t", pane)
	if err != nil {
		return 0, cursor{}, "", err
	}
	head, screen, _ := strings.Cut(out, "\n")
	f := strings.Fields(head)
	if len(f) != 4 {
		return 0, cursor{}, "", errors.New("display-message: unexpected output")
	}
	return atoi(f[0]), cursor{x: atoi(f[1]), y: atoi(f[2]), history: atoi(f[3])}, screen, nil
}

// waitPrompt waits until a shell is in the pane's foreground and its
// screen has stopped changing with the cursor past column 0, as it is
// after a prompt is drawn; or, with no prompt to see, until the screen has
// been still for a while.
func (r *Reviver) waitPrompt(ctx context.Context, pane, shell string) (cursor, error) {
	var (
		last  string
		since time.Time
	)
	for {
		pid, at, screen, err := r.paneInfo(ctx, pane)
		if err != nil {
			return cursor{}, err
		}
		now := time.Now()
		key := fmt.Sprintf("%d,%d\x00%s", at.x, at.y, screen)
		switch {
		case !shellUp(pid, shell):
			last = ""
		case key != last:
			last, since = key, now
		case at.x > 0 && now.Sub(since) >= promptQuiet, now.Sub(since) >= anyQuiet:
			return at, nil
		}
		if err := sleep(ctx, pollEvery); err != nil {
			return cursor{}, err
		}
	}
}

// waitEcho waits for text to show between where the prompt left the
// cursor and where the cursor is now. Lines that scrolled into the history
// are followed through #{history_size}.
func (r *Reviver) waitEcho(ctx context.Context, pane string, at cursor, text string) bool {
	ctx, cancel := context.WithTimeout(ctx, echoWait)
	defer cancel()
	for {
		_, now, _, err := r.paneInfo(ctx, pane)
		if err != nil {
			return false
		}
		start := max(at.y-(now.history-at.history), -now.history)
		out, err := r.Tmux.Output(ctx, "capture-pane", "-p", "-J", "-t", pane,
			"-S", strconv.Itoa(start), "-E", strconv.Itoa(now.y))
		if err == nil && strings.Contains(strings.ReplaceAll(out, "\n", ""), text) {
			return true
		}
		if sleep(ctx, pollEvery) != nil {
			return false
		}
	}
}

// waitStart waits for the command to take the pane's foreground from the
// shell, so that the next save sees the program it started.
func (r *Reviver) waitStart(ctx context.Context, pane, shell string) {
	ctx, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	for {
		pid, _, _, err := r.paneInfo(ctx, pane)
		if err != nil || !shellUp(pid, shell) {
			return
		}
		if sleep(ctx, pollEvery) != nil {
			return
		}
	}
}
