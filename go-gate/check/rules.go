package main

import (
	"context"
	"errors"
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
	"strconv"
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

var (
	// unswitched: the rules that guard the approved list, the legacy list and the test run. Neither
	// their off key nor the legacy list drops one of their findings.
	unswitched = map[string]bool{"deps": true, "legacy-list": true, "no-test-ran": true}
	// projectWide: the rules that judge a legacy file too. no-excluded is one, as ts-gate's
	// dependency-cruiser checks a legacy file: an import of excluded code fails wherever it is.
	projectWide = map[string]bool{"deps": true, "legacy-list": true, "no-test-ran": true, "no-excluded": true}
)

// report prints each finding but those in a file of the rule's go-gate.off.<rule> globs or, for a
// rule that is not project-wide, of the legacy list, and returns how many it printed.
func report(s settings, found []finding) int {
	legacy, _ := legacyFiles()
	found = slices.DeleteFunc(found, func(f finding) bool {
		return !unswitched[f.rule] && matches(s.off[f.rule], f.file) ||
			!projectWide[f.rule] && slices.Contains(legacy, f.file)
	})
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
	// Each comment that a linter of the gate's config honours, as its source matches it
	// (golangci-lint 2.15.0 and the versions it pins):
	//   - golangci-lint's //nolint: every leading / and space trimmed, then `nolint` and a space,
	//     a colon or the end (pkg/result/processors/nolint_filter.go). `///nolint` hides; a tab does not.
	//   - contextcheck's own: `//`, at most one space or tab, `nolint:`, and contextcheck after it.
	//   - staticcheck's //lint:ignore and //lint:file-ignore, which unused honours: no space after //.
	//   - gosec's //gosec:disable, with no space after //, then a space or the end. #nosec is
	//     tested by strings.Contains: gosec takes the tag anywhere in a comment (`#nosecG101`).
	//   - revive's disable, disable-line and disable-next-line (mgechev/revive lint/file.go);
	//     its enable alone hides nothing.
	// The other linters' own comments: hides.
	ignoreRe = regexp.MustCompile(`^//[/ ]*nolint( |:|$)|^//\s?nolint:.*contextcheck|` +
		`^//lint:(file-)?ignore( |$)|^//gosec:disable( |$)|` +
		`^//\s*revive:disable(-line|-next-line)?(:\S+)?\s*( .+)?$`)
	// makezero's `nozero`, matched on the comment's text without its markers
	// (ashanbrown/makezero): `// nozero`, `/* nozero */`; `//nozero:x` is a directive, which
	// the text leaves out.
	nozeroRe = regexp.MustCompile(`^\s*nozero\b`)
	// ginkgolinter's suppressions, each the whole line of a comment (nunnatsa/ginkgolinter config).
	ginkgoIgnore = map[string]bool{
		"ginkgo-linter:ignore-len-assert-warning":      true,
		"ginkgo-linter:ignore-nil-assert-warning":      true,
		"ginkgo-linter:ignore-err-assert-warning":      true,
		"ginkgo-linter:ignore-compare-assert-warning":  true,
		"ginkgo-linter:ignore-async-assert-warning":    true,
		"ginkgo-linter:ignore-focus-container-warning": true,
		"ginkgo-linter:ignore-type-compare-warning":    true,
	}
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
				if !matches(s.exclude, mod) {
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
	fset := token.NewFileSet()
	byDir, out := goFiles(s)
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
			if !matches(s.exclude, p) {
				out = append(out, file(fset, p, parsed[p], decoders)...)
			}
		}
	}
	return out
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

// hides reports whether a linter of the gate's config takes the comment as an order to hide a
// finding, each test as that linter's source makes it.
func hides(c *ast.Comment) bool {
	text := c.Text
	switch {
	case ignoreRe.MatchString(text), strings.Contains(text, "#nosec"):
		return true
	// godoclint (pkg/inspect): the tag anywhere in a line, then rule names or nothing.
	case strings.Contains(text, "//godoclint:disable"):
		return true
	// bodyclose: a function's doc line, its // and surrounding space trimmed.
	case strings.TrimSpace(strings.TrimPrefix(text, "//")) == "bodyclose:handled":
		return true
	case nozeroRe.MatchString((&ast.CommentGroup{List: []*ast.Comment{c}}).Text()):
		return true
	}
	// iface (internal/directive): the comment trimmed, alone or with `=names`.
	if t := strings.TrimSpace(text); t == "//iface:ignore" || strings.HasPrefix(t, "//iface:ignore=") {
		return true
	}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(line, "//"), "/*"), "*/")
		if ginkgoIgnore[strings.TrimSpace(line)] {
			return true
		}
	}
	return false
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
			if hides(c) {
				at(c)("no-ignore-comment", "the gate refuses ignore comments: fix what the linter reports. "+
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
					at(c)("no-disabled-test", "a skip with no condition, or under a constant one: the test never "+
						"runs. Fix the test or delete it; a skip passes only under a real condition")
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

// legacyLine: a line of legacy-list.py, `<list>:<line>: legacy-list: <how to fix>`.
var legacyLine = regexp.MustCompile(`^(.+?):(\d+): (legacy-list): (.*)$`)

// legacyList: go-gate-legacy.txt only gets shorter. The findings of go-gate/scripts/legacy-list.py,
// a copy of the workshop's ecosystems/shared/legacy-list.py, which ts-gate and py-gate run too.
// Without the list there is nothing to check, and python3 is not needed.
func legacyList() ([]finding, error) {
	if !fileExists(legacyFile) {
		return nil, nil
	}
	cmd := exec.CommandContext(context.Background(), "python3", "-I", "go-gate/scripts/legacy-list.py", "go-gate")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, fmt.Errorf("legacy-list: python3 go-gate/scripts/legacy-list.py: %w\n%s", err, stderr.String())
	}
	var found []finding
	for l := range strings.SplitSeq(string(out), "\n") {
		if l == "" {
			continue
		}
		m := legacyLine.FindStringSubmatch(l)
		if m == nil {
			return nil, fmt.Errorf("legacy-list: go-gate/scripts/legacy-list.py printed %q", l)
		}
		n, _ := strconv.Atoi(m[2])
		found = append(found, finding{m[1], n, m[3], m[4]})
	}
	return found, nil
}
