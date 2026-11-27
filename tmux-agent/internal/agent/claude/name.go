package claude

import (
	"regexp"
	"slices"
	"strings"
)

// Named is the name --name (or -n) gives the session; "" for none.
func (Claude) Named(args []string) string {
	name := ""
	for i, a := range args {
		switch {
		case a == "--":
			return name
		case a == "--name" || a == "-n":
			if i+1 < len(args) {
				name = args[i+1]
			}
		case strings.HasPrefix(a, "--name="):
			name = strings.TrimPrefix(a, "--name=")
		}
	}
	return name
}

// WithName drops each name args give and starts the session under name.
func (Claude) WithName(args []string, name string) []string {
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			out = append(out, args[i:]...)
			i = len(args)
		case a == "--name" || a == "-n":
			i++ // and its value
		case strings.HasPrefix(a, "--name="):
		default:
			out = append(out, a)
		}
	}
	at := slices.Index(out, "--")
	if at < 0 {
		return append(out, "--name", name)
	}
	return slices.Insert(out, at, "--name", name)
}

// RenameInput is Claude Code's /rename, which takes the rest of the line,
// spaces too.
func (Claude) RenameInput(name string) string { return "/rename " + name }

var (
	sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)
	// dim is text drawn faint: the placeholder an empty input box shows
	// (`Try "fix typecheck errors"`).
	dim = regexp.MustCompile(`\x1b\[2m[^\x1b]*`)
)

const (
	promptMark = "❯"
	ruleChar   = '─'
	// vim mode's modes in which typed text is not text: a rename typed in
	// would run as vim commands.
	vimNormal = "-- NORMAL --"
	vimVisual = "-- VISUAL"
)

// Ready: the process's file says idle, or no status yet (a session that
// has run no turn), and the input box is empty.
func (c Claude) Ready(pid int, session, screen string, getenv func(string) string) bool {
	f, ok := c.process(pid, session, getenv)
	if !ok || (f.Status != "" && !slices.Contains(idleStatuses, f.Status)) {
		return false
	}
	return inputEmpty(screen)
}

// inputEmpty finds Claude Code's input box, one line between two rules,
// the upper one with the session's name in it, and the line that starts
// with ❯ in it. It is empty when nothing but the faint placeholder
// follows the ❯, and vim mode, when on, inserts.
func inputEmpty(screen string) bool {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimSpace(sgr.ReplaceAllString(l, ""))
	}
	for i := len(plain) - 1; i >= 2; i-- {
		if !isRule(plain[i]) {
			continue
		}
		input, ok := strings.CutPrefix(plain[i-1], promptMark)
		if !ok || !strings.HasPrefix(plain[i-2], string(ruleChar)) {
			return false
		}
		for _, below := range plain[i+1:] {
			if strings.Contains(below, vimNormal) || strings.Contains(below, vimVisual) {
				return false
			}
		}
		if strings.TrimSpace(input) == "" {
			return true
		}
		raw := lines[i-1][strings.Index(lines[i-1], promptMark)+len(promptMark):]
		return strings.TrimSpace(sgr.ReplaceAllString(dim.ReplaceAllString(raw, ""), "")) == ""
	}
	return false
}

// isRule is a line of rule characters only.
func isRule(s string) bool {
	return s != "" && strings.Trim(s, string(ruleChar)) == ""
}
