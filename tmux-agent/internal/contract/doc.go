// Package contract holds the launcher's contract tests. Every agent runs
// through the real launcher, typed into a bash shell in a pane of a private
// tmux server, with a stub in place of the agent's program: exit codes,
// signals, Ctrl-C, Ctrl-\ and Ctrl-Z, one-shot runs, resume arguments,
// TMUX_AGENT_RUN, and an agent started inside another. Claude Code runs
// them with its hooks; a generic agent, with only a name and a command, runs
// the ones that need no hooks. The test binary stands in for tmux-agent
// (see package selftest).
package contract
