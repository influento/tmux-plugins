package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// event is one line of `go test -json` (cmd/test2json), with the build events of Go 1.24.
type event struct {
	Action      string `json:"Action"`
	Package     string `json:"Package"`
	Test        string `json:"Test"`
	Output      string `json:"Output"`
	ImportPath  string `json:"ImportPath"`
	FailedBuild string `json:"FailedBuild"`
}

type pkgRun struct {
	ran, failed, noFiles bool
	failedBuild          string
	out                  []string
	failedTests          map[string]bool
}

type testLine struct {
	pkg, test, text string
}

// tests reads `go test -json`, prints what each failure printed, and fails a package that has test
// files and ran no test: a TestMain without m.Run gives "ok" and exit 0 with a failing test in it.
func tests(r io.Reader, w io.Writer) int {
	pkgs := map[string]*pkgRun{}
	var order []string
	var lines []testLine
	build := map[string][]string{}
	get := func(name string) *pkgRun {
		p := pkgs[name]
		if p == nil {
			p = &pkgRun{failedTests: map[string]bool{}}
			pkgs[name] = p
			order = append(order, name)
		}
		return p
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			fmt.Fprintln(w, sc.Text())
			continue
		}
		if e.Action == "build-output" {
			build[e.ImportPath] = append(build[e.ImportPath], e.Output)
			continue
		}
		if e.Package == "" {
			continue
		}
		p := get(e.Package)
		switch e.Action {
		case "output":
			if e.Test != "" {
				lines = append(lines, testLine{e.Package, e.Test, e.Output})
				continue
			}
			p.out = append(p.out, e.Output)
			p.noFiles = p.noFiles || strings.Contains(e.Output, "[no test files]")
		case "run":
			p.ran = p.ran || e.Test != ""
		case "fail":
			if e.Test != "" {
				p.failedTests[topTest(e.Test)] = true
				continue
			}
			p.failed, p.failedBuild = true, e.FailedBuild
		}
	}
	n := 0
	for _, name := range order {
		p := pkgs[name]
		switch {
		case p.failed && p.failedBuild != "" && len(build[p.failedBuild]) > 0:
			fmt.Fprint(w, strings.Join(build[p.failedBuild], ""))
			n++
		case p.failed && len(p.failedTests) > 0:
			for _, l := range lines {
				if l.pkg == name && p.failedTests[topTest(l.test)] && !strings.HasPrefix(l.text, "=== ") {
					fmt.Fprint(w, l.text)
				}
			}
			n += len(p.failedTests)
		case p.failed:
			fmt.Fprint(w, strings.Join(p.out, ""))
			n++
		case !p.ran && !p.noFiles:
			fmt.Fprintf(w, "%s: no-test-ran: the package has test files and no test ran: a TestMain "+
				"without m.Run, or test files with no Test function\n", name)
			n++
		}
	}
	return n
}

func topTest(name string) string {
	top, _, _ := strings.Cut(name, "/")
	return top
}
