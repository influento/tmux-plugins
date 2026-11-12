# tmux-plugins

Custom tmux plugins, written in Go, released together as prebuilt binaries
through GitHub Releases. The repository is public.

## Repository Structure

```
tmux-plugins/
  go.mod               # one module for all plugins: github.com/influento/tmux-plugins
  Makefile             # build, vet, test, dist, dev/undev, release
  internal/tmux/       # shared tmux client (timeouts on every call, batches)
    tmuxtest/          # private-server test harness, self-install of test binaries
  internal/shell/      # quoting words for a POSIX shell or zsh
  internal/lockfile/   # flock locks: Lock waits until ctx ends, TryLock does not
  internal/layout/     # tmux 3.8 JSON layouts: remove panes, scale, number
  tmux-warp/           # search-and-jump; tmux-warp.sh stays at this path
  tmux-agent/          # agent flags, pickers, hooks, launcher (P0 step 2)
    internal/          # its packages; the compiler keeps them from tmux-revive
  tmux-revive/         # save and restore all of tmux, attach (P0 step 3)
    internal/
  .github/workflows/
    ci.yml             # vet + tests on every push; a v* tag also publishes
```

Each plugin's packages live under its own `internal/`, so tmux-agent and
tmux-revive cannot import each other: they meet only through tmux options
(`@revive_cmd` on a pane, `@revive_id` on a window). Shared code goes in the
root `internal/`.

## Build, test, release

- `make build`: the three binaries in `bin/`. `make vet`: go vet, gofmt, and
  a check that every action in `.github/workflows` is pinned by commit SHA.
- `make test`: vet, then `go test -race ./...`. The characterization tests
  run the bash scripts of the dotfiles checkout in `DOTFILES` (default
  `../dotfiles`) at the commit `DOTFILES_REF` in `ci.yml`, not at its HEAD:
  dotfiles deletes them at the cutover. The Makefile extracts them to a
  temporary folder, exported to the tests as `TMUX_PLUGINS_DOTFILES`; without
  a checkout that has the commit they skip.
- `make dev` / `make undev`: link the `bin/` builds into `~/.local/bin`, and
  remove the links. An installed release is kept as `<name>.release` and comes
  back on undev. Dotfiles' install skips symlinks.
- `make release V=vX.Y.Z`: on a clean `main` that matches `origin/main`, with
  a version after the last tag, runs the tests, then tags and pushes. There is
  no tag hook: every release is an explicit command. The first release with
  three plugins is v0.2.0.
- CI (`ci.yml`) runs in an `archlinux:base-devel` container on amd64, as a
  non-root user, with `TMUXTEST_REQUIRE=1` so a missing tmux fails instead of
  skipping. It checks out dotfiles at the pinned `DOTFILES_REF` for the
  characterization tests. On a `v*` tag the same job runs `make dist` and
  publishes the release.
- Release assets: `<plugin>-linux-{amd64,arm64}` for all three plugins,
  `tmux-warp.sh`, and `SHA256SUMS` over all of them. Dotfiles pins
  `TMUX_PLUGINS_VERSION` and the sha256 of `SHA256SUMS`. The asset names
  `tmux-warp-linux-*` must not change: older dotfiles installs follow
  `releases/latest` and fetch exactly those.
- Every binary prints `--version` exactly as its tag (`-X main.version`).

## Testing

Unit tests use synthetic data. Integration tests use `internal/tmux/tmuxtest`:

- `tmuxtest.Start` runs a private tmux server in a sandbox with its own HOME
  and XDG directories, and stubs for `notify-send`, `claude`, `swaymsg` and
  `systemctl` first on PATH; `Calls(name)` returns what each stub received, parallel
  calls included. The sandbox environment is an allowlist, so nothing
  reaches the user's desktop, tmux server or agent session, and its PATH
  leaves out the user's home, so installed copies in `~/.local/bin` never
  run in place of the code under test. At cleanup the sandbox kills every
  process still running with its environment (a notifier a hook left in
  the background), before its folders go.
