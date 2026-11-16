package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
)

const gateConfig = "go-gate/golangci.gate.yml"

// The gate's own rules: go-gate.off.<rule> turns one off in check's output, any other name is a
// golangci-lint linter.
var ownRules = map[string]bool{
	"no-ignore-comment": true, "no-hidden-file": true, "no-disabled-test": true, "no-any-signature": true,
	"no-excluded": true,
}

// exclusionRule is one of golangci-lint's linters.exclusions.rules.
type exclusionRule struct {
	Path    string   `json:"path"`
	Linters []string `json:"linters"`
}

func jsonStrings(v []string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "[]"
	}
	return strings.TrimSpace(b.String())
}

func jsonRule(r exclusionRule) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return "{}"
	}
	return strings.TrimSpace(b.String())
}

// writeConfig writes the config golangci-lint runs with: golangci.gate.yml with each `# go-gate:`
// line filled. golangci-lint has no config inheritance, so no project file can add to the gate's.
// JSON is YAML, so each filled value is JSON. With local, no formatter runs: --local format-checks
// the changed files alone, with `golangci-lint fmt`.
func writeConfig(s settings, local bool, w io.Writer) int {
	data, err := os.ReadFile(gateConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	paths := []string{}
	for _, g := range s.globs {
		paths = append(paths, globRegexp(g))
	}
	legacy, _ := legacyFiles()
	for _, p := range legacy {
		paths = append(paths, "^"+regexp.QuoteMeta(p)+"$")
	}
	seen := map[string]bool{}
	var out []string
	for line := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		switch {
		case strings.HasSuffix(line, "# go-gate:paths"):
			seen["paths"] = true
			line = indent + "paths: " + jsonStrings(paths)
		case strings.HasSuffix(line, "# go-gate:rules"):
			seen["rules"] = true
			line = ""
			for _, name := range slices.Sorted(maps.Keys(s.off)) {
				if ownRules[name] {
					continue
				}
				res := make([]string, 0, len(s.offGlob[name]))
				for _, g := range s.offGlob[name] {
					res = append(res, "(?:"+globRegexp(g)+")")
				}
				out = append(out, indent+"- "+jsonRule(exclusionRule{strings.Join(res, "|"), []string{name}}))
			}
		case strings.HasSuffix(line, "# go-gate:formatters"):
			seen["formatters"] = true
			if local {
				line = indent + "enable: []"
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	for _, mark := range []string{"paths", "rules", "formatters"} {
		if !seen[mark] {
			fmt.Fprintf(os.Stderr, "%s lacks its `# go-gate:%s` line: install go-gate again\n", gateConfig, mark)
			return 1
		}
	}
	fmt.Fprintln(w, strings.Join(out, "\n"))
	return 0
}
