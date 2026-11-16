package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

type finding struct {
	file string
	line int
	rule string
	msg  string
}

func (f finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.file, f.line, f.rule, f.msg)
}

func report(found []finding) int {
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].file != found[j].file {
			return found[i].file < found[j].file
		}
		return found[i].line < found[j].line
	})
	for _, f := range found {
		fmt.Fprintln(os.Stdout, f)
	}
	return len(found)
}

var (
	ignoreRe = regexp.MustCompile(`^//\s*nolint\b|^//\s*lint:(file-)?ignore\b|#nosec\b`)
	// A call that takes an `any` as a decode target, or as the value to encode: either way the
	// function does not read it.
	decodeCall = map[string]bool{
		"Unmarshal": true, "Decode": true, "Scan": true, "Marshal": true, "MarshalIndent": true, "Encode": true,
	}
	// A call that takes a variadic ...any of format arguments.
	formatCall = map[string]bool{
		"Printf": true, "Sprintf": true, "Errorf": true, "Fprintf": true, "Print": true, "Println": true,
		"Sprint": true, "Sprintln": true, "Fprint": true, "Fprintln": true, "Logf": true, "Log": true,
		"Info": true, "Warn": true, "Error": true, "Debug": true, "InfoContext": true, "WarnContext": true,
		"ErrorContext": true, "DebugContext": true, "With": true, "Fatalf": true, "Skipf": true,
	}
	// Methods whose standard library interface takes or returns any: sql.Scanner, errors.As.
	stdMethod = map[string]bool{"Scan": true, "As": true}
)

// goFiles: the .go files of the project's packages, by folder, as ./... sees them: no folder that
// starts with . or _, no testdata, vendor or go-gate, and no folder of another module. Each go.mod
// below the root that exclude does not cover is a finding: the folder is another module, which no
// check of the project sees.
func goFiles(s settings) (map[string][]string, []finding) {
	byDir := map[string][]string{}
	var nested []finding
	_ = filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a folder it cannot read is the compiler's to report
		}
		if d.IsDir() {
			name := d.Name()
			if p == "." {
				return nil
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" ||
				name == "vendor" || p == "go-gate" {
				return filepath.SkipDir
			}
			if mod := filepath.ToSlash(filepath.Join(p, "go.mod")); fileExists(mod) {
				if !matches(s.exclude, mod) && !matches(s.off["no-hidden-file"], mod) {
					nested = append(nested, finding{mod, 1, "no-hidden-file", "this go.mod makes the folder " +
						"another module, which the project's ./... and so every check of the gate skip. Delete " +
						"it; if the folder is not the project's code, ask the user to add it to exclude in " +
						".claude/workshop.conf"})
				}
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") {
			dir := filepath.ToSlash(filepath.Dir(p))
			byDir[dir] = append(byDir[dir], filepath.ToSlash(p))
		}
		return nil
	})
	return byDir, nested
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func rules(s settings) []finding {
	legacy, _ := legacyFiles()
	skip := map[string]bool{}
	for _, p := range legacy {
		skip[p] = true
	}
	fset := token.NewFileSet()
	byDir, nested := goFiles(s)
	var out []finding
	for _, f := range nested {
		if !skip[f.file] {
			out = append(out, f)
		}
	}
	for _, dir := range slices.Sorted(maps.Keys(byDir)) {
		parsed := map[string]*ast.File{}
		for _, p := range byDir[dir] {
			f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
			if err == nil { // a file that does not parse is the compiler's to report
				parsed[p] = f
			}
		}
		decoders := packageDecoders(parsed)
		for _, p := range slices.Sorted(maps.Keys(parsed)) {
			if skip[p] || matches(s.exclude, p) {
				continue
			}
			for _, f := range file(fset, p, parsed[p], decoders) {
				if !matches(s.off[f.rule], f.file) {
					out = append(out, f)
				}
			}
		}
	}
	return append(out, legacyList()...)
}

// packageDecoders: the decode calls, and each function of the folder that only passes its any on to
// one of them, which makes it a decoder too.
func packageDecoders(files map[string]*ast.File) map[string]bool {
	decoders := maps.Clone(decodeCall)
	for changed := true; changed; {
		changed = false
		for _, f := range files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || decoders[fn.Name.Name] || fn.Type.Params == nil {
					continue
				}
				for _, p := range fn.Type.Params.List {
					for _, id := range p.Names {
						if isAny(p.Type) && passedOnOnly(id.Name, fn.Body, decoders) {
							decoders[fn.Name.Name], changed = true, true
						}
					}
				}
			}
		}
	}
	return decoders
}

