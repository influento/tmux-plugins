# Backlog

Open bugs, features and ideas for tmux-plugins, one entry each.

## tmux-agent: bugs

- The context share misses some of what sets Claude Code's compaction
  point (bug): `CLAUDE_CODE_AUTO_COMPACT_WINDOW`, which wins over every
  settings file; `modelSettings.<model>.autoCompactWindow`, which wins over
  the top-level value; an `autoCompactWindow` of `auto` (tmux-agent skips
  that whole file); with no `autoCompactWindow` set anywhere, a default
  below the context window (the catalog's
  `runtime.default_auto_compact_window`, a server's or an experiment's);
  and, for a pane whose agent has no status line command,
  a window below the catalog's (a model whose long window is an opt-in, an
  account held to less). Read from Claude Code 2.1.332's code, none seen
  on the user's machine.
- The window a status line told is wrong for a moment in three orders
  (bug, minor; each ends at the agent's next status line run): a session
  resumed in a new process with another model reads with the old
  process's window; `/clear`, when the new session's status line runs
  before the old session's SessionEnd hook, loses the new window; a
  second agent of the same type with a status line inside the pane takes
  the pane's window away. Found by a review, 2026-11-28; none seen.
- A hook for a pane that is gone fails instead of noting the session's end
  (bug): tmux 3.8 answers `display-message -p -t %99` for a missing pane
  with empty fields and exit 0, so `Apply` never takes its `isGone` branch;
  a SessionEnd then fails at `paneOpts` with "no such pane", and the
  session's record gets no end. Found by a review, 2026-11-28.
- Phantom working flag (bug; waits for it to happen again): an idle agent pane
  went back to working with nothing to cause it. Seen once, on 2026-11-14 with
  v0.3.7: the turn ended at 03:16:51, and at 03:19:53 the flag went to
  working. Ruled out: no prompt in Claude Code's history, no transcript entry
  between the turn's end and Claude Code's recap (03:20:41), no subagent, no
  install or tmux-init, and the watcher never sets working. A recap in another
  pane left its flag alone. The likely cause is a hook event that Claude Code
  fired without a transcript line. No repro. Check first that
  `TMUX_AGENT_DEBUG=1` is in the agents' environment, so that
  `$XDG_STATE_HOME/tmux-agent/log` names each hook event. When it happens
  again, note the time and what was done in that window just before, and read
  the log around that minute.
- Panel: the client flicks between two windows (bug; waits for it to happen
  again): with the panel open, the client switched between a working agent's
  window and the window of the first agent row, about twice a second. Clicks
  and keys did not stop it; closing the panel did. Seen once, on 2026-11-15,
  on a dev build made before one background `sidebar go` at a time (580d4e9),
  so that change may have fixed it. Since v0.3.23 the log always traces each
  panel move, each switch the follow hook sees and each place, with the cause
  and `$FZF_ACTION`. When it happens again, note the minute and read
  `$XDG_STATE_HOME/tmux-agent/log` around it.
- Conformance hook tests fail inside a Claude Code Stop hook (bug): the
  Stop cases of `tmux-agent/internal/conformance` (TestHookStopFlagsDone,
  TestHookStopWithSubagentsKeepsWorking, TestHookEnd/other) find the pane
  "working", want "done", when `go test` runs as one of two Stop hooks of
  a Claude Code session that start in the same second (go-gate's and
  workbench's review). The hook runs as a child of `go test`, so its check
  for the agent's other Stop hooks (`OtherHooksRun`) walks out of the
  sandbox to the real Claude Code process and counts its other child. Shown
  on 2026-11-16: a background `sleep 20` started from Claude Code just
  before `go test -count=1 -run TestHookStopFlagsDone
  ./tmux-agent/internal/conformance/` fails it the same way; the run alone
  passes.
- A window that `start` opens shows `sh` (bug): the window runs `sh -c`
  with the launcher as a child, so the terminal's foreground process
  group leader is `sh`, and `#{pane_current_command}` is `sh`, not the
  agent. (The core's sweep keeps such a pane's flags: its owner runs below
  the pane.) Seen on 2026-11-20 in the contract sandbox. `set -m` in the
  start script would give the launcher its own group, but a Ctrl-Z of the
  agent would then leave a stopped, orphaned group behind the login shell.
- Panel: a split window's pane rows show no number. They show tmux's
  pane index (".1", ".2"), which the user finds odd; in the window the
  panel has joined they show ".2", ".3", as the panel takes index 1
  (seen at the demo of the open row, 2026-11-16; main does the same).
- Panel: a search's cursor goes back to its first row (bug): with a search
  in the panel, a move to a row of another window takes the client there,
  then the cursor jumps back to the search's first row and the client
  follows it. The panel's join reloads the panel, and the result event
  puts the cursor on row 2 after every reload while a query is set. The
  same on main before the open-row item (seen 2026-11-16 on a throwaway
  server). A move between the panes of one split window stays.
- Panel: the cursor shows one frame on another row (bug): when rows above the
  cursor come or go in a reload, fzf can draw the cursor for one frame on
  another row before place puts it back. Minor and cosmetic. Check first
  whether it still shows since one background go at a time.

- Launchers leave defunct `tmux-agent` children (bug): on 2026-11-21, four
  `tmux-agent run claude` launchers (v0.3.10, v0.3.33, v0.3.34) had 26
  zombie `tmux-agent` children between them, 18 under one launcher 3 h
  old. Which children they were, and why the launcher does not reap them,
  is not known yet.

## tmux-agent: contract

- Version field in `list --json` (feature; waits for a second reader of
  `list`): the JSON of `start`, `close` and `task` carries `version`;
  `list --json` does not. The integration rules say every JSON of the contract
  has one. Add it before another tool reads `list` (a dashboard, the
  cross-machine agent list, workbench).
- Event stream (idea; waits for its first user): `tmux-agent events`, one JSON
  line per change of a flag or a label, with a version. A part of the contract
  comes only when a tool needs it. Likely first users: the live-sections
  dashboard, the cross-machine agent list, a GUI. Open: what a line carries,
  and how a reader catches up after a gap.
- Session-window tie and resume (feature; nothing blocks it): each window
  remembers the agent sessions that ran in it, ended ones included, and one
  key resumes one of them. Each session already has a record in
  `$XDG_STATE_HOME/tmux-agent/sessions/<agent>/<id>.json`, never deleted, with
  its title, folder, last flag, how it ended and the pane it last ran in. A
  record does not know its window: pane ids change at a restore. Decided: the
  hook writes the window's `@revive_id` (which tmux-revive keeps across
  restarts) into the record; a window's history is every record with that id,
  newest first; the user searches by window name; an agent the user exited
  stays closed after a restart, and this resumes it by hand. Proposed readers:
  a resume picker (`prefix R`: resume here, in a new window, or pick from all
  windows), and `tmux-agent which <window>` (the session id),
  `where <session>` (session:window, pane, flag) and `history <window>`, for
  scripts and tmux-peers. It overlaps `claude --resume`, which lists the
  sessions of the current folder. The process tracker, prompt and read, and
  moving a conversation to another machine build on it. Open: show all of a
  window's sessions or only the last 30 days; should a plain `claude` in a
  window with history offer to resume; a popup of its own or a mode of the
  panel. Size S, about a day; a live demo first.
- `wait-output` (feature; parked): `tmux-agent wait-output <pane> <pattern>`
  blocks until a pane prints a pattern, for example `listening on :3000` or
  `passed|failed`. It knows no agent and needs nothing else; about two hours.
  Useful for scripts and agents that start a server or a test run in a pane
  they can see.
- Orchestration skill (idea): `tmux-agent prompt` and `read` exist. A skill
  that teaches agents to use them, here and over ssh on another machine
  (`ssh HOST tmux-agent prompt --wait NAME`), is not written. tmux-peers
  and Claude Code's own messages stay for Claude-to-Claude messages; what
  only prompt gives: scripts and other agents can drive a pane, by window
  name, and it refuses while the agent waits on the user.
- Process tracker (feature; optional, waits for the user to want it): a list
  of every process an agent started (shells, background jobs, dev servers,
  apps), under its session; what an ended session left shows as "no session",
  with keys to jump, kill, kill the group and show output, and a count on the
  pane's row. Orphan test, Claude Code 2.1.325: background Bash tasks stop at
  `/exit` (the default "Exit and stop tasks") and at kill-pane, but keep
  running after `kill -9` on claude; a `nohup … &` job always keeps running,
  and its parent is pid 1 at once, so a process tree cannot find it. None were
  left on a workstation after 10 hours. Full design: the launcher starts each
  run in its own systemd user scope in `agents.slice`; the cgroup keeps
  detached children; a live scope whose agent is gone holds leftovers; a hook
  finds its scope in `/proc/self/cgroup`, and the session records map scope to
  session to window. Not caught: apps started through `swaymsg exec`,
  `xdg-open` or D-Bus activation. Risk: a scope per agent changes the shutdown
  order that tmux-revive's shutdown guard was tested against; the guard then
  needs a new test. A cheaper form: list the processes with `CLAUDECODE=1` in
  their environment and no live claude above them, with a kill key; no scope
  and no change to shutdown, but it cannot tell which session a detached job
  belongs to. A use: workbench's merge or drop could kill what an item's
  agents left running in the worktree that goes. Open: at a session's end with
  processes left, list them, kill them after a delay, or ask; include
  processes the user starts in an agent pane; keep a log of finished
  processes; a popup of its own or rows in the panel. Size S-M.
- Automations (feature; parked): scheduled agent runs, each with a name, a
  schedule, a repo, where it runs (a new worktree through workbench, or the
  repo root), the agent, model, prompt and timeout. A systemd timer calls
  `tmux-agent start`; a list shows the schedules and their last runs. tmux has
  no forms, so a schedule is a file edited in nvim in a popup. Parked because
  Claude Code's own scheduled runs cover much of it. Check first what they
  lack.
- Subagent stats and view (idea): `tokens --by type` already gives the runs
  and tokens per subagent type. Not built: per type, the failed runs and the
  median time, and the runs at depth 2 or more and in the background, from the
  subagent records (proposed `tmux-agent stats subagents --since 7d`); and a
  panel view of the subagents. Not checked yet: whether a subagent's MCP
  dialog notification carries its `agent_id`, so the needs-you goes to the
  right agent.
- Git label: ahead, behind and uncommitted (idea; waits until it is missed):
  the git label shows the branch and `wt` only. `↑3 ↓1` for commits ahead and
  behind, and `*` for uncommitted changes, were left out because each costs
  one more git call per hook event.
- A second agent's adapter (idea; waits for a need): the agent layer takes
  another agent with one adapter package, one registry line and its hook
  config in dotfiles. Codex is the likely next: hooks for turns, the pane
  title (through tmux's `pane-title-changed` hook) for working and needs input
  where its hooks lack them, and `codex exec` as a one-shot run. Without an
  adapter an agent already gets a failed flag on a non-zero exit.

## tmux-agent: panel and views

- Several live sections (idea; waits until the user knows which sections): a
  dashboard with more than the panel, for example the selected row's details
  (labels, the agent's last message, a task's state), an event log, git or
  tasks. The technique: a sway window, opened and closed by one key, runs a
  tmux session of its own with one pane per section in a fixed layout, as a
  second client on the same server. fzf's focus event sets a tmux option and
  the other sections redraw on it; the panel targets the work client by name,
  so Enter and Esc still move it. Data from `list --json` with a version, and
  the event stream. tmux-revive leaves the session out through
  `@skip_sessions`. Not chosen: a sway window per section, an own TUI, a web
  page. Check first by hand: a terminal window with a tmux session of a few
  panes (the panel's command, lazygit, a `watch` over `list --json`), no code.
- Usage and quota page (idea): what each agent subscription has used and when
  it resets, beside `tmux-agent tokens`. Offered, not decided. Check first
  where Claude Code exposes quota use and reset times, if at all.
- A log of what agents did (idea; its place is open): the user runs no
  workbench commands; agents do, so the user wants a log of what they did,
  workbench calls included. Not in tmux-agent for now. Suggested: workbench
  logs its own calls, since only it knows the item, the stage and the result;
  every agent's tool calls are in Claude Code's transcripts already, which a
  reader could show later; a workbench label could show the last action in the
  panel.
- Panel: two-press kill and a start marker (idea): ctrl-x could ask for a
  second press before it kills a window or pane; the row the panel opened on
  could keep a marker (Esc goes back to it).
- Dock inside a window (idea; the work is in dotfiles): a tool tied to one
  window or repo, such as lazygit, as a split pane that is a nested client of
  a hidden tmux-overlay session. One key shows and hides it; closing it, even
  with `prefix x`, loses nothing; a tinted background marks it. tmux-revive
  must leave such panes out of the save, which `@revive_skip` already does.

## Cross-machine

Moving a conversation and the shared agent list wait for the second
workstation to run the new setup (dotfiles' main pushed, then its cutover).
tmux-agent and tmux-revive install on the workstation profile only; a server
that runs agents needs them in the common profile and a CI job for the
server's older tmux. Open for all three: which ones, in what order, and
whether the server runs agents or is only a target for remote tmux.

- Remote tmux in a local window (feature; dotfiles only, any time): a local
  window runs `ssh <host> -t tmux attach`; a key (F12) turns the outer prefix
  off so keys reach the remote tmux, and the status line shows that. Dotfiles
  only, minutes of work, no code here.
- Move a conversation to another machine (feature; waits for the second
  workstation and the session-window tie): `tmux-agent move <window> <host>`
  copies the session's transcript and the work tree to the same paths on the
  other machine, opens a window there and resumes the session through
  `tmux-agent revive`. Relies on identical paths. Check first whether claude
  on the other machine resumes a copied transcript cleanly. Open: carry
  uncommitted work with rsync, or require a WIP commit and push. Size S.
- One agent list for all machines (feature; waits for the second workstation,
  and agents running on other machines every day): a poller merges each
  machine's `tmux-agent list --json` over a shared SSH connection; the status
  line counts per machine, and a flag on another machine shows as
  `host:session:window`. Needs the version field in `list --json`. The panel,
  a pane, already works over SSH. Size M-L.

