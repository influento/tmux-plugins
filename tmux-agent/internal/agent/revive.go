package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"time"

	"github.com/influento/tmux-plugins/internal/shell"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// uuidRe is the shape a session id must have before it goes into a
// command another program types into a shell.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ReviveCommand is what tmux-revive types into a restored pane's shell to
// resume the session: `tmux-agent revive`. ok is false when the agent
// cannot resume, or the id does not look like a UUID.
func ReviveCommand(a Agent, session string) (cmd string, ok bool) {
	if _, isResumer := a.(Resumer); !isResumer || !uuidRe.MatchString(session) {
		return "", false
	}
	return shell.Join([]string{"tmux-agent", "revive", a.Name(), session}), true
}

// Revive resumes session in the pane, as a restore types it, through the
// launcher. A restore names the window after the session's title; Revive
// marks that name as the agent's, so it goes when the run ends, however it
// ends. A session whose transcript is gone cannot resume: the window gets
// its automatic name back, the pane its shell, and nothing is started.
func (e *Env) Revive(a Agent, session string, self []string, stderr io.Writer) int {
	r, ok := a.(Resumer)
	if !ok || !uuidRe.MatchString(session) {
		fmt.Fprintf(stderr, "tmux-agent: %s cannot resume session %q\n", a.Name(), session)
		return 2
	}
	if e.Pane != "" && !InsideAgent(e.Registry.ProcessNames()) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		gone, err := e.prepareRevive(ctx, a, session)
		cancel()
		if err != nil {
			e.logf("revive %s: %v", session, err)
		}
		if gone {
			fmt.Fprintf(stderr, "tmux-agent: session %s has no transcript left; not resumed\n", session)
			return 0
		}
	}
	args := r.ResumeArgs(session)
	run := append([]string{self[0], "run", a.Name()}, args...)
	return e.Launch(a, args, run, stderr)
}

// prepareRevive gets the pane and its window ready for the resume, and
// reports whether the session is gone.
func (e *Env) prepareRevive(ctx context.Context, a Agent, session string) (gone bool, err error) {
	defer e.lock(ctx)()
	var rec store.Session
	if e.Store != nil {
		r, ok, err := e.Store.Load(a.Name(), session)
		if err != nil {
			return false, err
		}
		if ok {
			rec = r
		}
	}
	l, err := e.readLive(ctx)
	if err != nil {
		return false, err
	}
	ours := rec.Title != "" && l.windowName == rec.Title && !l.autoRename
	if rec.Transcript != "" {
		if _, err := os.Stat(rec.Transcript); errors.Is(err, fs.ErrNotExist) {
			if ours && (l.title == "" || l.title == rec.Title) {
				l.named, l.title = rec.Title, rec.Title
				if err := e.releaseTitle(ctx, e.Pane, l); err != nil {
					return true, err
				}
			}
			e.markEnded(a, session, EndExited, "")
			return true, e.paneOpts(ctx, map[string]string{OptRevive: ""})
		}
	}
	if ours && l.title == "" {
		_, err := e.Tmux.Run(ctx, "set-option", "-w", "-t", e.Pane, OptTitle, rec.Title, ";",
			"set-option", "-p", "-t", e.Pane, optNamed, rec.Title)
		return false, err
	}
	return false, nil
}