func file(fset *token.FileSet, rel string, f *ast.File, decoders map[string]bool) []finding {
	var out []finding
	at := func(n ast.Node) func(rule, msg string) {
		return func(rule, msg string) {
			out = append(out, finding{rel, fset.Position(n.Pos()).Line, rule, msg})
		}
	}
	test := strings.HasSuffix(rel, "_test.go")
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if ignoreRe.MatchString(c.Text) || strings.Contains(c.Text, "#nosec") {
				at(c)("no-ignore-comment", "an ignore comment has no effect: fix what the linter reports. "+
					"If the rule is wrong for this project, ask the user to turn it off with "+
					"go-gate.off.<linter> in .claude/workshop.conf")
			}
			constraint := strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build")
			if !constraint || c.Pos() >= f.Package {
				continue
			}
			switch {
			case test:
				at(c)("no-disabled-test", "a build constraint on a test file hides its tests from go test: remove it")
			case strings.Contains(c.Text, "ignore"):
				at(c)("no-hidden-file", "//go:build ignore hides the file from the compiler and every check: "+
					"delete the file or remove the line")
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if test {
				disabled(at, n)
			} else {
				signature(at, n.Name.Name, n.Recv != nil, n.Type, n.Body, decoders)
			}
		case *ast.InterfaceType:
			if test {
				break // a test builds its data as any: no-any-signature is for the code under test
			}
			for _, m := range n.Methods.List {
				if ft, ok := m.Type.(*ast.FuncType); ok && len(m.Names) == 1 {
					signature(at, m.Names[0].Name, true, ft, nil, decoders)
				}
			}
		}
		return true
	})
	return out
}

func isAny(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name == "any"
	case *ast.InterfaceType:
		return t.Methods == nil || len(t.Methods.List) == 0
	}
	return false
}

func hasAny(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if x, ok := n.(ast.Expr); ok && isAny(x) {
			found = true
		}
		return !found
	})
	return found
}

func callName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}

// passedOnOnly: every use of name in body is an argument of one of calls, or of any call when calls
// is nil.
func passedOnOnly(name string, body *ast.BlockStmt, calls map[string]bool) bool {
	if body == nil {
		return false
	}
	args := map[*ast.Ident]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if c, isCall := n.(*ast.CallExpr); isCall && (calls == nil || calls[callName(c)]) {
			for _, a := range c.Args {
				if id, isID := a.(*ast.Ident); isID {
					args[id] = true
				}
			}
		}
		return true
	})
	ok, uses := true, 0
	ast.Inspect(body, func(n ast.Node) bool {
		if id, isID := n.(*ast.Ident); isID && id.Name == name {
			uses++
			ok = ok && args[id]
		}
		return true
	})
	return ok && uses > 0
}

type reporter func(n ast.Node) func(rule, msg string)

func signature(at reporter, name string, method bool, ft *ast.FuncType, body *ast.BlockStmt, decoders map[string]bool) {
	if method && stdMethod[name] {
		return
	}
	params := fieldList(ft.Params)
	for i, p := range params {
		t := p.Type
		variadic := false
		if el, isEl := t.(*ast.Ellipsis); isEl {
			t, variadic = el.Elt, true
		}
		if !hasAny(t) {
			continue
		}
		bare := isAny(t)
		for _, id := range p.Names {
			calls := decoders
			switch {
			case variadic && i > 0 && isString(params[i-1].Type):
				calls = nil // a printf wrapper, (format string, args ...any), passing args on
			case variadic:
				calls = formatCall
			}
			if bare && passedOnOnly(id.Name, body, calls) {
				continue
			}
			what := "any"
			switch {
			case variadic:
				what = "...any"
			case !bare:
				what = "a type that holds any"
			}
			at(id)("no-any-signature", fmt.Sprintf("parameter %s is %s: the input is not parsed yet. Parse it "+
				"at the edge into a struct or a named type (json.Unmarshal into a typed value) and take that type",
				id.Name, what))
		}
		if len(p.Names) == 0 {
			at(p)("no-any-signature", "a parameter of type any: take the parsed type")
		}
	}
	for _, r := range fieldList(ft.Results) {
		if isAny(r.Type) {
			at(r)("no-any-signature", "a result of type any: the caller must probe it. Return the parsed type, "+
				"or a named interface with the methods callers use")
		}
	}
}

func isString(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "string"
}

