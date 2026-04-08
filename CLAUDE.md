# tmux-plugins

Custom tmux plugins, written in Go, distributed as prebuilt binaries via GitHub Releases.

## Repository Structure

```
tmux-plugins/
  <plugin-name>/       # self-contained Go module per plugin
    main.go
    go.mod
  .github/workflows/
    ci.yml             # build on every push
    release.yml        # build + publish on tag push
```

## Build & Release

- Local: `cd <plugin> && go build -o <plugin> .`
- CI builds on every push (linux/amd64 + linux/arm64)
- Tag push (`v*`) triggers release: builds binaries, creates GitHub Release with assets
- Binary names: `<plugin>-linux-amd64`, `<plugin>-linux-arm64`

## How Dotfiles Consumes This

The dotfiles repo downloads the latest binary from GitHub Releases and places it in
`~/.local/bin/`. Binaries must be static, self-contained executables (no runtime deps).

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

- tmux man page: `capture-pane`, `send-keys -X`, `copy-mode`, `display-message`
