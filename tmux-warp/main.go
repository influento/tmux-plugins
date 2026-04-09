package main

import (
	"fmt"
	"os"
)

var version = "dev"

func parseFlags() (mode string, charMode string) {
	args := os.Args[1:]

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: tmux-warp --search | --char <f|F|t|T> | --version\n")
		os.Exit(1)
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--version":
			fmt.Println(version)
			os.Exit(0)
		case "--search":
			return "search", ""
		case "--char":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "tmux-warp: --char requires argument (f|F|t|T)\n")
				os.Exit(1)
			}
			i++
			m := args[i]
			if m != "f" && m != "F" && m != "t" && m != "T" {
				fmt.Fprintf(os.Stderr, "tmux-warp: --char must be one of: f, F, t, T\n")
				os.Exit(1)
			}
			return "char", m
		default:
			fmt.Fprintf(os.Stderr, "tmux-warp: unknown flag: %s\n", args[i])
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "Usage: tmux-warp --search | --char <f|F|t|T> | --version\n")
	os.Exit(1)
	return "", ""
}

func main() {
	initDebugLog()
	debugLog("starting tmux-warp %s, args=%v", version, os.Args)
	if err := run(); err != nil {
		fatal(err)
	}
}