func fieldList(l *ast.FieldList) []*ast.Field {
	if l == nil {
		return nil
	}
	return l.List
}

func disabled(at reporter, fn *ast.FuncDecl) {
	if fn.Body == nil {
		return
	}
	if fn.Name.Name == "TestMain" {
		// m.Run, or m handed to a helper that runs it: no-test-ran checks the run itself.
		m := ""
		if ps := fieldList(fn.Type.Params); len(ps) == 1 && len(ps[0].Names) == 1 {
			m = ps[0].Names[0].Name
		}
		runs := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				runs = runs || callName(c) == "Run" || slices.ContainsFunc(c.Args, func(a ast.Expr) bool {
					id, isID := a.(*ast.Ident)
					return isID && m != "" && id.Name == m
				})
			}
			return !runs
		})
		if !runs {
			at(fn)("no-disabled-test", "TestMain does not call m.Run: no test of this package runs. "+
				"Call os.Exit(m.Run()) or delete TestMain")
		}
	}
	// A Skip that runs whatever happens: in the function's own statements, or under a constant condition.
	var walk func(stmts []ast.Stmt, cond bool)
	walk = func(stmts []ast.Stmt, cond bool) {
		for _, s := range stmts {
			switch s := s.(type) {
			case *ast.ExprStmt:
				if c, ok := s.X.(*ast.CallExpr); ok && slices.Contains([]string{"Skip", "Skipf", "SkipNow"}, callName(c)) && !cond {
					at(c)("no-disabled-test", "a skip with no condition: the test never runs. Fix the test or "+
						"delete it; a skip passes only under a real condition")
				}
			case *ast.IfStmt:
				constant := false
				if id, ok := s.Cond.(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
					constant = true
				}
				walk(s.Body.List, cond || !constant)
				if e, ok := s.Else.(*ast.BlockStmt); ok {
					walk(e.List, cond || !constant)
				}
			case *ast.BlockStmt:
				walk(s.List, cond)
			}
		}
	}
	walk(fn.Body.List, false)
}

func git(args ...string) (string, bool) {
	out, err := exec.CommandContext(context.Background(), "git", args...).Output()
	return string(out), err == nil
}

// legacyList: go-gate-legacy.txt only gets shorter. Each line was on the list that go-gate's first
// install wrote, the first version git has, no commit since deleted it, and it names a file that
// exists. A list that turns up after the gate was committed fails.
func legacyList() []finding {
	data, err := os.ReadFile(legacyFile)
	if err != nil {
		return nil
	}
	var out []finding
	var allowed map[string]bool
	added, _ := git("log", "--diff-filter=A", "--format=%H", "--", legacyFile)
	shas := strings.Fields(added)
	original, ok := "", false
	if len(shas) > 0 {
		original, ok = git("show", shas[len(shas)-1]+":./"+legacyFile)
	}
	if !ok {
		if _, gated := git("cat-file", "-e", "HEAD:./go-gate/check/main.go"); gated {
			out = append(out, finding{legacyFile, 1, "legacy-list", "only go-gate's first install writes this " +
				"list, and go-gate was in the project before it. Delete the file, and fix what the gate then reports"})
		}
	} else {
		// A line a commit deleted never comes back, also when a later commit adds it again.
		log, _ := git("log", "--format=", "-p", "--unified=0", "--no-renames", "--", legacyFile)
		gone := map[string]bool{}
		for r := range strings.SplitSeq(log, "\n") {
			if strings.HasPrefix(r, "-") && !strings.HasPrefix(r, "--- ") {
				gone[strings.TrimSpace(r[1:])] = true
			}
		}
		allowed = map[string]bool{}
		for r := range strings.SplitSeq(original, "\n") {
			if r = strings.TrimSpace(r); !gone[r] {
				allowed[r] = true
			}
		}
	}
	for n, row := range strings.Split(string(data), "\n") {
		path := strings.TrimSpace(row)
		if path == "" || strings.HasPrefix(path, "#") {
			continue
		}
		switch {
		case allowed != nil && !allowed[path]:
			out = append(out, finding{legacyFile, n + 1, "legacy-list", fmt.Sprintf("`%s` was not on the list "+
				"go-gate's install wrote, and the list only gets shorter. Delete the line, and fix what the gate "+
				"then reports in the file", path)})
		case !fileExists(path):
			out = append(out, finding{
				legacyFile, n + 1, "legacy-list",
				fmt.Sprintf("`%s` does not exist. Delete the line", path),
			})
		}
	}
	return out
}
