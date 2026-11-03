// Command tmux-agent shows what each agent pane needs, and starts agents
// through one launcher. See internal/cli for the subcommands.
package main

import (
	"os"

	"github.com/influento/tmux-plugins/tmux-agent/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args, os.Stdin, os.Stdout, os.Stderr))
}
