package agent

import (
	"regexp"
	"strings"
)

// uuidRe is the shape a session id must have before it goes into a
// command another program types into a shell.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ReviveCommand is what tmux-revive types into a restored pane's shell to
// resume the session: the launcher with the agent's resume arguments. ok is
// false when the agent cannot resume, or the id does not look like a UUID.
func ReviveCommand(a Agent, session string) (cmd string, ok bool) {
	r, isResumer := a.(Resumer)
	if !isResumer || !uuidRe.MatchString(session) {
		return "", false
	}
	words := append([]string{"tmux-agent", "run", a.Name()}, r.ResumeArgs(session)...)
	for i, w := range words {
		words[i] = ShellQuote(w)
	}
	return strings.Join(words, " "), true
}

// safeWord is a word a POSIX shell and zsh take as it is.
var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ShellQuote quotes s for a POSIX shell or zsh, and leaves plain words
// alone.
func ShellQuote(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
