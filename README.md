# tmux-plugins

Custom tmux plugins, written in Go, distributed as prebuilt binaries via GitHub Releases.

Each plugin lives in its own directory as a self-contained Go module. One binary per plugin.

## Plugins

### tmux-warp

A [flash.nvim](https://github.com/folke/flash.nvim)-style jump/navigation tool for tmux.
Captures the visible pane content, lets you search for text, highlights matches with
rainbow-colored labels, and jumps the cursor to the selected match in tmux copy mode.

**Features:** distance-based label assignment, auto-jump on single match, rainbow labels
(Catppuccin Mocha).

## Installation

Download the latest binary for your architecture from
[GitHub Releases](https://github.com/influento/tmux-plugins/releases/latest):

```bash
# Example for linux/amd64
curl -fsSL https://github.com/influento/tmux-plugins/releases/latest/download/tmux-warp-linux-amd64 \
  -o ~/.local/bin/tmux-warp
chmod +x ~/.local/bin/tmux-warp

# Also grab the shell wrapper
curl -fsSL https://raw.githubusercontent.com/influento/tmux-plugins/main/tmux-warp/tmux-warp.sh \
  -o ~/.local/bin/tmux-warp.sh
chmod +x ~/.local/bin/tmux-warp.sh
```

Then bind it in your `tmux.conf`:

```tmux
bind s run-shell -b '~/.local/bin/tmux-warp.sh'
```

The `-b` flag is required -- it runs the script in the background so tmux can process
the `command-prompt` for keyboard input.

## Building from source

```bash
cd tmux-warp
go build -o tmux-warp .
```

No external dependencies -- stdlib only.

## License

MIT
