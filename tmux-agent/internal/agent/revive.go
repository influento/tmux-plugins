package agent

import (
	"regexp"

	"github.com/influento/tmux-plugins/internal/shell"
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
	return shell.Join(append([]string{"tmux-agent", "run", a.Name()}, r.ResumeArgs(session)...)), true
}
