package attention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// OptLabels is the pane option that holds a pane's labels: a JSON list,
// one entry per source, in the order the sources first reported.
const OptLabels = "@labels"

// OptSubagents is the pane option that lists, by id and space-separated,
// the subagents that run beside the pane's agent. The agent layer keeps
// it; the panel shows how many.
const OptSubagents = "@agent_subagents"

// Label is what one source (a tool, or tmux-agent itself) says about a
// pane: a short text the panel shows on the row's second line, and tokens
// for tools to read. The core knows no source and no token.
type Label struct {
	Source string            `json:"source"`
	Text   string            `json:"text,omitempty"`
	Tokens map[string]string `json:"tokens,omitempty"`
	// Until is when the label goes, unix seconds; 0 for never.
	Until int64 `json:"until,omitempty"`
}

// Shown is the label's text, else its tokens' values in name order.
func (l Label) Shown() string {
	if l.Text != "" {
		return l.Text
	}
	var vals []string
	for _, k := range slices.Sorted(maps.Keys(l.Tokens)) {
		vals = append(vals, l.Tokens[k])
	}
	return strings.Join(vals, " ")
}

// Limits of a label.
const (
	maxLabelText  = 64
	maxLabelValue = 64
	maxTokens     = 16
)

var labelName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ErrBadLabel is a label the core refuses.
var ErrBadLabel = errors.New("bad label")

// Valid checks a label's names and lengths. Text and values must hold no
// control characters: they go on one line.
func (l Label) Valid() error {
	if !labelName.MatchString(l.Source) {
		return fmt.Errorf("%w: source %q: use a-z, 0-9, _ and -, up to 32", ErrBadLabel, l.Source)
	}
	if err := labelText("text", l.Text, maxLabelText); err != nil {
		return err
	}
	if len(l.Tokens) > maxTokens {
		return fmt.Errorf("%w: more than %d tokens", ErrBadLabel, maxTokens)
	}
	for k, v := range l.Tokens {
		if !labelName.MatchString(k) {
			return fmt.Errorf("%w: token %q: use a-z, 0-9, _ and -, up to 32", ErrBadLabel, k)
		}
		if err := labelText("token "+k, v, maxLabelValue); err != nil {
			return err
		}
	}
	return nil
}

func labelText(what, s string, n int) error {
	if utf8.RuneCountInString(s) > n {
		return fmt.Errorf("%w: %s longer than %d characters", ErrBadLabel, what, n)
	}
	if strings.ContainsFunc(s, unicode.IsControl) || !utf8.ValidString(s) {
		return fmt.Errorf("%w: %s holds a control character", ErrBadLabel, what)
	}
	return nil
}

// empty reports whether the label says nothing: reporting it clears the
// source's label.
func (l Label) empty() bool { return l.Text == "" && len(l.Tokens) == 0 }

// ParseLabels reads a pane's @labels and leaves out the labels past their
// time. A garbled value reads as none.
func ParseLabels(raw string, now int64) []Label {
	if raw == "" {
		return nil
	}
	var ls []Label
	if err := json.Unmarshal([]byte(raw), &ls); err != nil {
		return nil
	}
	return slices.DeleteFunc(ls, func(l Label) bool { return l.Until != 0 && l.Until <= now })
}

// withLabel is labels with l in place of its source's label, or after the
// others when the source is new; an empty l removes it.
func withLabel(labels []Label, l Label) []Label {
	i := slices.IndexFunc(labels, func(o Label) bool { return o.Source == l.Source })
	switch {
	case l.empty() && i >= 0:
		return slices.Delete(labels, i, i+1)
	case l.empty():
		return labels
	case i >= 0:
		labels[i] = l
		return labels
	}
	return append(labels, l)
}

// Report sets the pane's label from l.Source to l, or clears it when l has
// neither text nor tokens. It writes only when the labels change. The
// caller holds the lock: two reports would race on the one option.
func (c *Core) Report(ctx context.Context, pane string, l Label) error {
	if err := l.Valid(); err != nil {
		return err
	}
	raw, err := c.Tmux.Output(ctx, "display-message", "-p", "-t", pane, "#{"+OptLabels+"}")
	if err != nil {
		return err
	}
	old := ParseLabels(raw, c.now().Unix())
	labels := withLabel(slices.Clone(old), l)
	expired := len(old) < len(ParseLabels(raw, 0))
	if !expired && slices.EqualFunc(old, labels, sameLabel) {
		return nil
	}
	args := []string{"set-option", "-pu", "-t", pane, OptLabels}
	if len(labels) > 0 {
		b, err := json.Marshal(labels)
		if err != nil {
			return err
		}
		args = []string{"set-option", "-p", "-t", pane, OptLabels, string(b)}
	}
	if _, err := c.Tmux.Run(ctx, args...); err != nil {
		return err
	}
	c.changed(ctx)
	return nil
}

func sameLabel(a, b Label) bool {
	return a.Source == b.Source && a.Text == b.Text && a.Until == b.Until && maps.Equal(a.Tokens, b.Tokens)
}
