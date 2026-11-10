package agent

import (
	"context"
	"unicode/utf8"

	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/git"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// GitSource is the label source of a pane's branch and worktree.
const GitSource = "git"

// headOf reads a folder's checkout: Env.Git, else git.
func (e *Env) headOf(ctx context.Context, dir string) (git.Head, error) {
	if e.Git != nil {
		return e.Git(ctx, dir)
	}
	return git.HeadOf(ctx, dir)
}

// place keeps the pane's folder and its git label current: at a session
// start, at a turn end (the agent may have switched branches) and whenever
// the folder changes. The caller holds the lock. A folder git cannot read
// (outside a checkout, or git too slow) clears the label.
func (e *Env) place(ctx context.Context, a Agent, ev Event, l live, session string) error {
	if ev.Cwd == "" {
		return nil
	}
	moved := ev.Cwd != l.cwd
	if !moved && ev.Kind != SessionStart && ev.Kind != TurnEnd {
		return nil
	}
	if moved {
		if err := e.paneOpts(ctx, map[string]string{optCwd: ev.Cwd}); err != nil {
			return err
		}
		e.remember(a, session, func(s *store.Session) { s.Cwd = ev.Cwd })
	}
	label := attention.Label{Source: GitSource}
	if h, err := e.headOf(ctx, ev.Cwd); err == nil {
		label = gitLabel(h)
	}
	return e.Core.Report(ctx, e.Pane, label)
}

// gitLabel is how the panel shows a checkout: ⎇ and the branch, then wt
// for a linked worktree. Tokens give the same to tools.
func gitLabel(h git.Head) attention.Label {
	branch := h.Branch
	if utf8.RuneCountInString(branch) > 48 {
		branch = string([]rune(branch)[:47]) + "…"
	}
	l := attention.Label{Source: GitSource, Text: "⎇ " + branch, Tokens: map[string]string{"branch": branch}}
	if h.Detached {
		l.Tokens["detached"] = "1"
	}
	if h.Worktree {
		l.Text += " wt"
		l.Tokens["worktree"] = "1"
	}
	return l
}

// dropGit clears the pane's git label: its agent's session is over.
func (e *Env) dropGit(ctx context.Context, pane string) error {
	return e.Core.Report(ctx, pane, attention.Label{Source: GitSource})
}

// Report sets a label of pane under the lock, for a tool's report.
func (e *Env) Report(ctx context.Context, pane string, l attention.Label) error {
	defer e.lock(ctx)()
	return e.Core.Report(ctx, pane, l)
}
