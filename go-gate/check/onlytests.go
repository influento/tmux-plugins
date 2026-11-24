package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var deadcodeLine = regexp.MustCompile(`^(.+?):(\d+):\d+: unreachable func: (.+)$`)

// onlyScope: the project's packages, by what only-tests-use makes of them. A package outside
// internal/ that is not main is a library's API, called from outside, which deadcode cannot start
// from: it and every package it imports are library. Only an internal below the module's own path
// keeps a package to the module.
type onlyScope struct {
	ours             map[string]listedPkg
	mains, public    []string
	reached, library map[string]bool
	// cgoOff: a package with cgo files while cgo is off. go list then counts them among the ignored
	// files and deadcode cannot load them, so a program of them would be no entry point: the rule
	// does not run.
	cgoOff string
}

func loadScope(s settings) (onlyScope, error) {
	sc := onlyScope{ours: map[string]listedPkg{}}
	pkgs, err := listPackages(false)
	if err != nil {
		return sc, err
	}
	if sc.cgoOff, err = cgoOffPkg(s); err != nil || sc.cgoOff != "" {
		return sc, err
	}
	for _, p := range pkgs {
		if p.Module == nil || !p.Module.Main || excludedPkg(s, p) || len(p.GoFiles)+len(p.CgoFiles) == 0 {
			continue
		}
		sc.ours[p.ImportPath] = p
		switch {
		case p.Name == "main":
			sc.mains = append(sc.mains, p.ImportPath)
		case !internalPkg(p):
			sc.public = append(sc.public, p.ImportPath)
		}
	}
	if sc.reached, err = depsOf(sc.mains); err != nil {
		return sc, err
	}
	sc.library, err = depsOf(sc.public)
	return sc, err
}

// cgoOffPkg: when cgo is off, the first of the project's packages that has cgo files with cgo on.
func cgoOffPkg(s settings) (string, error) {
	out, err := goCmd("env", "CGO_ENABLED")
	if err != nil || strings.TrimSpace(string(out)) == "1" {
		return "", err
	}
	pkgs, err := listPackages(false, "CGO_ENABLED=1")
	if err != nil {
		return "", err
	}
	for _, p := range pkgs {
		if p.Module != nil && p.Module.Main && !excludedPkg(s, p) && len(p.CgoFiles) > 0 {
			return p.ImportPath, nil
		}
	}
	return "", nil
}

func internalPkg(p listedPkg) bool {
	return slices.Contains(strings.Split(strings.TrimPrefix(p.ImportPath, p.Module.Path), "/"), "internal")
}

// deadcodeArgs: deadcode's arguments for only-tests-use, as py-gate's rule and ts-gate's knip run
// in production mode, or none when the project has no program. deadcode (golang.org/x/tools)
// starts from each main package of the project, tests not counted, and reports each function and
// method no program reaches: only tests use it, or nothing does. It reports only in the packages a
// program imports and that are not library, which leaves out packages that only tests import (test
// helpers): onlyTests judges those. -generated: a generated-code header hides nothing, and deadcode
// skips such a file without it (probed).
func deadcodeArgs(sc onlyScope) []string {
	var scope []string
	for _, p := range slices.Sorted(maps.Keys(sc.ours)) {
		if sc.reached[p] && !sc.library[p] {
			scope = append(scope, regexp.QuoteMeta(p))
		}
	}
	if len(sc.mains) == 0 || len(scope) == 0 {
		return nil
	}
	return append([]string{"-generated", "-filter=^(" + strings.Join(scope, "|") + ")$"}, sc.mains...)
}

// onlyTests runs deadcode, the binary at bin, when the project has a program: a finding for each
// function it names, and for each unused package (unusedPkgs), but in the files exclude covers.
func onlyTests(s settings, bin string) ([]finding, error) {
	scope, err := loadScope(s)
	if err != nil {
		return nil, err
	}
	if scope.cgoOff != "" {
		fmt.Printf("go-gate: cgo is off, so only-tests-use does not run: %s has cgo files. Install a C "+
			"compiler (gcc) to turn it on.\n", scope.cgoOff)
		return nil, nil
	}
	var out []byte
	if args := deadcodeArgs(scope); args != nil {
		out, err = exec.CommandContext(context.Background(), bin, args...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%sdeadcode failed: %w", out, err)
		}
	}
	var found []finding
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		m := deadcodeLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		// A file outside the project is the go command's own, as the code cgo generates in the build cache.
		file := filepath.ToSlash(filepath.Clean(m[1]))
		if filepath.IsAbs(file) || strings.HasPrefix(file, "../") {
			continue
		}
		line, _ := strconv.Atoi(m[2])
		found = append(found, finding{
			file, line, "only-tests-use",
			fmt.Sprintf("no program here calls %s: only tests use it, or nothing does. If a program "+
				"needs it, call it from there; if not, delete it, and test through the code the "+
				"programs run", m[3]),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	pkgs, err := unusedPkgs(scope)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(append(found, pkgs...), func(f finding) bool { return matches(s.exclude, f.file) }), nil
}

// unusedPkgs: each internal package that no program, no library package and no other package's
// tests import. Only its own tests use it, or nothing does; deadcode never reports in it, since no
// program imports it. A package that another package's tests import is a test helper, and so is
// what it imports. A package whose files outside its tests declare nothing is a package of tests.
func unusedPkgs(sc onlyScope) ([]finding, error) {
	var helpers []string
	for path, p := range sc.ours {
		for _, imp := range slices.Concat(p.TestImps, p.XTestImps) {
			if imp != path && sc.ours[imp].ImportPath != "" {
				helpers = append(helpers, imp)
			}
		}
	}
	helped, err := depsOf(slices.Compact(slices.Sorted(slices.Values(helpers))))
	if err != nil {
		return nil, err
	}
	var found []finding
	for _, path := range slices.Sorted(maps.Keys(sc.ours)) {
		p := sc.ours[path]
		if p.Name == "main" || !internalPkg(p) || sc.reached[path] || sc.library[path] || helped[path] {
			continue
		}
		file, line := codeFile(p)
		if file == "" {
			continue
		}
		found = append(found, finding{relDir(file), line, "only-tests-use", fmt.Sprintf(
			"no program or library package imports %s: only tests use it, or nothing does. If a "+
				"program needs it, import it from there; if not, delete the package, and test through "+
				"the code the programs run", path)})
	}
	return found, nil
}

// codeFile: the first of p's files outside its tests that declares something besides imports, and
// the line of its package clause; "" when none does. A file that does not parse counts as code: the
// build reports it.
func codeFile(p listedPkg) (string, int) {
	for _, name := range slices.Concat(p.GoFiles, p.CgoFiles) {
		file := filepath.Join(p.Dir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			return file, 1
		}
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); !ok || g.Tok != token.IMPORT {
				return file, fset.Position(f.Package).Line
			}
		}
	}
	return "", 0
}

// depsOf: the packages that pkgs import, directly or not, pkgs included; tests not counted.
func depsOf(pkgs []string) (map[string]bool, error) {
	set := map[string]bool{}
	if len(pkgs) == 0 {
		return set, nil
	}
	out, err := goCmd(append([]string{"list", "-e", "-deps", "-f", "{{.ImportPath}}"}, pkgs...)...)
	if err != nil {
		return nil, err
	}
	for l := range strings.Lines(string(out)) {
		set[strings.TrimSpace(l)] = true
	}
	return set, nil
}
