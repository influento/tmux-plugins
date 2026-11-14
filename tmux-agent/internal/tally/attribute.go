package tally

import (
	"path/filepath"
	"strings"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// Most sessions are runs that other sessions start (claude -p in an eval
// or a workbench item). A run's parent is, first, the session its tally
// names (the hook read it from the process tree while both ran); else the
// session whose own folder holds the run's (Claude Code's scratchpad).
// A parent's parent is followed up to the first session with none.
//
// A session's folder is where it started, with two rules, since the
// folders are often gone by the time of a report: a worktree folder
// (.worktrees/<name>, .claude/worktrees/<name>) is its repo, and a folder
// below one where an interactive session started is that folder, the
// nearest one; the home folder is no project.

// Attribution tells whom a session's tokens count for.
type Attribution struct {
	// Owner names the session whose own folder holds the folder cwd of a
	// run of agent (agent.RunOwner), or "".
	Owner func(agent, cwd string) string
	// Home is the user's home folder.
	Home string
}

// worktreeDirs are the folders that hold a repo's worktrees.
var worktreeDirs = []string{"/.worktrees/", "/.claude/worktrees/"}

// repoOf is a worktree's repo, or the folder itself.
func repoOf(dir string) string {
	for _, w := range worktreeDirs {
		if i := strings.Index(dir, w); i > 0 {
			return dir[:i]
		}
	}
	return dir
}

// resolver attributes an agent's sessions.
type resolver struct {
	agent       string
	tallies     map[string]store.Usage
	interactive map[string]bool // the folders interactive sessions started in
	at          Attribution
}

func newResolver(agent string, tallies []store.Usage, at Attribution) *resolver {
	r := &resolver{agent: agent, tallies: map[string]store.Usage{}, interactive: map[string]bool{}, at: at}
	for _, u := range tallies {
		r.tallies[u.Session] = u
		if !u.OneShot && filepath.IsAbs(u.Cwd) {
			r.interactive[repoOf(filepath.Clean(u.Cwd))] = true
		}
	}
	return r
}

// parentOf is the session that started a session, or "".
func (r *resolver) parentOf(session string) string {
	u, ok := r.tallies[session]
	if !ok {
		return ""
	}
	if u.Parent != "" && (u.ParentAgent == "" || u.ParentAgent == r.agent) {
		return u.Parent
	}
	if r.at.Owner != nil {
		return r.at.Owner(r.agent, u.Cwd)
	}
	return ""
}

// top is the session a session's tokens count for: the first one up its
// parents that has none, itself when it has none.
func (r *resolver) top(session string) string {
	seen := map[string]bool{session: true}
	s := session
	for {
		p := r.parentOf(s)
		if p == "" || seen[p] {
			return s
		}
		seen[p] = true
		s = p
	}
}

// path is a session's folder, or "" when none is known.
func (r *resolver) path(session string) string {
	cwd := r.tallies[session].Cwd
	if !filepath.IsAbs(cwd) {
		return ""
	}
	dir := repoOf(filepath.Clean(cwd))
	for d := dir; d != "/" && d != r.at.Home; d = filepath.Dir(d) {
		if r.interactive[d] {
			return d
		}
	}
	return dir
}

// short writes a folder in the home folder with ~.
func (r *resolver) short(path string) string {
	switch {
	case path == "":
		return "?"
	case r.at.Home != "" && path == r.at.Home:
		return "~"
	case r.at.Home != "" && strings.HasPrefix(path, r.at.Home+"/"):
		return "~" + path[len(r.at.Home):]
	}
	return path
}
