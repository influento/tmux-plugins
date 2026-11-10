// Package git reads the facts tmux-agent shows about a folder's checkout:
// its branch, and whether it is a linked worktree. It is the one place
// that runs git.
package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Head is a checkout's current branch.
type Head struct {
	// Branch is the branch's short name; for a detached HEAD, the commit's
	// short hash.
	Branch   string
	Detached bool
	// Worktree is true for a linked worktree (git worktree add), false
	// for a repository's main checkout.
	Worktree bool
}

// Timeout bounds each git call.
const Timeout = time.Second

// ErrNotARepo is a folder outside any git checkout, or one git cannot read.
var ErrNotARepo = errors.New("git: not a checkout")

// HeadOf reads dir's checkout. dir must be absolute.
func HeadOf(ctx context.Context, dir string) (Head, error) {
	if !filepath.IsAbs(dir) {
		return Head{}, ErrNotARepo
	}
	out, err := run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--symbolic-full-name", "HEAD")
	if err != nil {
		return Head{}, err
	}
	f := strings.Split(out, "\n")
	if len(f) != 3 {
		return Head{}, ErrNotARepo
	}
	h := Head{Worktree: filepath.Clean(f[0]) != filepath.Clean(f[1])}
	if b, ok := strings.CutPrefix(f[2], "refs/heads/"); ok {
		h.Branch = b
		return h, nil
	}
	sha, err := run(ctx, dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return Head{}, err
	}
	h.Branch, h.Detached = sha, true
	return h, nil
}

// run runs git in dir, without the caller's GIT_* settings, which would
// point it at another repository, and without taking optional locks.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", errors.Join(ErrNotARepo, err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}
