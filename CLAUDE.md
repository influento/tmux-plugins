# tmux-plugins

Custom tmux plugins, written in Go, released together as prebuilt binaries
through GitHub Releases. The repository is public.

## Repository Structure

```
tmux-plugins/
  go.mod               # one module for all plugins: github.com/influento/tmux-plugins
  Makefile             # build, vet, test, dist, dev/undev, release
  internal/tmux/       # shared tmux client (timeouts on every call)
    tmuxtest/          # private-server test harness
  tmux-warp/           # search-and-jump; tmux-warp.sh stays at this path
  tmux-agent/          # agent flags, pickers, hooks, launcher (P0 step 2)
    internal/          # its packages; the compiler keeps them from tmux-revive
  tmux-revive/         # save and restore all of tmux (P0 step 3)
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
  run the bash scripts from the dotfiles checkout in `DOTFILES` (default
  `../dotfiles`), exported to the tests as `TMUX_PLUGINS_DOTFILES`; without it
  they skip.
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
  and XDG directories, and stubs for `notify-send`, `claude` and `swaymsg`
  first on PATH; `Calls(name)` returns what each stub received, parallel
  calls included. The sandbox environment is an allowlist, so nothing
  reaches the user's desktop, tmux server or agent session, and its PATH
  leaves out the user's home, so installed copies in `~/.local/bin` never
  run in place of the code under test.
- The socket goes in a short directory with `-S`: a unix socket path must fit
  in 108 bytes, and `t.TempDir()` paths do not.
- Servers start with `-f` and a test config, never the user's.
- `Attach` gives a real client through `script(1)` for tests that need one
  (command-prompt, popups, client focus); the standard library has no PTY.
  `Type` sends keys to it.
- `InPane` points plain `tmux` calls in the test process at the private
  server and a pane, for in-process tests of code that runs `tmux`.

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
  the nested-agent rule, the title sweep. Knows no particular agent.
- `agent/claude`: the Claude Code adapter: hook mapping, one-shot and resume
  arguments, the window title from the first prompt.
- `store`: one JSON record per session in
  `$XDG_STATE_HOME/tmux-agent/sessions/<agent>/<id>.json` (mode 0600, never
  deleted), and the lock.
- `ui`: fzf, notify-send, swaymsg. `cli`: the subcommands, wiring the rest.
- `selftest`: lets a test binary stand in for tmux-agent.
- `conformance`: one suite, two drivers: the Go code, and dotfiles' bash
  scripts (characterization). `contract`: the launcher in a real pane with
  a stub agent: exit codes, signals, Ctrl-C, Ctrl-Z, one-shot, resume,
  nested agents.

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
- A client has focus when `#{client_flags}` contains `focused`; tmux 3.8 has
  no `client_focused`.
- The launcher re-execs itself with the agent's name as argv[0]: tmux names
  a pane after the argv[0] of the terminal's foreground process group
  leader. It catches Ctrl-C and Ctrl-\, passes SIGTERM and SIGHUP on, stops
  itself when the agent stops itself (Ctrl-Z in a TUI), and dies from the
  agent's signal.
- A hook counts for a run only when its nearest agent process is the run's
  own; a launcher started inside an agent runs the agent in its place.
- A one-shot run (`claude -p`) is flagged working at launch and done or
  failed at exit by the launcher; its own hooks only name the session.
- A non-zero exit is a done flag with reason `failed: <cause>`, not a state
  of its own.
- A conformance case's expectation changes only after you show that the
  bash script behaves differently.
- In tests, the sandbox runs the test binary as tmux-agent
  (`selftest.Install`). Under `-race` those runs skip the race runtime's 1 s
  sleep at exit (`GORACE=atexit_sleep_ms=0`), and any race they report
  fails the test.

## Plugin: tmux-revive

P0 step 3. Its `main.go` only answers `--version` so far. Rules already
fixed:

- tmux-init sets its hooks only at `[20]` (see above).
- A tmux-revive save that cannot reach the server, or finds no sessions,
  writes nothing: `kill-server` runs `session-closed` hooks as the server
  goes down. There is no `after-kill-session` hook in tmux 3.8.

## How Dotfiles Consumes This

The dotfiles repo pins `TMUX_PLUGINS_VERSION` and the sha256 of the release's
`SHA256SUMS`, downloads the binaries and `tmux-warp.sh` into `~/.local/bin/`,
and checks them against `SHA256SUMS`. (Until the P0 cutover it still follows
`releases/latest` and fetches the wrapper from the tree at
`<tag>/tmux-warp/tmux-warp.sh`, which therefore stays at that path.)
tmux.conf binds: `bind s run-shell '~/.local/bin/tmux-warp.sh'`

## Code Conventions

### Go
- No external Go dependencies (stdlib only) — keeps builds simple and
  binaries small
- Before you write or change Go code, load the Go skill that fits the task with the Skill tool: go-fundamentals (types, naming, design), go-reliability (errors, concurrency, context, tests), go-infra (database, security, performance), go-tooling (CLI, linting, dependencies, Go versions).
- tmux calls go through `internal/tmux`, which puts a timeout on each. The
  one exception is tmux-warp's label `command-prompt`, which holds its client
  until answered and is started without waiting. `Option` returns ok=false
  only for an unset `@option`; any other failure is an error. An argument
  that ends in `;` is escaped, since tmux reads it as a command separator;
  only a lone `;` separates commands.

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
