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
status bar, a side panel (`prefix f`) with two views on Tab (each session's
agent windows with the other windows folded, or the agents by state, each
with its labels: the branch, and what tools report through `tmux-agent
report`) and a jump key, desktop notifications. Agents start
through one launcher; Claude Code is the first integration.
`tmux-agent --help` lists the subcommands.

The panel is a tmux pane at the left edge, full height, 40 columns, that
follows its client to every window and session; the same key closes it.
`tmux-agent tmux-init` sets its hooks. These keys move the panel and switch
in one step, so tmux draws once; with no panel open they do what tmux's
own keys do. With the focus in the panel, `|` and `-` split the main pane
you had last, in its folder:

```tmux
bind f run-shell -b "tmux-agent popup #{client_name}"
bind n if -F '#{@sidebar_open}' "run-shell \"tmux-agent sidebar step '#{client_name}' +\"" 'next-window'
bind p if -F '#{@sidebar_open}' "run-shell \"tmux-agent sidebar step '#{client_name}' -\"" 'previous-window'
bind 1 if -F '#{@sidebar_open}' "run-shell \"tmux-agent sidebar step '#{client_name}' 1\"" 'select-window -t :=1'
# ... the same for 2 to 9
bind | if -F '#{&&:#{@sidebar_client},#{P:#{?#{==:#{pane_id},#{@sidebar_last}},1,}}}' "run-shell -C \"select-pane -t #{@sidebar_last} ; split-window -h -c '##{pane_current_path}'\"" 'split-window -h -c "#{pane_current_path}"'
bind - if -F '#{&&:#{@sidebar_client},#{P:#{?#{==:#{pane_id},#{@sidebar_last}},1,}}}' "run-shell -C \"select-pane -t #{@sidebar_last} ; split-window -v -c '##{pane_current_path}'\"" 'split-window -v -c "#{pane_current_path}"'
bind Space if -F '#{@sidebar_open}' "run-shell \"tmux-agent sidebar layout '#{client_name}' next\"" 'next-layout'
bind M-1 if -F '#{@sidebar_open}' "run-shell \"tmux-agent sidebar layout '#{client_name}' even-horizontal\"" 'select-layout even-horizontal'
# ... M-2 even-vertical, M-3 main-horizontal, M-4 main-vertical, M-5 tiled
# optional: the panel's colours, while it has the focus, and its cursor row
set -g @sidebar_style 'bg=#1e1e2e,fg=#a6adc8'
set -g @sidebar_active_style 'bg=#1e1e2e,fg=#cdd6f4'
set -g @sidebar_cursor_style 'bg=#45475a,fg=#b4befe'
# optional: no label line above the panes of a split window
set -g @attention_borders off
```

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
