package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	initDebugLog()

	args := os.Args[1:]
	if len(args) >= 1 && args[0] == "--version" {
		fmt.Println(version)
		return
	}

	// The search query is read from the @warp_query tmux option, set by the
	// shell wrapper's command-prompt. Any positional argument from an older
	// wrapper is ignored.
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "tmux-warp: %v\n", err)
		os.Exit(1)
	}
}
