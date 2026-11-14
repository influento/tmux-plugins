// Package agent is tmux-agent's agent layer: the contract every integration
// implements, the launcher every agent starts through, the neutral events
// and what they do to flags, records and window titles. It knows no agent;
// adapters translate their agent's hooks and arguments into this package's
// terms, and only the CLI imports them, to register them.
package agent

import (
	"path/filepath"
	"slices"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// Agent is the one required part of an integration: with only a name and a
// command, the launcher reports start, end and exit code.
type Agent interface {
	// Name is the registry key, the argument of `hook` and `run`, and the
	// name of the shell function shell-init defines.
	Name() string
	// Command is the program the launcher runs.
	Command() string
}

// HookReader turns the agent's hook payload into a neutral event. ok is
// false for an event that means nothing to the pane.
type HookReader interface {
	ReadHook(payload []byte) (ev Event, ok bool, err error)
}

// OneShotter tells a run that is one turn (claude -p) from an interactive
// one. Without it every run is interactive.
type OneShotter interface {
	OneShot(args []string) bool
}

// Resumer builds the arguments that resume a session, and reads which
// session a run's arguments resume. Without it a restore leaves a shell.
type Resumer interface {
	ResumeArgs(session string) []string
	ResumedSession(args []string) string
}

// ProcessNamer lists the names the agent's processes run under. An adapter
// with hook events needs it: it is how a hook tells the run's own agent from
// one started inside it.
type ProcessNamer interface {
	ProcessNames() []string
}

// Titler names a window from the agent's transcript, or returns "". A
// title the agent gave the session itself is sure, and replaces a stand-in
// (such as the first prompt's first words) at a later turn end.
type Titler interface {
	Title(transcript string) (title string, sure bool)
}

// Interrupter reads from the agent's transcript when the user last cut a
// turn short (declined a permission, pressed Esc). The agent sends no hook
// for that, so without it a working or needs-you flag stays until the next
// prompt.
type Interrupter interface {
	Interrupted(transcript string) (at time.Time, ok bool)
}

// ContextGauge reads how full the agent's context is: the tokens its last
// model call held, and the limit they count against. Both are read again
// each time, so a changed setting shows at the next turn end. ok is false
// before the first model call. cwd is the session's folder now; start, when
// known, the folder the agent process started in, for a reader whose
// environment is not the agent's (a watcher).
type ContextGauge interface {
	Context(transcript, cwd, start string, getenv func(string) string) (used, limit int64, ok bool)
}

// FileWatcher names the files and folders of the agent's own that the
// watcher follows for the session whose transcript is given, beside the
// transcript: a subagent's files, say. A path that does not exist yet is
// followed once it does.
type FileWatcher interface {
	WatchedFiles(transcript string) []string
}

// SubagentEnder tells from the agent's files, without reading a history,
// how a subagent ended; "" while it runs. A subagent the user stops sends
// no hook, so only its files tell.
type SubagentEnder interface {
	SubagentEnded(transcript, id string) Outcome
}

// SubagentReader reads a session's subagents from the agent's own files,
// which hold more than its hooks: the result, the tokens, how each ended.
// The files can be large, so they are read outside the hook, after the
// subagent ended (see ScanSubagents).
type SubagentReader interface {
	// SubagentIDs lists the subagents in the files of the session whose
	// transcript is given.
	SubagentIDs(transcript string) ([]string, error)
	// ReadSubagent reads one of them. Its Outcome is "" while it runs; the
	// rest of the files is then left unread.
	ReadSubagent(transcript, id string) (Subagent, error)
	// SubagentSessions lists every session whose files hold subagents.
	SubagentSessions(getenv func(string) string) ([]SessionFiles, error)
}

// SessionFiles names a session and its transcript.
type SessionFiles struct {
	Session, Transcript string
}

// Subagent is what an event or the agent's files tell about one subagent.
type Subagent struct {
	ID, Type, Description, Prompt, Result, Model, Transcript string
	Depth                                                    int
	Started, Ended                                           time.Time
	Outcome                                                  Outcome
	Tokens                                                   *Tokens
}

// Tokens are the tokens a subagent's model calls used, by kind.
type Tokens = store.Tokens

// Outcome is how a subagent ended.
type Outcome string

// The outcomes. The zero Outcome is a subagent that still runs.
const (
	OutcomeDone    Outcome = "done"    // it finished and reported
	OutcomeStopped Outcome = "stopped" // the user stopped it
	OutcomeUnknown Outcome = "unknown" // its files stop without saying
)

// Marker gives the glyph and the colour (#rrggbb) the panel marks the
// agent's windows with. Without it the agent gets the panel's default mark.
type Marker interface {
	Mark() (glyph, colour string)
}

// Generic is an agent the registry does not know: a name that is also its
// command. It gets what the launcher reports and nothing else.
type Generic string

// Name is the agent's name.
func (g Generic) Name() string { return string(g) }

// Command is the program, the same as the name.
func (g Generic) Command() string { return string(g) }

// CommandName is the name tmux shows for an agent's pane: the command's
// base name, which the launcher puts in argv[0].
func CommandName(a Agent) string { return filepath.Base(a.Command()) }

// Registry holds the known agents.
type Registry struct {
	agents []Agent
}

// NewRegistry returns a registry of agents.
func NewRegistry(agents ...Agent) *Registry {
	return &Registry{agents: slices.Clone(agents)}
}

// Lookup returns the registered agent of that name.
func (r *Registry) Lookup(name string) (Agent, bool) {
	for _, a := range r.agents {
		if a.Name() == name {
			return a, true
		}
	}
	return nil, false
}

// Get returns the registered agent of that name, else a generic one.
func (r *Registry) Get(name string) Agent {
	if a, ok := r.Lookup(name); ok {
		return a
	}
	return Generic(name)
}

// Agents returns the registered agents.
func (r *Registry) Agents() []Agent { return slices.Clone(r.agents) }

// CommandNames returns the names tmux shows for the registered agents'
// panes.
func (r *Registry) CommandNames() []string {
	var names []string
	for _, a := range r.agents {
		names = append(names, CommandName(a))
	}
	return names
}

// ProcessNames returns the process names of every registered agent.
func (r *Registry) ProcessNames() []string {
	var names []string
	for _, a := range r.agents {
		if p, ok := a.(ProcessNamer); ok {
			names = append(names, p.ProcessNames()...)
		}
	}
	return names
}

// Kind is what a neutral event is about.
type Kind int

// The neutral events. The zero Kind is no event.
const (
	KindNone      Kind = iota
	SessionStart       // a session began, resumed, or replaced the last one (/clear)
	Working            // a turn started, or a tool started or finished
	NeedsInput         // the agent waits on a decision
	TurnEnd            // a turn finished
	SessionEnd         // the session ended
	SubagentStart      // a subagent started
	SubagentEnd        // a subagent ended
	Compacted          // the agent compacted its context
)

// End is how a session ended.
type End string

// The ends.
const (
	EndExited  End = "exited"  // the user ended it, or it finished by itself
	EndKilled  End = "killed"  // the launcher got SIGHUP or SIGTERM
	EndUnknown End = "unknown" // the agent cannot tell: a kill, or a one-shot run ending
)

// Event is one neutral event, the same for every agent.
type Event struct {
	Kind       Kind
	SessionID  string // empty when the agent did not say
	Transcript string
	Cwd        string
	// Resume marks a SessionStart that continues an earlier session.
	Resume bool
	// Reason is what a NeedsInput event waits on, as the flag shows it.
	Reason string
	// Tool identifies the tool call a Working or NeedsInput event is about,
	// so a decision pending on one tool is not cleared when another tool
	// that ran beside it finishes. Empty when there is none.
	Tool string
	// ToolEnd marks a Working event sent when a tool finished.
	ToolEnd bool
	// By is the subagent that sent the event; "" for the agent itself. The
	// agent's subagents run beside it, in its session and pane.
	By string
	// Subagents are, on a TurnEnd, the subagents still running, by the ids
	// their events carry in By. Each one's report starts another turn.
	Subagents []string
	// End is how a SessionEnd ended.
	End End
	// Subagent is what the event tells about a subagent: the one a
	// SubagentStart or SubagentEnd is about, or on a Working event one that
	// the agent's tool call started, with its prompt. nil for none.
	Subagent *Subagent
}