- The socket goes in a short directory with `-S`: a unix socket path must fit
  in 108 bytes, and `t.TempDir()` paths do not.
- Servers start with `-f` and a test config, never the user's.
- `Attach` gives a real client through `script(1)` for tests that need one
  (command-prompt, popups, client focus); the standard library has no PTY.
  `Type` sends keys to it.
- `InPane` points plain `tmux` calls in the test process at the private
  server and a pane, for in-process tests of code that runs `tmux`.
- `InstallSelf` puts the test binary on the sandbox PATH under a plugin's
  name, and `SelfMain` in `TestMain` runs the plugin's main when the binary
  starts that way. Each plugin's `selftest` package wraps the two.
  A test binary started with arguments but no `-test.` flags (a notifier
  that in-process code under test spawns as itself) refuses to run the
  tests: they would start it again, without end.

Fixtures and test names use neutral names (`proj-a`, `host-b`): no real
window, host or project names, session ids or home paths.

## Plugin: tmux-warp

Search-and-jump tool for tmux. Type a char or word, see matches with labels,
press label to jump.

### Architecture (follows tmux-jump pattern)

1. **Shell wrapper** (`tmux-warp.sh`): clears `@warp_query`, then calls
   `tmux command-prompt` to store the search query (char or word) into the
   `@warp_query` tmux option, then invokes the Go binary
2. **Go binary** (`tmux-warp`): polls the `@warp_query` option for the query,
   finds matches in pane content, renders overlay with labels to pane TTY,
   spawns `command-prompt -1` for label selection into `@warp_label`
   (multi-char labels prompt iteratively), then jumps cursor via
   `tmux copy-mode` + `send-keys -X`

The binding works both in the foreground (`run-shell`) and with
`run-shell -b` on tmux 3.8; `TestBindingModes` checks both. Dotfiles binds it
in the foreground. (An older design read the query through `run-shell`, which
deadlocked in the foreground; that is gone since input moved to tmux options.)

### How input works

- Search input goes through `tmux command-prompt` (no `-1`) which captures a
  full string (char or word) via tmux's event loop — NOT by reading from the
  pane TTY
- Result is stored via `set-option -g @warp_query '%1'` — NOT
  `run-shell`/`printf`. command-prompt substitutes `%1` textually and
  re-parses the result, so sending it through a shell (`run-shell`) lets query
  metacharacters (`$ ; " ` + backtick) execute. A tmux option has no shell
  layer, so the query is stored literally. (Residual: a literal `'` in the
  query can still break the option's tmux quoting — accepted; do not paste
  untrusted text into the prompt.)
- Go binary polls `show-options -gv @warp_query` with 10ms sleeps, 10s
  timeout; the option is unset until submit (show-options exits non-zero),
  then holds the value. The binary clears the option after reading; the
  wrapper also clears it before the prompt to avoid a stale read
- For label selection after overlay is shown, Go spawns `command-prompt -1`
  via `exec.Command` fire-and-forget (`cmd.Start()` + `go cmd.Wait()`),
  storing the key into `@warp_label` the same way
- Multi-char labels: prompts iteratively, collecting one char at a time until
  an exact label match is found or no labels share the prefix
- `command-prompt` must NOT use `-t paneID` — it takes a target-client, not a
  pane; passing a pane ID silently breaks input. Omit `-t` entirely (matches
  tmux-jump)

### How the jump moves the cursor

Copy mode, `top-line`, then `cursor-down` to the target row, `start-of-line`,
and `cursor-right` by grid cells. Never one flat `cursor-right` count across
rows: what a line break costs depends on `mode-keys` (vi stops on the last
cell, emacs one past it), and the old flat count landed one cell right per
row on tmux 3.8. A grid cell holds a base rune and its combining marks; a wide
rune is one step but two screen columns. `TestJumpLandsOnTarget` checks both
mode-keys settings on a real server.

