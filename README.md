# tmux-plugins

Custom tmux plugins, written in Go, distributed as prebuilt binaries via GitHub Releases.

Each plugin lives in its own directory as a self-contained Go module. One binary per plugin.

## Plugins

### tmux-warp

A [flash.nvim](https://github.com/folke/flash.nvim)-style jump/navigation tool for tmux.
Captures the visible pane content, lets you search for text, highlights matches with
rainbow-colored labels, and jumps the cursor to the selected match in tmux copy mode.

Based on the original [tmux-warp](https://github.com/schasse/tmux-warp) Ruby plugin, rewritten in Go.

**Modes:**

- `tmux-warp --search` -- incremental search with live highlighting and auto-jump
- `tmux-warp --char f|F|t|T` -- single-character jump (like vim `f`/`t` motions, pane-wide)

**Features:** incremental narrowing, distance-based label assignment, auto-jump on single
match, rainbow labels (Catppuccin Mocha), selection preservation in copy mode.

## Installation

Download the latest binary for your architecture from
[GitHub Releases](https://github.com/influento/tmux-plugins/releases/latest):

```bash
# Example for linux/amd64
curl -fsSL https://github.com/influento/tmux-plugins/releases/latest/download/tmux-warp-linux-amd64 \
  -o ~/.local/bin/tmux-warp
chmod +x ~/.local/bin/tmux-warp
```

Then bind it in your `tmux.conf`:

```tmux
bind s run-shell '~/.local/bin/tmux-warp --search'
bind f run-shell '~/.local/bin/tmux-warp --char f'
bind F run-shell '~/.local/bin/tmux-warp --char F'
bind t run-shell '~/.local/bin/tmux-warp --char t'
bind T run-shell '~/.local/bin/tmux-warp --char T'
```

## Building from source

```bash
cd tmux-warp
go build -o tmux-warp .
```

No external dependencies -- stdlib only.

## License

MIT
