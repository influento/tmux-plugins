// Command tmux-revive saves all of tmux on every change and restores it
// after a restart. tmux-revive --help lists the subcommands.
package main

import (
	"os"

	"github.com/influento/tmux-plugins/tmux-revive/internal/cli"
)

// version is set at build time: -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args[1:], os.Stdout, os.Stderr))
}