### Debug logging

Off by default. Set `TMUX_WARP_DEBUG=1` to enable. Logs go to
`$XDG_RUNTIME_DIR/tmux-warp.log` (falling back to `$TMPDIR/tmux-warp-<uid>.log`),
created with mode `0600` and `O_NOFOLLOW`; override the path with
`TMUX_WARP_LOG`. Shows pane state, chars received, match counts, jump steps.
Check this first when debugging.

## Plugin: tmux-agent

Shows what each agent pane needs (flags, status counts, pickers, jump,
notifications) and starts agents through one launcher, `tmux-agent run`.
Ported from dotfiles' tmux-attention and claude-tmux in P0 step 2.
`tmux-agent --help` lists the subcommands.

Packages in `tmux-agent/internal/`:

- `attention`: the core. Flags in the `@attention*` pane options, status
  counts, sweep, list, borders, jump, notifications. Knows no agent.
- `agent`: the agent layer. Neutral events, the hook handler, the launcher,
  the nested-agent rule, the title sweep, the git label. Knows no
  particular agent.
- `agent/claude`: the Claude Code adapter: hook mapping, one-shot and resume
  arguments, the window title (Claude Code's own `ai-title` from the
  transcript, cut at a word to 24 characters; the first prompt until then),
  the interrupt line of a turn cut short.
- `git`: the one place that runs git: a folder's branch and whether it is
  a linked worktree, in one `rev-parse` (a second for a detached HEAD).
- `store`: one JSON record per session in
  `$XDG_STATE_HOME/tmux-agent/sessions/<agent>/<id>.json` (mode 0600, never
  deleted), one per subagent in `<id>/subagents/<subagent id>.json`, and
  the lock.
- `sidebar`: the panel as a pane that follows its client (see below).
- `ui`: fzf, notify-send, swaymsg. `cli`: the subcommands, wiring the rest.
- `selftest`: lets a test binary stand in for tmux-agent.
- `conformance`: one suite, two drivers: the Go code, and dotfiles' bash
  scripts (characterization). `contract`: the launcher in a real pane with
  a stub agent: exit codes, signals, Ctrl-C, Ctrl-Z, one-shot, resume,
  nested agents, revive, agents gone without a word.

Rules:

- `tmux-agent hook <agent>` prints nothing on stdout (Claude Code adds hook
  stdout to the model's context), recovers panics and exits 0 (exit 2 from
  PreToolUse blocks the tool call), and has a timeout on its lock and on
  every tmux call.
- tmux-init sets hooks only at a fixed index: `[10]` tmux-agent, `[20]`
  tmux-revive. Never `set-hook -u` without an index: it clears the whole
  array.
- A pane's live record is in pane options (`@agent*`, and `@revive_cmd` for
  tmux-revive); the window title an agent set is `@agent_title`.
