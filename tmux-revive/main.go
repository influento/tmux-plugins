// Command tmux-revive saves all of tmux on every change and restores it
// after a restart. P0 step 3 builds it.
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
	fmt.Fprintln(os.Stderr, "usage: tmux-revive --version")
	os.Exit(2)
}
