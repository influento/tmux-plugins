package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const depsFile = "go-gate/deps.txt"

type modVersion struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
}

type replace struct {
	Old modVersion `json:"Old"`
	New modVersion `json:"New"`
}

// goMod is what `go mod edit -json` prints.
type goMod struct {
	Require []struct {
		Path     string `json:"Path"`
		Indirect bool   `json:"Indirect"`
	} `json:"Require"`
	Replace []replace `json:"Replace"`
	Tool    []struct {
		Path string `json:"Path"`
	} `json:"Tool"`
}

type listedPkg struct {
	ImportPath string   `json:"ImportPath"`
	Name       string   `json:"Name"`
	Dir        string   `json:"Dir"`
	Standard   bool     `json:"Standard"`
	GoFiles    []string `json:"GoFiles"`
	CgoFiles   []string `json:"CgoFiles"`
	TestFiles  []string `json:"TestGoFiles"`
	XTestFiles []string `json:"XTestGoFiles"`
	Imports    []string `json:"Imports"`
	TestImps   []string `json:"TestImports"`
	XTestImps  []string `json:"XTestImports"`
	Module     *struct {
		Path string `json:"Path"`
		Main bool   `json:"Main"`
	} `json:"Module"`
}

// goCmd runs the go command with the GOFLAGS that check inherits from gate.sh.
func goCmd(args ...string) ([]byte, error) { return goCmdEnv(nil, args...) }

// goCmdEnv: goCmd with env added to the environment. The error holds go's stderr without its last
// newline, as the callers print it with Fprintln.
func goCmdEnv(env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(context.Background(), "go", args...)
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, strings.TrimRight(stderr.String(), "\n"))
	}
	return out, nil
}

func readGoMod() (goMod, error) {
	var m goMod
	out, err := goCmd("mod", "edit", "-json")
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(out, &m)
	if err != nil {
		return m, fmt.Errorf("go mod edit -json: %w", err)
	}
	return m, nil
}

// readWorkReplaces: the replaces of the go.work that gate.sh names in GOWORK when git tracks one; none
// when GOWORK is off. In workspace mode a go.work replace applies as one in go.mod does.
func readWorkReplaces() ([]replace, error) {
	file := os.Getenv("GOWORK")
	if file == "" || file == "off" {
		return nil, nil
	}
	out, err := goCmd("work", "edit", "-json", file)
	if err != nil {
		return nil, err
	}
	var w struct {
		Replace []replace `json:"Replace"`
	}
	err = json.Unmarshal(out, &w)
	if err != nil {
		return nil, fmt.Errorf("go work edit -json: %w", err)
	}
	return w.Replace, nil
}

func listPackages(deps bool, env ...string) ([]listedPkg, error) {
	args := []string{"list", "-e", "-json=ImportPath,Name,Dir,Standard,GoFiles,CgoFiles,TestGoFiles,XTestGoFiles,Imports,TestImports,XTestImports,Module"}
	if deps {
		args = append(args, "-deps", "-test")
	}
	out, err := goCmdEnv(env, append(args, "./...")...)
	if err != nil {
		return nil, err
	}
	var pkgs []listedPkg
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPkg
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return pkgs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		pkgs = append(pkgs, p)
	}
}

func replaceLine(o, n modVersion) string {
	at := func(m modVersion) string {
		if m.Version == "" {
			return m.Path
		}
		return m.Path + "@" + m.Version
	}
	return at(o) + " => " + at(n)
}

// toolModule: the required module a tool's package path lies in.
func toolModule(m goMod, tool string) string {
	best := ""
	for _, r := range m.Require {
		if (tool == r.Path || strings.HasPrefix(tool, r.Path+"/")) && len(r.Path) > len(best) {
			best = r.Path
		}
	}
	return best
}