- `@revive_cmd` is `tmux-agent revive <agent> <session id>`, for a UUID
  session id only. A one-shot run sets none, also one the launcher did not
  start (the arguments of the hook's nearest agent process). revive marks
  the restored window name as the agent's title, so it goes when the run
  ends however it ends (the launcher releases it too); a session whose
  transcript is gone starts nothing, and gives the window its automatic
  name back.
- `seen`, `status` and `sweep` end the session of a pane whose agent is
  gone without a word (killed, crashed, outside the launcher): no agent
  process left below the pane's shell (a stopped one counts). The resume
  command, the `@agent*` options and the agent's title go. A pane whose
  launcher runs, or whose run was killed (the record's end), keeps them.
- A client has focus when `#{client_flags}` contains `focused`; tmux 3.8 has
  no `client_focused`.
- The launcher re-execs itself with the agent's name as argv[0]: tmux names
  a pane after the argv[0] of the terminal's foreground process group
  leader. It catches Ctrl-C and Ctrl-\, passes SIGTERM and SIGHUP on, stops
  itself when the agent stops itself (Ctrl-Z in a TUI), and dies from the
  agent's signal.
- A hook counts for a run only when its nearest agent process is the run's
  own; a launcher started inside an agent runs the agent in its place.
- A turn cut short (No or Esc at a permission prompt) sends no hook. Seen,
  status, sweep, list, jump and the picker read the transcript's last
  message: Claude Code's interrupt line, written at or after the flag,
  clears the flag.
- Subagents run beside their agent, in its session and pane; Claude Code
  2.1.322 runs every one in the background. A turn that ends while one
  runs (Stop's `background_tasks`) leaves the pane working: its report, or
  its stop by the user, starts the next turn. A needs-you waits on one
  agent, the main one or a subagent (`@agent_wait_by`, the hooks'
  `agent_id`); only that agent's events end it.
- Subagents (F6): the pane lists the running ones in `@agent_subagents`
  (SubagentStart adds, SubagentStop removes, each Stop sets it, since a
  subagent the user stops sends no SubagentStop); the panel shows the count
  after the name (`⑂2`), in the working colour. A SubagentStop with an
  empty type is a hidden helper: ignored. A subagent's record keeps its
  prompt and result text (Claude Code deletes old transcripts) and its
  tokens. The hook never reads Claude Code's subagent files (one ran to
  2 GB): it spawns `subagents fill`, which waits for the meta file to say
  finished or stopped, then reads the history; `tmux-init` spawns
  `subagents scan`, the pass over every session's files. A complete record
  is never read again. A subagent's own events never move the git label.
- A one-shot run (`claude -p`) is flagged working at launch and done or
  failed at exit by the launcher; its own hooks only name the session.
- `popup` waits until the user closes the popup, with no timeout; `pick`
  gives the tmux calls after the choice their own deadline. The 30 s
  command deadline leaves out the time the user takes.
- The panel (`prefix f` runs `popup`, which opens or closes it): a tmux
  pane at the left edge, full height, 40 columns, one per client (pane
  option `@sidebar_client`), that moves with join-pane into every window
  the client goes to. Each window keeps its main area's layout (its panes
  without the panel) in `@sidebar_main`; the panel's arrival, departure
  and every resize lay the whole window out again from it, in one command
  list with the move and the switch, so tmux draws once. tmux 3.8's
  `select-layout` takes JSON, places panes by their `"I"` id, refuses two
  panes with one `"i"`, and does not scale: `internal/layout` scales. The
  hooks (follow, keep, reload) run only while `@sidebar_open` is set.
  Keep puts the panel back after a split, a resize, a preset or a swap;
  not while zoomed, nor in a window that looks as it left it
  (`@sidebar_mark`). The README's `|` and `-`, with the focus in the
  panel, split the main pane that had the focus last (`@sidebar_last`,
  which the after-select-pane hook records without a process) in its
  folder, through `run-shell -C` with no process: tmux draws once. A split
  of the panel some other way (the mouse, tmux's own keys) splits the
  panel: keep moves the new pane to that main pane, split the same way at
  half its size, a default shell started again in that pane's folder; the
  panel's split shows for a moment. A panel left alone in its window moves
  elsewhere with its client; kill and kill-session move it out first. The panel is no
  row, counts for no border line, and carries `@revive_skip`.
  `display-message -c CLIENT` does not read that client's pane (it takes
  the newest session's): read it through `list-clients`. Two views, Tab
  switches: the tree (opens first) lists each session's agent windows
  (flag, the agent's mark from its adapter's `Mark`, name, no index; a ?
  or ✓ row keeps its reason, the others show only the age) and folds the
  other windows into one row; Enter on a fold opens it, a search opens
  all. By state lists only agents under Needs you, Working, Done (✗
  included) and Idle, the session as a column. An idle agent's age counts
  from `@attention_idle_at`, the time its last flag was set. A split
  window lists its panes only when two or more run an agent or have a
  flag; with one, the window's row goes to that pane. A row's second line
  is its pane's labels (see below), dim, cut to the width. fzf only
  displays: `pick-rows --fzf` builds the rows (NUL-separated, a row can
  have two lines) and filters by session and window name, so a session's
  name keeps all its windows; `pick-key` gives Tab's and Enter's actions,
  and `place` the cursor after a reload.
  The view and the open folds are fzf's header label, which shows
  nowhere. Labels (session names, group titles) the cursor steps over.
  The client and the panel follow the cursor (`sidebar go`); Enter stays
  and closes the panel, Esc goes back to where it opened, ctrl-x kills the
  window or pane and reloads, C-l gives focus to the pane on the right
  (vim-tmux-navigator sends C-l to fzf).
- Labels (C1): what a source says about a pane, in the pane option
  `@labels` (a JSON list, one entry per source in the order they first
  reported). `tmux-agent report --source S --text T --token k=v --ttl D`
  sets one under the lock, `--clear` removes it; the core validates names
  (a-z, 0-9, `_`, `-`) and refuses control characters, and drops a label
  past its time. The panel shows each label's text (else its tokens'
  values), joined by ` · `; `list --json` gives them whole. The core knows
  no source.
- The git label (F4): every Claude hook carries the session's `cwd`, which
  follows the agent into a worktree (EnterWorktree, `claude -w`). At a
  session start, a turn end, or when the folder changes, the agent layer
  reads the branch and sets source `git`: `⎇ <branch>`, plus ` wt` in a
  linked worktree. It goes when the session ends (the hook, the launcher,
  the sweep); a folder outside git clears it.
- tmux-agent draws the flags: every change sets `@attention_mark` on the
  pane and `@attention_win_mark` on its window (the window shows its
  best-ranked pane), a ready-to-print glyph in its `@attention_fg_*` colour,
  or nothing. `tmux-init` redraws every mark and the counts, so prefix r
  applies new colours.
- A non-zero exit is a done flag with reason `failed: <cause>` and the pane
  option `@attention_failed`; its window gets `@attention_win_failed`. It
  still behaves as done (seen clears it, it ranks as done) but shows a ✗ in
  `@attention_fg_failed`, counts apart in the status, and notifies as
  "<window> failed". `tmux-agent flag done --failed` sets it by hand.
- An agent with no adapter gets only what the launcher knows: no flag while
  it runs, nothing on exit 0, failed on any other exit.
- A conformance case's expectation changes only after you show that the
  bash script behaves differently.
- In tests, the sandbox runs the test binary as tmux-agent
  (`selftest.Install`). Under `-race` those runs skip the race runtime's 1 s
  sleep at exit (`GORACE=atexit_sleep_ms=0`), and any race they report
  fails the test.

## Plugin: tmux-revive

Saves all of one tmux server on every change and brings it back after a
restart: sessions, windows, panes, layouts, names, folders, programs,
scrollback and each pane's `@revive_cmd`. It replaces tmux-resurrect,
tmux-continuum and dotfiles' tmux-attach. `tmux-revive --help` lists the
subcommands.

Packages in `tmux-revive/internal/`:

- `snapshot`: the snapshot types, their comparison (`Equal`, `SameShape`,
  `Lost`, `LostCommands`) and the store in `$XDG_STATE_HOME/tmux-revive/`:
  `snapshots/` (the newest `@revive_snapshots`, one JSON file each),
  `scrollback/<pane id>` and `log` (moved to `log.old` past 256 KiB). Folders 0700, files 0600; writes go
  through a temp file, fsync and rename.
- `revive`: save, restore, attach, forget, scrollback capture and paint,
  and typing a pane's command. Locks go in `$XDG_RUNTIME_DIR/tmux-revive/`.
- `cli`: the subcommands, and the hooks that `tmux-init` sets.
- `selftest`: lets a test binary stand in for tmux-revive.
- `conformance`: the attach rules of dotfiles' tmux-attach, run against
  the bash script (characterization) and tmux-revive.

Options: `@revive_ready` (server), `@revive_forgotten` (session),
`@revive_id` (window), `@revive_pane`, `@revive_cmd` and `@revive_skip`
(pane: not saved, and its cell left out of the window's layout; tmux-agent
sets it on the panel), and in
tmux.conf `@revive_programs`, `@revive_scrollback_lines` (2000),
`@revive_scrollback_interval` (300 s, 0 for off), `@revive_snapshots` (20)
and `@skip_sessions` (`ov-*`).

Rules:

- What closes tmux from outside (reboot, logout, kill-server, a crash)
  comes back in full. What the user closes inside tmux stays closed.
- tmux-init sets its hooks only at `[20]` (see tmux-agent). The save hooks
  run `tmux-revive save` in the background; `after-set-option` saves only
  for `@revive_cmd` and `automatic-rename` (`#{hook_argument_0}`).
- Sessions in `@skip_sessions` (tmux-overlay's `ov-*`) are not saved:
  their owner rebuilds them on demand. A save that cannot reach the
  server, or finds no sessions besides those, writes nothing: `kill-server` runs `session-closed` hooks as the server goes
  down. There is no `after-kill-session` hook in tmux 3.8. A save that
  loses panes, or a pane's command, while systemd reports `stopping`
  (system or `--user`) writes nothing either: programs die before tmux.
- Saves wait for `@revive_ready`. Attach and restore set it, even past
  their deadline, once the
  saved state is back, or once they find live work they must not restore
  onto; tmux-init sets it only on a server that already has sessions
  (prefix r, the first run after an upgrade). At server start tmux.conf
  runs before the first session, so a server started some other way
  cannot replace the snapshot a restore still needs.
- Saves coalesce: a save that finds another one queued exits; the queued
  one waits `--settle`, then takes the save lock. Restore holds the save
  lock, so no save sees a half-restored server.
- A save with the same state writes nothing. A save with the same
  sessions, windows and panes (other folders, layouts, names) replaces the
  newest snapshot, so resizes and `cd` do not push older states out.
- `forget-session` marks the session `@revive_forgotten` and saves before
  the session goes, so it stays closed after a restore.
- Restore adds only what is missing, by session name and window id. A
  restored pane whose `@revive_pane` a live pane holds gets a new id and no
  scrollback. A session, window or pane tmux refuses costs only itself; the
  rest are still made. It types `@revive_cmd` once into a pane: it waits
  for the shell's prompt, sends the text literally, checks the text shows,
  then sends Enter; it clears the line and tries again on a timeout. Text
  with a newline or another control character is not typed (the shell
  would act on it as it arrives) and shows as not typed. A program on the
  `@revive_programs` list starts again; a pane with scrollback runs
  `tmux-revive paint`, which waits (up to 3 s) until restore has placed
  the pane and its terminal has the pane's size, prints it and execs the
  login shell. tmux sets a terminal's size a moment after a layout: a zsh
  started at the old width shows a stray `%`.
- Names and folders that go into `-s`, `-n` or `-c` escape `#` as `##`;
  tmux expands formats there.
- Capture of a pane on the alternate screen (a full-screen program) saves
  what the program covers: the history, and the saved normal screen
  (`capture-pane -a`) up to the line above `#{alternate_saved_y}`. Capture
  removes files of panes neither live nor in a kept snapshot.
- Known gaps: `exit` in the last shell runs no hook; swap, move, join and
  break of panes or windows have no `after-` hooks in 3.8, so the next
  save picks them up; a pane has no `@revive_pane` until its first save,
  so capture skips it until then.

## How Dotfiles Consumes This

The dotfiles repo pins `TMUX_PLUGINS_VERSION` and the sha256 of the release's
`SHA256SUMS`, downloads the binaries and `tmux-warp.sh` into `~/.local/bin/`,
and checks them against `SHA256SUMS`. (Until the P0 cutover it still follows
`releases/latest` and fetches the wrapper from the tree at
`<tag>/tmux-warp/tmux-warp.sh`, which therefore stays at that path.)
tmux.conf binds: `bind s run-shell '~/.local/bin/tmux-warp.sh'`

tmux.conf places `@attention_status` in the status line and
`@attention_win_mark` in the window list; tmux-agent draws both. tmux.conf
sets only the colours, of the flags and of the panel.

At the cutover, tmux-revive takes over from tmux-resurrect, tmux-continuum
and tmux-attach. The terminal starts `tmux-revive attach`; tmux.conf runs
`tmux-revive tmux-init` and binds `C-r` to `tmux-revive restore` and `X` to
`forget-session` before `kill-session`; zshrc evals
`tmux-revive shell-init zsh`. The README has the exact lines.

## Images in tmux

tmux 3.8 shows no images: it only passes kitty escapes through
(`allow-passthrough on`), and Ghostty has no sixel. The decision is to wait
for native support in a tmux release (PR #5445, kept current with master on
the branch `image_merge`), not to use kitten's Unicode placeholders or a
self-built tmux. On 2026-11-11 that branch merged into master without
conflicts, detected Ghostty's `kittygraphics`, and passed `make test`. When
a release has it, run `make test` against it.

## Code Conventions

### Go
- No external Go dependencies (stdlib only) — keeps builds simple and
  binaries small
- tmux calls go through `internal/tmux`, which puts a timeout on each. The
  exceptions are tmux-warp's label `command-prompt`, which holds its client
  until answered and is started without waiting, and tmux-agent's
  `display-popup -E`, which runs through `Client.Interactive` and waits
  until the user closes the popup. `Option` returns ok=false
  only for an unset `@option`; any other failure is an error. An argument
  that ends in `;` is escaped, since tmux reads it as a command separator;
  only a lone `;` separates commands. tmux 3.8 refuses a command line over
  about 16 KiB or 1,000 arguments ("command too long"): a list that grows
  with the number of panes or windows goes in batches (`tmux.Batches`,
  `Client.RunAll`).

### Shell scripts
- All scripts use `#!/usr/bin/env bash` shebang
- Every script starts with `set -euo pipefail`
- Use `shellcheck`-clean bash
- Indent with 2 spaces, no tabs
- Functions use `snake_case`
- Quote all variable expansions

### Git
- P0 work happens on the `p0` branch and is merged before the v0.2.0 tag
- Before every commit/push, audit the staged diff for sensitive information
  leaks: usernames, passwords, API keys, tokens, private IPs, email
  addresses, or any data that should not appear in a public repository. Flag
  any findings to the user before proceeding

## References

- tmux man page: `capture-pane`, `send-keys -X`, `copy-mode`,
  `display-message`, `command-prompt`, `set-hook`

<!-- stack:start -->
## Stack

Chosen for this project with `stack add`. A reference under `repos/` is
read-only: never edit it, never import from it. How to work with each is
in its rule and skills. A dependency you add comes from the registry that
`stack list` shows. When a package there covers a need, run `stack add
<name>`; the user approves it first. When none does, ask the user before
you write that code yourself.

Code reaches anything outside the process (a chain, a database, an
external API, files) only through a facade: code of this project that
combines a library's calls into the operations the project needs, and
exposes them in the project's terms. Nothing outside the facade imports
the library or its types.

- **go-practices** (practices) — Before you write or change Go code, load the Go skill that fits the task with the Skill tool: go-fundamentals (types, naming, design), go-reliability (errors, concurrency, context, tests), go-security-perf (security, performance), go-database (PostgreSQL, pgx), go-tooling (CLI, linting, dependencies, Go versions). Skills `go-database`, `go-fundamentals`, `go-reliability`, `go-security-perf`, `go-tooling`.
<!-- stack:end -->
