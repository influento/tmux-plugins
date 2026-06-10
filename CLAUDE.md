# tmux-plugins

Custom tmux plugins, written in Go, distributed as prebuilt binaries via GitHub Releases.

## Repository Structure

```
tmux-plugins/
  tmux-warp/
    tmux-warp.sh     # shell wrapper — handles tmux command-prompt input
    main.go          # entry point — reads query from the @warp_query tmux option
    warp.go          # search overlay loop, label selection, screen save/restore
    tmux.go          # tmux command helpers, pane state capture, cursor jump
    match.go         # match finding, distance-based label assignment
    render.go        # ANSI rendering to pane TTY (Catppuccin Mocha colors)
    input.go         # tmux option polling, command-prompt spawning, debug logging
    go.mod
  .github/workflows/
    ci.yml           # build + vet on every push
    release.yml      # build + publish binaries on tag push (v*)
```

## Plugin: tmux-warp

Search-and-jump tool for tmux. Type a char or word, see matches with labels, press label to jump.

### Architecture (follows tmux-jump pattern)

1. **Shell wrapper** (`tmux-warp.sh`): clears `@warp_query`, then calls
   `tmux command-prompt` to store the search query (char or word) into the
   `@warp_query` tmux option, then invokes the Go binary
2. **Go binary** (`tmux-warp`): polls the `@warp_query` option for the query, finds
   matches in pane content, renders overlay with labels to pane TTY, spawns
   `command-prompt -1` for label selection into `@warp_label` (multi-char labels
   prompt iteratively), then jumps cursor via `tmux copy-mode` + `send-keys -X`

Key constraint: binding MUST use `run-shell -b` (background) so tmux stays free to
process `command-prompt` input. Without `-b`, tmux blocks and input never arrives.

### How input works

- Search input goes through `tmux command-prompt` (no `-1`) which captures a full
  string (char or word) via tmux's event loop — NOT by reading from the pane TTY
- Result is stored via `set-option -g @warp_query '%1'` — NOT `run-shell`/`printf`.
  command-prompt substitutes `%1` textually and re-parses the result, so sending it
  through a shell (`run-shell`) lets query metacharacters (`$ ; " ` + backtick)
  execute. A tmux option has no shell layer, so the query is stored literally.
  (Residual: a literal `'` in the query can still break the option's tmux quoting —
  accepted; do not paste untrusted text into the prompt.)
- Go binary polls `show-options -gv @warp_query` with 10ms sleeps, 10s timeout; the
  option is unset until submit (show-options exits non-zero), then holds the value.
  The binary clears the option after reading; the wrapper also clears it before the
  prompt to avoid a stale read
- For label selection after overlay is shown, Go spawns `command-prompt -1` via
  `exec.Command` fire-and-forget (`cmd.Start()` + `go cmd.Wait()`), storing the key
  into `@warp_label` the same way
- Multi-char labels: prompts iteratively, collecting one char at a time until an
  exact label match is found or no labels share the prefix
- `command-prompt` must NOT use `-t paneID` — it takes a target-client, not a pane;
  passing a pane ID silently breaks input. Omit `-t` entirely (matches tmux-jump)

### Debug logging

Off by default. Set `TMUX_WARP_DEBUG=1` to enable. Logs go to
`$XDG_RUNTIME_DIR/tmux-warp.log` (falling back to `$TMPDIR/tmux-warp-<uid>.log`),
created with mode `0600` and `O_NOFOLLOW`; override the path with `TMUX_WARP_LOG`.
Shows pane state, chars received, match counts, jump coordinates. Check this
first when debugging.

## Build & Release

- Local: `cd tmux-warp && go build -o tmux-warp .`
- CI builds on every push (linux/amd64 + linux/arm64), uploads artifacts
- Tag push (`v*`) triggers release with binaries as GitHub Release assets
- Tags are auto-created by a `.git/hooks/pre-push` hook that increments patch version
- Binary names: `tmux-warp-linux-amd64`, `tmux-warp-linux-arm64`

## How Dotfiles Consumes This

The dotfiles repo downloads the latest binary and shell wrapper, places them in
`~/.local/bin/`. Both `tmux-warp` (binary) and `tmux-warp.sh` (wrapper) are needed.
tmux.conf binds: `bind s run-shell -b '~/.local/bin/tmux-warp.sh'`

## Code Conventions

### Go
- No external Go dependencies (stdlib only) — keeps builds simple and binaries small
- Follow the Go Claude skills (go-fundamentals, go-reliability, go-tooling) for all
  style, error handling, and tooling conventions

### Shell scripts
- All scripts use `#!/usr/bin/env bash` shebang
- Every script starts with `set -euo pipefail`
- Use `shellcheck`-clean bash
- Indent with 2 spaces, no tabs
- Functions use `snake_case`
- Quote all variable expansions

### Git
- Never add `Co-Authored-By` trailers to git commits
- Before every commit/push, audit the staged diff for sensitive information leaks:
  usernames, passwords, API keys, tokens, private IPs, email addresses, or any
  data that should not appear in a public repository. Flag any findings to the user
  before proceeding

## References

- tmux man page: `capture-pane`, `send-keys -X`, `copy-mode`, `display-message`,
  `command-prompt`
