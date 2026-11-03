package attention

import (
	"slices"
	"testing"
)

func TestParseState(t *testing.T) {
	for _, c := range []struct {
		in   string
		want State
		ok   bool
	}{
		{"needs-you", NeedsYou, true},
		{"done", Done, true},
		{"working", Working, true},
		{"", Idle, false},
		{"idle", Idle, false},
		{"Done", Idle, false},
	} {
		if got, ok := ParseState(c.in); got != c.want || ok != c.ok {
			t.Errorf("ParseState(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestStateRankOrder(t *testing.T) {
	order := []State{NeedsYou, Done, Working, Idle}
	for i := 1; i < len(order); i++ {
		if order[i-1].Rank() >= order[i].Rank() {
			t.Errorf("%s ranks %d, not before %s at %d", order[i-1], order[i-1].Rank(), order[i], order[i].Rank())
		}
	}
	if Idle.String() != "idle" || Idle.Glyph() != " " {
		t.Errorf("idle = %q %q, want \"idle\" and a blank glyph", Idle.String(), Idle.Glyph())
	}
}

func TestRenderStatus(t *testing.T) {
	col := Colours{NeedsYou: "red", Done: "green", Working: "blue", Dim: "default"}
	for _, c := range []struct {
		name string
		n    Counts
		want string
	}{
		{"nothing flagged", Counts{}, ""},
		{"needs you, working, then done", Counts{NeedsYou: 2, Done: 1, Working: 3},
			"#[fg=red]? 2  #[fg=blue]● 3  #[fg=green]✓ 1  #[fg=default]│"},
		{"done only", Counts{Done: 4}, "#[fg=green]✓ 4  #[fg=default]│"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := RenderStatus(c.n, col); got != c.want {
				t.Errorf("RenderStatus = %q, want %q", got, c.want)
			}
		})
	}
}

func TestHumanize(t *testing.T) {
	for _, c := range []struct {
		s    int64
		want string
	}{{0, "0s"}, {59, "59s"}, {60, "1m"}, {3599, "59m"}, {3600, "1h"}, {86399, "23h"}, {86400, "1d"}, {10 * 86400, "10d"}} {
		if got := Humanize(c.s); got != c.want {
			t.Errorf("Humanize(%d) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	const now = 1000
	for _, c := range []struct {
		name   string
		row    Row
		agent  bool
		rank   int
		detail string
		age    string
	}{
		{"flag with a reason", Row{State: "needs-you", Reason: "Bash(make)", At: now - 120}, true, 0, "Bash(make)", "2m"},
		{"flag without a reason", Row{State: "done"}, true, 1, "done", "-"},
		{"dropped flag", Row{Gone: "done"}, true, 3, "flag dropped · done", "-"},
		{"idle agent", Row{Title: "✳ fix the tests"}, true, 3, "idle · fix the tests", "-"},
		{"plain pane", Row{Command: "zsh"}, false, 4, "zsh", "-"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.row
			describe(&r, now, c.agent)
			if r.Rank != c.rank || r.Detail != c.detail || r.Age != c.age {
				t.Errorf("row = rank %d %q %q, want rank %d %q %q", r.Rank, r.Detail, r.Age, c.rank, c.detail, c.age)
			}
		})
	}
}

func TestTrimSpinner(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"✳ fix the tests", "fix the tests"},
		{"fix the tests", "fix the tests"},
		{"1 thing", "1 thing"},
		{"✳", "✳"},
		{"", ""},
	} {
		if got := trimSpinner(c.in); got != c.want {
			t.Errorf("trimSpinner(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Rows sort by rank, then the oldest flag, then the target.
func TestSortRows(t *testing.T) {
	rows := []Row{
		{Rank: 4, Target: "proj-a:1.0"},
		{Rank: 1, At: 20, Target: "proj-a:2.0"},
		{Rank: 0, At: 30, Target: "proj-b:0.0"},
		{Rank: 1, At: 10, Target: "proj-b:1.0"},
		{Rank: 4, Target: "proj-a:0.0"},
	}
	SortRows(rows)
	var got []string
	for _, r := range rows {
		got = append(got, r.Target)
	}
	want := []string{"proj-b:0.0", "proj-b:1.0", "proj-a:2.0", "proj-a:0.0", "proj-a:1.0"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}
}

func TestRenderSessionRows(t *testing.T) {
	col := Colours{NeedsYou: "#ff0000", Done: "green", Working: "#0000ff"}
	sessions := []string{"proj-a", "proj-b", "a-long-session-name"}
	windows := []Window{
		{Session: "proj-a", Name: "w-a"},
		{Session: "proj-a", Name: "w-build", State: Done},
		{Session: "proj-b", Name: "a-window-name-longer-than-16", State: NeedsYou},
		{Session: "proj-b", Name: "w-b", State: Working},
		{Session: "proj-c", Name: "skipped"},
	}
	rows := RenderSessionRows(sessions, windows, col)
	var order []string
	for _, r := range rows {
		order = append(order, r.Session)
	}
	if want := []string{"proj-b", "proj-a", "a-long-session-name"}; !slices.Equal(order, want) {
		t.Fatalf("order = %q, want %q: flagged sessions first, by best rank", order, want)
	}
	red, blue := "\x1b[38;2;255;0;0m", "\x1b[38;2;0;0;255m"
	want := map[string]string{
		// The first column is as wide as its longest name, plus three.
		"proj-b": "proj-b         " + red + "? \x1b[0ma-window-name-lo   " + blue + "● \x1b[0mw-b",
		// A named colour has no escape.
		"proj-a":              "proj-a         " + "  w-a" + "                " + "✓ \x1b[0mw-build",
		"a-long-session-name": "a-long-session ",
	}
	for _, r := range rows {
		if r.Display != want[r.Session] {
			t.Errorf("%s row = %q, want %q", r.Session, r.Display, want[r.Session])
		}
	}
	if rows[0].Rank != NeedsYou.Rank() || rows[1].Rank != Done.Rank() || rows[2].Rank != 9 {
		t.Errorf("ranks = %d %d %d, want %d %d 9", rows[0].Rank, rows[1].Rank, rows[2].Rank, NeedsYou.Rank(), Done.Rank())
	}
}
