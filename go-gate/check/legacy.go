package main

import (
	"bufio"
	"io"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
)

var findingPath = regexp.MustCompile(`^(\S+?(?:\.go|/go\.mod)):\d+`)

// legacyOrder: the .go files that tool output names, a package's files after the packages it
// imports (go list -deps lists a package after its dependencies), so a rename a linter asks for
// touches each caller once. The order is only advice.
func legacyOrder(r io.Reader) ([]string, error) {
	files := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if m := findingPath.FindStringSubmatch(sc.Text()); m != nil && fileExists(m[1]) {
			files[path.Clean(m[1])] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	rank := map[string]int{}
	if pkgs, err := listPackages(true); err == nil {
		for _, p := range pkgs {
			if dir := relDir(p.Dir); p.Module != nil && p.Module.Main {
				if _, seen := rank[dir]; !seen {
					rank[dir] = len(rank)
				}
			}
		}
	}
	out := slices.Sorted(maps.Keys(files))
	sort.SliceStable(out, func(i, j int) bool {
		ri, iok := rank[path.Dir(out[i])]
		rj, jok := rank[path.Dir(out[j])]
		if iok != jok {
			return iok
		}
		return ri < rj
	})
	return out, nil
}
