// Command check holds go-gate's own rules and the plumbing gate.sh needs.
// Every command runs in the project root, the first argument.
//
//	check <root> rules         the source rules; one line per finding with its fix
//	check <root> deps          go.mod and the imports against go-gate/deps.txt, and no-excluded
//	check <root> approved      the list a first install writes: go.mod's direct requires, tools, replaces
//	check <root> tests         reads `go test -json` on stdin: prints each failure, and fails a
//	                           package with test files where no test ran
//	check <root> config [--local]  golangci-lint's config for this run, on stdout
//	check <root> pkgs          the packages to test: ./... less the ones exclude covers
//	check <root> legacy        reads tool output on stdin: the files it names, leaf packages first
//	check <root> legacy-files  the files go-gate-legacy.txt lists
//	check <root> excluded      reads paths on stdin: those that no exclude glob covers
//	check <root> only-tests-args  deadcode's arguments for only-tests-use; none without a program
//	check <root> only-tests    reads deadcode's output on stdin: only-tests-use, one line per finding
package main

import (
	"fmt"
	"os"
)

const usage = "usage: check <root> rules|deps|approved|tests|config [--local]|pkgs|legacy|legacy-files|excluded|only-tests-args|only-tests"

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	err := os.Chdir(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(run(os.Args[2], os.Args[3:]))
}

func run(cmd string, args []string) int {
	conf := readConf()
	var n int
	switch cmd {
	case "rules":
		n = report(rules(conf))
	case "deps":
		found, err := deps(conf)
		if err != nil {
			fmt.Fprintln(os.Stdout, err)
			return 1
		}
		n = report(found)
	case "approved":
		return printLines(approved())
	case "tests":
		n = tests(os.Stdin, os.Stdout)
	case "config":
		return writeConfig(conf, len(args) > 0 && args[0] == "--local", os.Stdout)
	case "pkgs":
		return printLines(testPackages(conf))
	case "legacy":
		return printLines(legacyOrder(os.Stdin))
	case "legacy-files":
		return printLines(legacyFiles())
	case "excluded":
		return printLines(notExcluded(conf, os.Stdin))
	case "only-tests-args":
		return printLines(onlyTestsArgs(conf))
	case "only-tests":
		found, err := onlyTests(conf, os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stdout, err)
			return 1
		}
		n = report(found)
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if n > 0 {
		return 1
	}
	return 0
}

func printLines(lines []string, err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, l := range lines {
		fmt.Fprintln(os.Stdout, l)
	}
	return 0
}
