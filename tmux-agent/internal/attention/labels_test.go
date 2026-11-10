package attention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// A source's label replaces its last one in place; a new source goes
// last; an empty label removes its source's; a label past its time reads
// as gone, and the next report drops it.
func TestReport(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	pane := s.Display(t, "proj-a:0.0", "#{pane_id}")
	now := time.Unix(1_000_000, 0)
	c := &Core{Tmux: s.Client, Now: func() time.Time { return now }}
	ctx := context.Background()
	shown := func() string {
		var out []string
		for _, l := range ParseLabels(s.Display(t, pane, "#{@labels}"), now.Unix()) {
			out = append(out, l.Source+"="+l.Shown())
		}
		return strings.Join(out, ",")
	}
	for _, step := range []struct {
		l    Label
		want string
	}{
		{Label{Source: "git", Text: "⎇ main"}, "git=⎇ main"},
		{Label{Source: "tool-a", Tokens: map[string]string{"stage": "review", "item": "42"}}, "git=⎇ main,tool-a=42 review"},
		{Label{Source: "git", Text: "⎇ feat wt"}, "git=⎇ feat wt,tool-a=42 review"},
		{Label{Source: "tool-b", Text: "soon gone", Until: now.Unix() + 60}, "git=⎇ feat wt,tool-a=42 review,tool-b=soon gone"},
		{Label{Source: "git"}, "tool-a=42 review,tool-b=soon gone"},
	} {
		if err := c.Report(ctx, pane, step.l); err != nil {
			t.Fatal(err)
		}
		if got := shown(); got != step.want {
			t.Fatalf("after %+v: %s, want %s", step.l, got, step.want)
		}
	}
	now = now.Add(time.Minute)
	if got := shown(); got != "tool-a=42 review" {
		t.Errorf("past its time: %s", got)
	}
	if err := c.Report(ctx, pane, Label{Source: "tool-a"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Display(t, pane, "#{@labels}"); got != "" {
		t.Errorf("@labels = %q, want unset", got)
	}
}

func TestLabelValid(t *testing.T) {
	for _, l := range []Label{
		{Source: "Tool"},
		{Source: ""},
		{Source: "a", Text: "two\nlines"},
		{Source: "a", Text: strings.Repeat("x", 65)},
		{Source: "a", Tokens: map[string]string{"Bad Key": "v"}},
		{Source: "a", Tokens: map[string]string{"k": "tab\there"}},
	} {
		if err := l.Valid(); !errors.Is(err, ErrBadLabel) {
			t.Errorf("Valid(%+v) = %v, want ErrBadLabel", l, err)
		}
	}
	if err := (Label{Source: "work-bench_2", Text: "⎇ main · 3/12", Tokens: map[string]string{"item": "42"}}).Valid(); err != nil {
		t.Errorf("a good label: %v", err)
	}
}

func TestParseLabelsGarbled(t *testing.T) {
	if got := ParseLabels("{not json", 0); got != nil {
		t.Errorf("ParseLabels = %+v", got)
	}
}
