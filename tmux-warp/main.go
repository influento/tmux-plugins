package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	initDebugLog()

	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(version)
		return
	}

	// Expected usage: tmux-warp <tmp-file>
	// The shell wrapper creates a temp file, calls tmux command-prompt to
	// capture the first search char into it, then invokes this binary.
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Usage: tmux-warp <tmp-file> | tmux-warp --version\n")
		os.Exit(1)
	}

	if err := run(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "tmux-warp: %v\n", err)
		os.Exit(1)
	}
}