// approved: what a first install puts on the list. go.mod's direct requires, the modules of its
// tools, its replaces and a tracked go.work's: the indirect requires come with them.
func approved() ([]string, error) {
	m, err := readGoMod()
	if err != nil {
		return nil, err
	}
	work, err := readWorkReplaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range m.Require {
		if !r.Indirect {
			out = append(out, r.Path)
		}
	}
	for _, t := range m.Tool {
		if mod := toolModule(m, t.Path); mod != "" {
			out = append(out, mod)
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	for _, r := range slices.Concat(m.Replace, work) {
		out = append(out, replaceLine(r.Old, r.New))
	}
	return out, nil
}

func readApproved() map[string]bool {
	ok := map[string]bool{}
	data, err := os.ReadFile(depsFile)
	if err != nil {
		return nil
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" && !strings.HasPrefix(line, "#") {
			ok[line] = true
		}
	}
	return ok
}

// fileLine: the line of file that names path, for the finding's position.
func fileLine(file, path string) int {
	data, _ := os.ReadFile(file)
	for i, l := range strings.Split(string(data), "\n") {
		if slices.Contains(strings.Fields(l), path) {
			return i + 1
		}
	}
	return 1
}

// deps: every direct require, every module the project's packages and tests import, every tool and
// every replace of go.mod and of a tracked go.work must be on go-gate/deps.txt, the list the user
// approved. `go mod tidy` writes go.mod from the imports, from its own process, so a deny rule cannot
// stop a new module; this check does. Also no-excluded: an import of a package that exclude covers.
func deps(s settings) ([]finding, error) {
	m, err := readGoMod()
	if err != nil {
		return nil, err
	}
	work, err := readWorkReplaces()
	if err != nil {
		return nil, err
	}
	pkgs, err := listPackages(true)
	if err != nil {
		return nil, err
	}
	var out []finding
	if ok := readApproved(); ok != nil {
		out = append(out, unapproved(m, work, pkgs, ok)...)
	}
	return append(out, importsExcluded(s, pkgs)...), nil
}

func unapproved(m goMod, work []replace, pkgs []listedPkg, ok map[string]bool) []finding {
	mods := map[string]bool{}
	for _, r := range m.Require {
		if !r.Indirect {
			mods[r.Path] = true
		}
	}
	for _, t := range m.Tool {
		if mod := toolModule(m, t.Path); mod != "" {
			mods[mod] = true
		}
	}
	byPath := map[string]listedPkg{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	// Once go mod tidy -diff passes, an imported module is a direct require already, but for a module
	// that a tracked go.work uses: it is a main module, and go.mod lists its imports as indirect.
	for _, p := range pkgs {
		if p.Module == nil || !p.Module.Main {
			continue
		}
		for _, imp := range p.Imports {
			if d, found := byPath[imp]; found && d.Module != nil && !d.Module.Main && !d.Standard {
				mods[d.Module.Path] = true
			}
		}
	}
	var out []finding
	fix := func(remove, what string) string {
		return "If you added it, remove it (" + remove + "). If you did not, ask the user to approve or " +
			"remove it. After the user approves it, add " + what + " on a new line in " + depsFile
	}
	for _, mod := range slices.Sorted(maps.Keys(mods)) {
		if !ok[mod] {
			out = append(out, finding{"go.mod", fileLine("go.mod", mod), "deps", fmt.Sprintf(
				"the project depends on module %s, which the user did not approve (%s). %s", mod, depsFile,
				fix("delete the import, require or tool line that brought it in, then run `go mod tidy`", "its path"))})
		}
	}
	// The same message for go.mod and go.work; its command names the file's go subcommand.
	replaces := func(file string, rs []replace) {
		for _, r := range rs {
			if line := replaceLine(r.Old, r.New); !ok[line] {
				drop := "`go " + strings.TrimPrefix(file, "go.") + " edit -dropreplace=" + strings.Split(line, " => ")[0] + "`"
				out = append(out, finding{file, fileLine(file, r.Old.Path), "deps", fmt.Sprintf(
					"`replace %s` points a module at other code, and the user did not approve it (%s). %s",
					line, depsFile, fix(drop, "`old[@v] => new[@v]`"))})
			}
		}
	}
	replaces("go.mod", m.Replace)
	replaces("go.work", work)
	return out
}

func relDir(dir string) string {
	wd, _ := os.Getwd()
	rel, err := filepath.Rel(wd, dir)
	if err != nil {
		return dir
	}
	return filepath.ToSlash(rel)
}

// excludedPkg: a package of the project whose every file, tests too, exclude covers.
func excludedPkg(s settings, p listedPkg) bool {
	files := slices.Concat(p.GoFiles, p.CgoFiles, p.TestFiles, p.XTestFiles)
	if len(s.exclude) == 0 || p.Module == nil || !p.Module.Main || len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !matches(s.exclude, relDir(filepath.Join(p.Dir, f))) {
			return false
		}
	}
	return true
}

// importsExcluded: no-excluded, as ts-gate's dependency-cruiser rule, at each import of an excluded
// package in the files of a package, its tests too. What an excluded path holds can go at any time.
func importsExcluded(s settings, pkgs []listedPkg) []finding {
	if len(s.exclude) == 0 {
		return nil
	}
	byPath := map[string]listedPkg{}
	for _, p := range pkgs {
		byPath[p.ImportPath] = p
	}
	var out []finding
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.Module == nil || !p.Module.Main || excludedPkg(s, p) {
			continue
		}
		// -test lists a package's test variant as `path [path.test]`, and its imports so too.
		excluded := map[string]bool{}
		for _, imp := range p.Imports {
			if d, found := byPath[imp]; found && excludedPkg(s, d) {
				name, _, _ := strings.Cut(d.ImportPath, " ")
				excluded[name] = true
			}
		}
		if len(excluded) == 0 {
			continue
		}
		for _, name := range slices.Concat(p.GoFiles, p.CgoFiles, p.TestFiles, p.XTestFiles) {
			for _, f := range importsOf(filepath.Join(p.Dir, name), excluded) {
				if key := f.String(); !seen[key] {
					seen[key] = true
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// importsOf: a no-excluded finding at each import of file that names a package of excluded. A file
// that does not parse is the compiler's to report.
func importsOf(file string, excluded map[string]bool) []finding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	var out []finding
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !excluded[path] {
			continue
		}
		out = append(out, finding{relDir(file), fset.Position(spec.Pos()).Line, "no-excluded", fmt.Sprintf(
			"this file imports %s, which exclude in .claude/workshop.conf leaves out of the gate: what it "+
				"holds can go at any time. Copy what the code needs out of it into a checked path", path)})
	}
	return out
}

// testPackages: the packages go test runs. ./... when exclude covers none.
func testPackages(s settings) ([]string, error) {
	pkgs, err := listPackages(false)
	if err != nil {
		return nil, err
	}
	var out []string
	cut := false
	for _, p := range pkgs {
		if excludedPkg(s, p) {
			cut = true
			continue
		}
		out = append(out, p.ImportPath)
	}
	if !cut && len(out) > 0 {
		return []string{"./..."}, nil
	}
	return out, nil
}
