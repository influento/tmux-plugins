package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// legacyFile lists the files that failed the gate when it came into an existing project.
const legacyFile = "go-gate-legacy.txt"

const offPrefix = "go-gate.off."

// settings is what .claude/workshop.conf sets for the gate (the key table: AGENTS.md in the workshop
// repo): `key=value` lines, `#` comments and blank lines skipped, keys and values trimmed, the last
// occurrence of a key wins.
type settings struct {
	exclude []*regexp.Regexp
	globs   []string
	off     map[string][]*regexp.Regexp
	offGlob map[string][]string
}

func readConf() settings {
	s := settings{off: map[string][]*regexp.Regexp{}, offGlob: map[string][]string{}}
	kv := map[string]string{}
	f, err := os.Open(filepath.Join(".claude", "workshop.conf"))
	if err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			key, value, ok := strings.Cut(line, "=")
			if line == "" || strings.HasPrefix(line, "#") || !ok || strings.TrimSpace(key) == "" {
				continue
			}
			kv[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
		_ = f.Close()
	}
	s.globs = splitGlobs(kv["exclude"])
	s.exclude = compile(s.globs)
	for k, v := range kv {
		name, ok := strings.CutPrefix(k, offPrefix)
		if g := splitGlobs(v); ok && name != "" && len(g) > 0 {
			s.off[name], s.offGlob[name] = compile(g), g
		}
	}
	return s
}

func splitGlobs(v string) []string {
	var out []string
	for g := range strings.SplitSeq(v, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// globRegexp is a glob as Python's fnmatch reads it, anchored: `*` matches any run of characters, `/`
// too, `?` one character, `[...]` a class and `[!...]` its complement. Every other character stands for
// itself, a multi-byte one as a whole: one byte at a time, `é`'s two bytes became `Ã©`. A byte that is
// not UTF-8 matches a byte that is not UTF-8, as close as Go's regexp gets: it reads such a byte, in
// the pattern and in the path, as U+FFFD.
func globRegexp(g string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			end := strings.IndexByte(g[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := g[i+1 : i+1+end]
			i += end + 1
			if neg, ok := strings.CutPrefix(class, "!"); ok {
				class = "^" + neg
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
		default:
			r, size := utf8.DecodeRuneInString(g[i:])
			if r == utf8.RuneError && size == 1 {
				b.WriteString(`\x{FFFD}`)
			} else {
				b.WriteString(regexp.QuoteMeta(g[i : i+size]))
			}
			i += size - 1
		}
	}
	b.WriteString("$")
	return b.String()
}

func compile(globs []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(globs))
	for _, g := range globs {
		if re, err := regexp.Compile(globRegexp(g)); err == nil {
			out = append(out, re)
		}
	}
	return out
}

func matches(res []*regexp.Regexp, path string) bool {
	for _, re := range res {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// legacyFiles: the paths go-gate-legacy.txt lists, which the gate does not check yet.
func legacyFiles() ([]string, error) {
	data, err := os.ReadFile(legacyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", legacyFile, err)
	}
	var out []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, nil
}

func notExcluded(s settings, in io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		if p := sc.Text(); p != "" && !matches(s.exclude, p) {
			out = append(out, p)
		}
	}
	return out, sc.Err()
}
