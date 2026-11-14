package tally

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

// testStore holds two sessions: proj-a's, with calls on two days (model-b's
// at no effort the agent said) and a subagent, and proj-b's, with one call.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	st := &store.Store{Dir: t.TempDir()}
	a := store.Usage{Agent: "fakeagent", Session: sessA, Cwd: "/work/proj-a", Title: "fix the build", Version: 1}
	a.Add(time.Date(2026, 1, 5, 21, 30, 0, 0, time.UTC), "model-a", "high", store.Tokens{Input: 1, CacheWrite: 9, CacheRead: 1000, Output: 50})
	a.Add(time.Date(2026, 1, 5, 21, 45, 0, 0, time.UTC), "model-a", "high", store.Tokens{Input: 1, CacheWrite: 9, CacheRead: 1000, Output: 50})
	a.Add(time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC), "model-b", "", store.Tokens{Input: 5, CacheRead: 10, Output: 5})
	b := store.Usage{Agent: "fakeagent", Session: sessB, Cwd: "/work/proj-b", Version: 1}
	b.Add(time.Date(2026, 1, 6, 10, 0, 0, 0, time.UTC), "model-a", "medium", store.Tokens{Input: 100, CacheRead: 1, Output: 1})
	for _, u := range []store.Usage{a, b} {
		if err := st.SaveUsage(u); err != nil {
			t.Fatal(err)
		}
	}
	sub := func(id, typ string, tok *store.Tokens) {
		if err := st.UpdateSubagent("fakeagent", sessA, id, func(s *store.Subagent) {
			s.Type, s.Model, s.Effort, s.Started, s.Tokens = typ, "model-b", "low", time.Date(2026, 1, 6, 9, 5, 0, 0, time.UTC).Unix(), tok
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
		{"models and efforts", "model", "output", time.UTC, time.Time{}, []string{"model-a high", "model-b ?", "model-b low", "model-a medium"}},
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
	if kinds(rows[0].Totals) != kinds(want) || rows[0].Session != sessA || rows[0].Agent != "fakeagent" {
		t.Errorf("row = %+v, want %+v", rows[0], want)
	}
	explore := group(t, "type", "total", time.Time{}, time.UTC)[1]
	if explore.Subagents != 1 || explore.Output != 2 {
		t.Errorf("explore = %+v", explore)
	}
}

// kinds is a row's tokens by kind and its counts.
func kinds(t Totals) [6]int64 {
	return [6]int64{t.Input, t.CacheWrite, t.CacheRead, t.Output, int64(t.Subagents), int64(t.Runs)}
}

func TestHitRate(t *testing.T) {
	if h := hitRate(0, 0, 0); h != nil {
		t.Errorf("no input: %v, want nil", *h)
	}
	for _, c := range []struct {
		input, write, read int64
		want               float64
	}{{1, 9, 990, 0.99}, {0, 10, 0, 0}, {0, 0, 7, 1}, {1, 2, 3, 0.5}, {0, 1, 2, 0.6667}} {
		if h := hitRate(c.input, c.write, c.read); h == nil || *h != c.want {
			t.Errorf("hitRate(%d, %d, %d) = %v, want %v", c.input, c.write, c.read, h, c.want)
		}
	}
}

// Every row, its runs' line and the total carry the hit rate and the
// tokens of each model and effort, the most first; by model a row is one.
func TestGroupModels(t *testing.T) {
	entries, err := Entries(testStore(t), []string{"fakeagent"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Group(entries, Options{By: "session", Sort: "total", Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	a := r.Rows[0]
	if a.CacheHit == nil || *a.CacheHit != 0.9869 { // 2030 of 9 + 18 + 2030
		t.Errorf("hit = %v", a.CacheHit)
	}
	type use struct {
		model, effort string
		total         int64
		subagents     int
	}
	var got []use
	for _, u := range a.Models {
		got = append(got, use{u.Model, u.Effort, u.Total(), u.Subagents})
	}
	want := []use{{"model-a", "high", 2120, 0}, {"model-b", "low", 24, 1}, {"model-b", "", 20, 0}}
	if !slices.Equal(got, want) {
		t.Errorf("models = %+v, want %+v", got, want)
	}
	if b := a.Models[1]; b.CacheHit == nil || *b.CacheHit != 0.9091 { // 20 of 2 + 20
		t.Errorf("model-b low hit = %v", b.CacheHit)
	}
	if c := a.modelCell(); c != "model-a high +2" {
		t.Errorf("cell = %q", c)
	}
	if len(r.Total.Models) != 4 || r.Total.CacheHit == nil {
		t.Errorf("total = %+v", r.Total)
	}

	r, err = Group(entries, Options{By: "model", Sort: "total", Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range append(r.Rows, Row{Totals: r.Total}) {
		if row.Models != nil || row.CacheHit == nil {
			t.Errorf("by model, row %q: models %v, hit %v", row.Key, row.Models, row.CacheHit)
		}
	}
	if top := r.Rows[0]; top.Model != "model-a" || top.Effort != "high" {
		t.Errorf("by model, first row = %+v", top)
	}
}

// A use is named without its agent's name, its effort ? when unknown.
func TestUseLabel(t *testing.T) {
	for _, c := range []struct {
		u    Use
		want string
	}{
		{Use{Model: "fakeagent-big-2", Effort: "high", agent: "fakeagent"}, "big-2 high"},
		{Use{Model: "other-big-2", Effort: "low", agent: "fakeagent"}, "other-big-2 low"},
		{Use{Model: "fakeagent-big-2", agent: "fakeagent"}, "big-2 ?"},
		{Use{agent: "fakeagent"}, "?"},
	} {
		if got := c.u.label(); got != c.want {
			t.Errorf("label(%+v) = %q, want %q", c.u, got, c.want)
		}
	}
}

func TestGroupFilters(t *testing.T) {
	entries, err := Entries(testStore(t), []string{"fakeagent"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, model, effort string
		want                []string
	}{
		{"a model", "model-b", "", []string{"model-b low", "model-b ?"}},
		{"part of a name", "-a", "", []string{"model-a high", "model-a medium"}},
		{"an effort", "", "medium", []string{"model-a medium"}},
		{"an effort in another case", "", "Medium", []string{"model-a medium"}},
		{"an unknown effort", "", "?", []string{"model-b ?"}},
		{"both", "model-b", "low", []string{"model-b low"}},
		{"none left", "model-c", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Group(entries, Options{By: "model", Sort: "total", Loc: time.UTC, Model: c.model, Effort: c.effort})
			if err != nil {
				t.Fatal(err)
			}
			if got := keys(r.Rows); !slices.Equal(got, c.want) {
				t.Errorf("rows = %q, want %q", got, c.want)
			}
		})
	}
}

// A tally from before efforts, whose transcript is gone, still counts:
// its calls at no effort the agent said.
func TestEntriesOldTally(t *testing.T) {
	st := &store.Store{Dir: t.TempDir()}
	u := store.Usage{Agent: "fakeagent", Session: sessA, Cwd: "/work/proj-a", Version: 1}
	u.Add(time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC), "model-a", "high", store.Tokens{Output: 4})
	if err := st.SaveUsage(u); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(st.Dir, "sessions", "fakeagent", sessB, "usage.json")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte(`{"agent":"fakeagent","session_id":"`+sessB+`","cwd":"/work/proj-b","offset":9,`+
		`"hours":{"2026-01-05T10":{"model-a":{"input":0,"cache_write":0,"cache_read":9,"output":3}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := Entries(st, []string{"fakeagent"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Group(entries, Options{By: "model", Sort: "output", Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(r.Rows); !slices.Equal(got, []string{"model-a high", "model-a ?"}) || r.Total.Output != 7 {
		t.Errorf("rows = %q, total %+v", got, r.Total)
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
		"session                          model             fresh in  cache read     hit    output  subagents   runs",
		"aaaaaaaa proj-a · fix the build  model-a high +2         27        2.0k   98.7%       107          1      –",
		"… 1 more",
		"total                            model-a high +3        127        2.0k   94.1%       108          1      –",
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

// By model the key names the model and effort: no model column.
func TestWriteTableByModel(t *testing.T) {
	entries, err := Entries(testStore(t), []string{"fakeagent"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Group(entries, Options{By: "model", Sort: "total", Loc: time.UTC, Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := r.WriteTable(&b, 0); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"model          fresh in  cache read     hit    output  subagents   runs",
		"model-a high         20        2.0k   99.0%       100          –      –",
		"total                20        2.0k   99.0%       100          –      –",
		"",
	}, "\n")
	if b.String() != want {
		t.Errorf("table:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestWriteJSONModels(t *testing.T) {
	entries, err := Entries(testStore(t), []string{"fakeagent"}, Attribution{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Group(entries, Options{By: "day", Sort: "total", Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := r.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Rows []struct {
			Key      string   `json:"key"`
			CacheHit *float64 `json:"cache_hit"`
			Models   []struct {
				Model, Effort string
				CacheRead     int64    `json:"cache_read"`
				CacheHit      *float64 `json:"cache_hit"`
			} `json:"models"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 2 || got.Rows[0].CacheHit == nil || len(got.Rows[0].Models) != 1 {
		t.Fatalf("report = %s", b.String())
	}
	m := got.Rows[0].Models[0]
	if m.Model != "model-a" || m.Effort != "high" || m.CacheRead != 2000 || m.CacheHit == nil || *m.CacheHit != 0.9901 {
		t.Errorf("first day's model = %+v", m)
	}
}
