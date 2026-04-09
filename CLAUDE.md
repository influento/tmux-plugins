# tmux-plugins

Custom tmux plugins, written in Go, distributed as prebuilt binaries via GitHub Releases.

## Repository Structure

```
tmux-plugins/
  tmux-warp/
    tmux-warp.sh     # shell wrapper — handles tmux command-prompt input
    main.go          # entry point — receives temp file path from wrapper
    warp.go          # search overlay loop, label selection, screen save/restore
    tmux.go          # tmux command helpers, pane state capture, cursor jump
    match.go         # match finding, distance-based label assignment
    render.go        # ANSI rendering to pane TTY (Catppuccin Mocha colors)
    input.go         # temp file polling, command-prompt spawning, debug logging
    go.mod
  .github/workflows/
    ci.yml           # build + vet on every push
    release.yml      # build + publish binaries on tag push (v*)
```

## Plugin: tmux-warp

Search-and-jump tool for tmux. Type a char or word, see matches with labels, press label to jump.

### Architecture (follows tmux-jump pattern)

1. **Shell wrapper** (`tmux-warp.sh`): calls `tmux command-prompt` to capture search
   query (char or word) into a temp file, then invokes the Go binary with the temp file path
2. **Go binary** (`tmux-warp`): polls temp file for the query, finds matches in pane
   content, renders overlay with labels to pane TTY, spawns `command-prompt -1`
   for label selection (multi-char labels prompt iteratively), then jumps cursor
   via `tmux copy-mode` + `send-keys -X`

Key constraint: binding MUST use `run-shell -b` (background) so tmux stays free to
process `command-prompt` input. Without `-b`, tmux blocks and input never arrives.

### How input works

- Search input goes through `tmux command-prompt` (no `-1`) which captures a full
  string (char or word) via tmux's event loop — NOT by reading from the pane TTY
- Result is written to a temp file via `run-shell "printf '%1' >> <file>"`
- Go binary polls the temp file with 10ms sleep intervals, 10s timeout
- For label selection after overlay is shown, Go spawns `command-prompt -1` via
  `exec.Command` fire-and-forget (`cmd.Start()` + `go cmd.Wait()`)
- Multi-char labels: prompts iteratively, collecting one char at a time until an
  exact label match is found or no labels share the prefix
- `command-prompt` must NOT use `-t paneID` — it takes a target-client, not a pane;
  passing a pane ID silently breaks input. Omit `-t` entirely (matches tmux-jump)
- `run-shell` arg in `exec.Command` must use single quotes, not escaped double
  quotes — there's no shell to process the escapes

### Debug logging

Binary writes to `/tmp/tmux-warp.log` — shows pane state, chars received, match
counts, jump coordinates. Check this first when debugging.

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
