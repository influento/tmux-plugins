package git

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestHeadOf(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "proj-a")
	gitIn(t, root, "init", "-q", "-b", "main", repo)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	wt := filepath.Join(root, "proj-a-wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feat-a", wt)
	ctx := context.Background()
	for _, c := range []struct {
		dir  string
		want Head
	}{
		{repo, Head{Branch: "main"}},
		{wt, Head{Branch: "feat-a", Worktree: true}},
	} {
		got, err := HeadOf(ctx, c.dir)
		if err != nil || got != c.want {
			t.Errorf("HeadOf(%s) = %+v, %v; want %+v", filepath.Base(c.dir), got, err, c.want)
		}
	}
	gitIn(t, repo, "checkout", "-q", "--detach")
	if got, err := HeadOf(ctx, repo); err != nil || !got.Detached || len(got.Branch) < 4 {
		t.Errorf("detached: %+v, %v", got, err)
	}
	for _, dir := range []string{root, "relative/path"} {
		if _, err := HeadOf(ctx, dir); !errors.Is(err, ErrNotARepo) {
			t.Errorf("HeadOf(%s) = %v, want ErrNotARepo", dir, err)
		}
	}
}
