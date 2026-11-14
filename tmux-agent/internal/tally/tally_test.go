package tally

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

const (
	sessA = "aaaaaaaa-1111-4222-8333-444444444444"
	sessB = "bbbbbbbb-1111-4222-8333-444444444444"
)

// testStore holds two sessions: proj-a's, with calls on two days and a
// subagent, and proj-b's, with one call.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	st := &store.Store{Dir: t.TempDir()}
	a := store.Usage{Agent: "fakeagent", Session: sessA, Cwd: "/work/proj-a", Title: "fix the build"}
	a.Add(time.Date(2026, 1, 5, 21, 30, 0, 0, time.UTC), "model-a", store.Tokens{Input: 1, CacheWrite: 9, CacheRead: 1000, Output: 50})
	a.Add(time.Date(2026, 1, 5, 21, 45, 0, 0, time.UTC), "model-a", store.Tokens{Input: 1, CacheWrite: 9, CacheRead: 1000, Output: 50})
	a.Add(time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC), "model-b", store.Tokens{Input: 5, CacheRead: 10, Output: 5})
	b := store.Usage{Agent: "fakeagent", Session: sessB, Cwd: "/work/proj-b"}
	b.Add(time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC), "model-a", store.Tokens{Input: 100, CacheRead: 1, Output: 1})
	for _, u := range []store.Usage{a, b} {
		if err := st.SaveUsage(u); err != nil {
			t.Fatal(err)
		}
	}
	sub := func(id, typ string, tok *store.Tokens) {
		if err := st.UpdateSubagent("fakeagent", sessA, id, func(s *store.Subagent) {
			s.Type, s.Model, s.Started, s.Tokens = typ, "model-b", time.Date(2026, 1, 6, 9, 5, 0, 0, time.UTC).Unix(), tok
		}); err != nil {
			t.Fatal(err)
		}
	}
	sub("sub1", "explore", &store.Tokens{Input: 2, CacheRead: 20, Output: 2})
	sub("sub2", "explore", nil) // still runs: no tokens yet
	return st
}

func group(t *testing.T, by, sortBy string, since time.Time, loc *time.Location) []Row {
	t.Helper()
	entries, err := Entries(testStore(t), []string{"fakeagent"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Group(entries, Options{Since: since, By: by, Sort: sortBy, Loc: loc})
	if err != nil {
		t.Fatal(err)
	}
	return r.Rows
}

func keys(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Key)
	}
	return out
}

func TestGroup(t *testing.T) {
	plus3 := time.FixedZone("UTC+3", 3*3600)
	cases := []struct {
		name, by, sortBy string
		loc              *time.Location
		since            time.Time
		want             []string
	}{
		// 21:30 UTC is Jan 06 at UTC+3: every call falls on one day.
		{"days in their zone", "day", "total", plus3, time.Time{}, []string{"Tue Jan 06"}},
		{"days in order", "day", "total", time.UTC, time.Time{}, []string{"Mon Jan 05", "Tue Jan 06"}},
		{"since drops earlier", "day", "total", time.UTC, time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC), []string{"Tue Jan 06"}},
		{"sessions, most first", "session", "total", time.UTC, time.Time{}, []string{"aaaaaaaa proj-a · fix the build", "bbbbbbbb proj-b"}},
		{"sessions, most fresh first", "session", "fresh", time.UTC, time.Time{}, []string{"bbbbbbbb proj-b", "aaaaaaaa proj-a · fix the build"}},
		{"types", "type", "total", time.UTC, time.Time{}, []string{"main", "explore"}},
		{"models", "model", "output", time.UTC, time.Time{}, []string{"model-a", "model-b"}},
		{"paths", "path", "cache", time.UTC, time.Time{}, []string{"/work/proj-a", "/work/proj-b"}},
		{"agents", "agent", "total", time.UTC, time.Time{}, []string{"fakeagent"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keys(group(t, c.by, c.sortBy, c.since, c.loc)); !slices.Equal(got, c.want) {
				t.Errorf("rows = %q, want %q", got, c.want)
			}
		})
	}
}

// A subagent counts with its session, and once as a run.
func TestGroupSubagents(t *testing.T) {
	rows := group(t, "session", "total", time.Time{}, time.UTC)
	want := Totals{Input: 2 + 5 + 2, CacheWrite: 18, CacheRead: 2010 + 20, Output: 105 + 2, Subagents: 1}
	if rows[0].Totals != want || rows[0].Session != sessA || rows[0].Agent != "fakeagent" {
		t.Errorf("row = %+v, want %+v", rows[0], want)
	}
	explore := group(t, "type", "total", time.Time{}, time.UTC)[1]
	if explore.Subagents != 1 || explore.Output != 2 {
		t.Errorf("explore = %+v", explore)
	}
}

func TestGroupRefusesUnknown(t *testing.T) {
	if _, err := Group(nil, Options{By: "week", Sort: "total", Loc: time.UTC}); err == nil {
		t.Error("by week: no error")
	}
	if _, err := Group(nil, Options{By: "day", Sort: "price", Loc: time.UTC}); err == nil {
		t.Error("sort by price: no error")
	}
}

func TestSince(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*3600)
	now := time.Date(2026, 1, 10, 15, 30, 0, 0, loc)
	cases := []struct {
		in   string
		want time.Time
		bad  bool
	}{
		{in: "1d", want: time.Date(2026, 1, 10, 0, 0, 0, 0, loc)},
		{in: "7d", want: time.Date(2026, 1, 4, 0, 0, 0, 0, loc)},
		{in: "24h", want: now.Add(-24 * time.Hour)},
		{in: "2026-01-02", want: time.Date(2026, 1, 2, 0, 0, 0, 0, loc)},
		{in: "all"},
		{in: "0d", bad: true},
		{in: "d", bad: true},
		{in: "7w", bad: true},
		{in: "-3d", bad: true},
		{in: "7dh", bad: true},
		{in: "7hd", bad: true},
		{in: "", bad: true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := Since(c.in, now, loc)
			if (err != nil) != c.bad || !got.Equal(c.want) {
				t.Errorf("Since(%q) = %v, %v; want %v, bad %v", c.in, got, err, c.want, c.bad)
			}
		})
	}
}

func TestCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1_000: "1.0k", 12_345: "12.3k", 141_234_567: "141.2M", 1_734_000_000: "1.73B"} {
		if got := Count(n); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestWriteTable(t *testing.T) {
	entries, err := Entries(testStore(t), []string{"fakeagent"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Group(entries, Options{By: "session", Sort: "total", Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := r.WriteTable(&b, 1); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"session                           fresh in  cache read    output  subagents   runs",
		"aaaaaaaa proj-a · fix the build         27        2.0k       107          1      –",
		"… 1 more",
		"total                                  127        2.0k       108          1      –",
		"",
	}, "\n")
	if b.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestWriteJSON(t *testing.T) {
	r, err := Group(nil, Options{Since: time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC), By: "day", Sort: "total", Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := r.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	var got struct {
		By, Since string
		Rows      []json.RawMessage
		Total     map[string]int64
	}
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.By != "day" || got.Since != "2026-01-04T00:00:00Z" || got.Rows == nil || len(got.Rows) != 0 || got.Total["output"] != 0 {
		t.Errorf("report = %s", b.String())
	}
}
