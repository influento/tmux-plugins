package tally

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// Sessions of the attribution store. The parent started run1 and run2 in
// its scratchpad, and run1 started run3 in its own; loose ran in a
// worktree of /home/u/proj-a, deep below a folder where the parent
// started, and homeRun in a folder of the home folder.
const (
	parent  = "p0000000-0000-4000-8000-000000000000"
	run1    = "r1000000-0000-4000-8000-000000000000"
	run2    = "r2000000-0000-4000-8000-000000000000"
	run3    = "r3000000-0000-4000-8000-000000000000"
	loose   = "l0000000-0000-4000-8000-000000000000"
	deep    = "d0000000-0000-4000-8000-000000000000"
	homeRun = "h0000000-0000-4000-8000-000000000000"
	gone    = "g0000000-0000-4000-8000-000000000000" // its tally is gone
	orphan  = "o0000000-0000-4000-8000-000000000000"
)

// owner reads the owner from a folder named /pad/<session>/...: a stand-in
// for an agent's scratchpad.
func owner(_, cwd string) string {
	rest, ok := strings.CutPrefix(cwd, "/pad/")
	if !ok {
		return ""
	}
	s, _, _ := strings.Cut(rest, "/")
	return s
}

func attributionEntries(t *testing.T) []Entry {
	t.Helper()
	st := &store.Store{Dir: t.TempDir()}
	at := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	for i, u := range []store.Usage{
		{Session: parent, Cwd: "/home/u/proj-a", Title: "plan"},
		{Session: run1, Cwd: "/pad/" + parent + "/runs/1", Title: "work the item", OneShot: true},
		{Session: run2, Cwd: "/pad/" + parent + "/runs/2/.worktrees/b-item", OneShot: true},
		{Session: run3, Cwd: "/pad/" + run1 + "/x", OneShot: true},
		{Session: loose, Cwd: "/home/u/proj-a/.worktrees/f-item", OneShot: true},
		{Session: deep, Cwd: "/home/u/proj-a/work/t1/sub", OneShot: true},
		{Session: homeRun, Cwd: "/home/u/scratch-x", OneShot: true},
		{Session: "z0000000-0000-4000-8000-000000000000", Cwd: "/home/u"}, // interactive in the home folder
		{Session: orphan, Cwd: "/pad/" + gone + "/y", OneShot: true},
	} {
		u.Agent = "fakeagent"
		u.Add(at, "model-a", store.Tokens{Output: int64(1) << i})
		if err := st.SaveUsage(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpdateSubagent("fakeagent", run1, "sub1", func(s *store.Subagent) {
		s.Type, s.Started, s.Tokens = "explore", at.Unix(), &store.Tokens{Output: 1000}
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := Entries(st, []string{"fakeagent"}, Attribution{Owner: owner, Home: "/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// rows groups the attribution store's entries and maps each row's key to
// it.
func rows(t *testing.T, o Options) (map[string]Row, Report) {
	t.Helper()
	o.Sort, o.Loc = cmp.Or(o.Sort, "output"), time.UTC
	r, err := Group(attributionEntries(t), o)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Row{}
	for _, row := range r.Rows {
		m[row.Key] = row
	}
	return m, r
}

func TestAttributionByPath(t *testing.T) {
	got, r := rows(t, Options{By: "path"})
	// Its own work: the parent, and the worktree and deep sessions, which
	// no session started (1+16+32). Under it, by folder, the runs the
	// parent started, run3 through run1, with run1's subagent.
	a := got["~/proj-a"]
	if a.Output != 49 || a.Runs != 3 || a.WithRuns == nil || a.WithRuns.Output != 49+2+4+8+1000 || a.WithRuns.Subagents != 1 {
		t.Errorf("~/proj-a = %+v, with runs %+v", a, a.WithRuns)
	}
	var children []string
	for _, c := range a.Children {
		children = append(children, c.Key)
	}
	want := []string{"/pad/" + parent + "/runs/1", "/pad/" + run1 + "/x", "/pad/" + parent + "/runs/2"}
	if !slices.Equal(children, want) {
		t.Errorf("children = %q, want %q", children, want)
	}
	// The home folder is no project: a session there keeps its own folder.
	if h := got["~/scratch-x"]; h.Output != 64 || h.WithRuns != nil {
		t.Errorf("~/scratch-x = %+v", h)
	}
	// A run whose parent's tally is gone counts for that parent, in the
	// run's own folder.
	if o := got["/pad/"+gone+"/y"]; o.Output != 0 || o.WithRuns == nil || o.WithRuns.Output != 256 || o.Runs != 1 {
		t.Errorf("orphan = %+v (rows %q)", o, keys(r.Rows))
	}
	if r.Total.Runs != 4 || r.Total.Output != 511+1000 {
		t.Errorf("total = %+v", r.Total)
	}
}

func TestAttributionBySession(t *testing.T) {
	got, _ := rows(t, Options{By: "session"})
	p := got["p0000000 proj-a · plan"]
	if p.Output != 1 || p.Runs != 3 || p.Path != "/home/u/proj-a" || p.WithRuns.Output != 1+2+4+8+1000 {
		t.Fatalf("parent = %+v (rows %v)", p, slices.Collect(maps.Keys(got)))
	}
	var children []string
	for _, c := range p.Children {
		children = append(children, c.Key)
	}
	// run1 with its subagent, then run2 and run3, which share no title,
	// in one row.
	if want := []string{"run r1000000 work the item", "2 runs · untitled"}; !slices.Equal(children, want) {
		t.Errorf("children = %q, want %q", children, want)
	}
	if c := p.Children[0]; c.Output != 1002 || c.Subagents != 1 || c.Session != run1 {
		t.Errorf("run1 = %+v", c)
	}
	if c := p.Children[1]; c.Output != 4+8 || c.Runs != 2 || !slices.Equal(c.Sessions, []string{run2, run3}) || c.Session != "" {
		t.Errorf("untitled runs = %+v", c)
	}
}

func TestFlat(t *testing.T) {
	got, r := rows(t, Options{By: "session", Flat: true})
	if p := got["p0000000 proj-a · plan"]; p.Output != 1 || p.Children != nil || p.WithRuns != nil {
		t.Errorf("parent = %+v", p)
	}
	if c := got["r1000000 1 · work the item"]; c.Output != 1002 {
		t.Errorf("run1 = %+v (rows %q)", c, keys(r.Rows))
	}
	paths, _ := rows(t, Options{By: "path", Flat: true, Path: "/pad"})
	var sum int64
	for _, row := range paths {
		sum += row.Output
	}
	if sum != 2+1000+4+8+256 {
		t.Errorf("flat paths under /pad: output %d", sum)
	}
}

// A parent the process tree told comes before the folder's.
func TestParentFromProcessTree(t *testing.T) {
	st := &store.Store{Dir: t.TempDir()}
	at := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	for _, u := range []store.Usage{
		{Session: parent, Cwd: "/home/u/proj-a"},
		{Session: "other000-0000-4000-8000-000000000000", Cwd: "/home/u/proj-b"},
		{Session: run1, Cwd: "/srv/eval/c1", OneShot: true, Parent: parent, ParentAgent: "fakeagent"},
		{Session: run2, Cwd: "/pad/other000-0000-4000-8000-000000000000/x", OneShot: true, Parent: parent},
		{Session: run3, Cwd: "/srv/eval/c2", OneShot: true, Parent: parent, ParentAgent: "otheragent"},
	} {
		u.Agent = "fakeagent"
		u.Add(at, "model-a", store.Tokens{Output: 1})
		if err := st.SaveUsage(u); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := Entries(st, []string{"fakeagent"}, Attribution{Owner: owner, Home: "/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Group(entries, Options{By: "path", Sort: "total", Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range r.Rows {
		// run1 and run2 under the parent; run3's parent is of another
		// agent, which this agent's report does not follow.
		if row.Key == "~/proj-a" && row.Runs != 2 {
			t.Errorf("~/proj-a runs = %d, want 2", row.Runs)
		}
	}
	if !slices.Contains(keys(r.Rows), "/srv/eval/c2") {
		t.Errorf("rows = %q, want run3 on its own", keys(r.Rows))
	}
}

func TestPathFilter(t *testing.T) {
	entries := attributionEntries(t)
	for _, c := range []struct {
		path string
		want int64
	}{
		{"/home/u/proj-a", 1063},
		{"/home/u/proj-a/", 1063},
		{"/home/u/proj", 0}, // a prefix of a name is no folder above it
		{"/home/u", 1063 + 64 + 128},
	} {
		r, err := Group(entries, Options{By: "agent", Sort: "total", Loc: time.UTC, Path: c.path})
		if err != nil {
			t.Fatal(err)
		}
		if r.Total.Output != c.want {
			t.Errorf("--path %s: output %d, want %d", c.path, r.Total.Output, c.want)
		}
	}
}

func TestRepoOf(t *testing.T) {
	for in, want := range map[string]string{
		"/w/proj-a/.worktrees/f-x":        "/w/proj-a",
		"/w/proj-a/.claude/worktrees/x/y": "/w/proj-a",
		"/w/proj-a/sub":                   "/w/proj-a/sub",
	} {
		if got := repoOf(in); got != want {
			t.Errorf("repoOf(%q) = %q, want %q", in, got, want)
		}
	}
}
