# tmux-plugins

Custom tmux plugins, written in Go, released together as static binaries
through GitHub Releases. Linux amd64 and arm64. No dependencies beyond tmux
(and fzf for the agent pickers).

## Plugins

### tmux-warp

A [flash.nvim](https://github.com/folke/flash.nvim)-style jump tool for tmux.
Captures the visible pane content, lets you search for text, highlights
matches with rainbow-colored labels, and jumps the cursor to the selected
match in tmux copy mode.

**Features:** distance-based label assignment, auto-jump on single match,
rainbow labels (Catppuccin Mocha).

### tmux-agent

Shows what each agent pane needs: flags on windows and borders, counts in the
status bar, a picker and a jump key, desktop notifications. Agents start
through one launcher; Claude Code is the first integration.
`tmux-agent --help` lists the subcommands.

### tmux-revive

Saves all of tmux on every change and restores it after a restart: sessions,
windows, layouts, names, folders, programs (nvim, less, man, ssh, ...),
scrollback, and each pane's `@revive_cmd`, which tmux-agent sets to resume an
agent's conversation. Whatever closes tmux from outside (a reboot, logout,
`kill-server`, a crash) comes back in full; what you close inside tmux stays
closed. `tmux-revive --help` lists the subcommands.

```tmux
# last lines of tmux.conf: tmux-revive's hooks, at index [20]
run-shell 'command -v tmux-revive >/dev/null && tmux-revive tmux-init || true'
bind C-r run-shell 'tmux-revive restore'
bind X confirm-before "run-shell 'tmux-revive forget-session \"#S\"; tmux kill-session -t \"=#S\"'"
```

The terminal runs `tmux-revive attach` instead of `tmux new-session -A`: it
restores once per server, before any shell starts, then attaches. In zsh,
`eval "$(tmux-revive shell-init zsh)"` saves the folder on each `cd`.

## Installation

Each release carries every binary for both architectures, `tmux-warp.sh`,
and `SHA256SUMS`. Pin a version and check the files against it:

```bash
version=v0.2.0 arch=amd64
base=https://github.com/influento/tmux-plugins/releases/download/$version
cd "$(mktemp -d)"
curl -fsSLO "$base/SHA256SUMS"
curl -fsSLO "$base/tmux-warp-linux-$arch"
curl -fsSLO "$base/tmux-warp.sh"
sha256sum --check --ignore-missing SHA256SUMS
install -m 0755 "tmux-warp-linux-$arch" ~/.local/bin/tmux-warp
install -m 0755 tmux-warp.sh ~/.local/bin/tmux-warp.sh
```

Then bind it in your `tmux.conf`:

```tmux
bind s run-shell '~/.local/bin/tmux-warp.sh'
```

The binding works in the foreground and with `run-shell -b`.

## Building from source

```bash
make build        # bin/tmux-warp, bin/tmux-agent, bin/tmux-revive
make test         # go vet, gofmt check, unit and tmux integration tests
make dev          # link the builds into ~/.local/bin (make undev reverses it)
```

The integration tests start private tmux servers in a sandbox; they need
`tmux` and `script` (util-linux) and skip without them.

## License

MIT
