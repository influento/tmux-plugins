// Command tmux-agent shows what each agent pane needs and starts agents
// through one launcher. P0 step 2 ports it from the dotfiles scripts.
package main

import (
	"fmt"
	"os"
)

// version is set at build time: -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: tmux-agent --version")
	os.Exit(2)
}
