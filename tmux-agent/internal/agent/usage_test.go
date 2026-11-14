package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// usageAgent stands in for an adapter whose transcripts hold one call per
// line: "<RFC 3339 time> <model> <output tokens>".
type usageAgent struct {
	dir    string
	reads  int
	during func() // runs in each read, as another pass could
}

func (*usageAgent) Name() string    { return "fakeagent" }
func (*usageAgent) Command() string { return "fakeagent" }

func (u *usageAgent) Transcripts(func(string) string) ([]SessionFiles, error) {
	return []SessionFiles{{Session: sessionA, Transcript: filepath.Join(u.dir, sessionA+".jsonl")}}, nil
}

func (u *usageAgent) ReadUsage(transcript string, from int64, recent []string) (UsageRead, error) {
	u.reads++
	if u.during != nil {
		u.during()
	}
	f, err := os.Open(transcript)
	if err != nil {
		return UsageRead{}, err
	}
	defer f.Close()
	if _, err := f.Seek(from, 0); err != nil {
		return UsageRead{}, err
	}
	read := UsageRead{To: from, Cwd: "/proj-a"}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			return read, nil // a line still being written waits
		}
		if err != nil {
			return read, err
		}
		read.To += int64(len(line))
		fields := strings.Fields(line)
		at, _ := time.Parse(time.RFC3339, fields[0])
		n, _ := strconv.ParseInt(fields[2], 10, 64)
		read.Calls = append(read.Calls, Call{At: at, Model: fields[1], Tokens: Tokens{Output: n}})
	}
}

func (*usageAgent) Title(string) (string, bool) { return "fix the build", false }

func usageEnv(t *testing.T) (*Env, *usageAgent, string) {
	t.Helper()
	a := &usageAgent{dir: t.TempDir()}
	e := &Env{Store: &store.Store{Dir: t.TempDir()}, LockPath: filepath.Join(t.TempDir(), "lock")}
	return e, a, filepath.Join(a.dir, sessionA+".jsonl")
}

func writeLines(t *testing.T, path string, appendTo bool, lines ...string) {
	t.Helper()
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendTo {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func scan(t *testing.T, e *Env, a *usageAgent) int {
	t.Helper()
	n, err := e.ScanUsage(context.Background(), a, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func output(t *testing.T, e *Env) map[string]int64 {
	t.Helper()
	u, ok, err := e.Store.LoadUsage("fakeagent", sessionA)
	if err != nil || !ok {
		t.Fatalf("tally: %v, %v", ok, err)
	}
	out := map[string]int64{}
	for h, models := range u.Hours {
		for m, tok := range models {
			out[h+" "+m] += tok.Output
		}
	}
	return out
}

func TestScanUsage(t *testing.T) {
	e, a, path := usageEnv(t)
	writeLines(t, path, false, "2026-01-05T10:01:00Z model-a 5", "2026-01-05T10:59:00Z model-a 7", "2026-01-05T11:00:00Z model-b 1")
	if n := scan(t, e, a); n != 1 {
		t.Fatalf("first pass changed %d tallies", n)
	}
	got := output(t, e)
	if got["2026-01-05T10 model-a"] != 12 || got["2026-01-05T11 model-b"] != 1 || len(got) != 2 {
		t.Errorf("tally = %v", got)
	}
	u, _, _ := e.Store.LoadUsage("fakeagent", sessionA)
	if u.Cwd != "/proj-a" || u.Title != "fix the build" || u.Transcript != path {
		t.Errorf("tally = %+v", u)
	}

	t.Run("nothing new reads nothing", func(t *testing.T) {
		reads := a.reads
		if n := scan(t, e, a); n != 0 || a.reads != reads {
			t.Errorf("changed %d, read %d times", n, a.reads-reads)
		}
	})
	t.Run("what is new adds", func(t *testing.T) {
		writeLines(t, path, true, "2026-01-05T10:30:00Z model-a 100")
		scan(t, e, a)
		if got := output(t, e); got["2026-01-05T10 model-a"] != 112 {
			t.Errorf("tally = %v", got)
		}
	})
	t.Run("a line still being written changes nothing", func(t *testing.T) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("2026-01-05T10:40:00Z mod")
		if err := errors.Join(err, f.Close()); err != nil {
			t.Fatal(err)
		}
		if n := scan(t, e, a); n != 0 {
			t.Errorf("changed %d tallies", n)
		}
		if got := output(t, e); got["2026-01-05T10 model-a"] != 112 {
			t.Errorf("tally = %v", got)
		}
	})
	t.Run("a shorter transcript is read anew", func(t *testing.T) {
		writeLines(t, path, false, "2026-01-06T09:00:00Z model-c 3")
		scan(t, e, a)
		if got := output(t, e); got["2026-01-06T09 model-c"] != 3 || len(got) != 1 {
			t.Errorf("tally = %v", got)
		}
	})
	t.Run("a deleted transcript leaves the tally", func(t *testing.T) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		scan(t, e, a)
		if got := output(t, e); got["2026-01-06T09 model-c"] != 3 {
			t.Errorf("tally = %v", got)
		}
	})
}

// Two passes at once count each call once: the one that writes second
// finds the tally moved, and drops its read.
func TestScanUsageRace(t *testing.T) {
	e, a, path := usageEnv(t)
	writeLines(t, path, false, "2026-01-05T10:01:00Z model-a 5")
	a.during = func() {
		a.during = nil
		scan(t, e, a) // the other pass, done before this read
	}
	scan(t, e, a)
	if got := output(t, e); got["2026-01-05T10 model-a"] != 5 {
		t.Errorf("tally = %v, want one count of 5", got)
	}
}

func TestScanUsageDamagedTally(t *testing.T) {
	e, a, path := usageEnv(t)
	writeLines(t, path, false, "2026-01-05T10:01:00Z model-a 5")
	p := filepath.Join(e.Store.Dir, "sessions", "fakeagent", sessionA, "usage.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	scan(t, e, a)
	if got := output(t, e); got["2026-01-05T10 model-a"] != 5 {
		t.Errorf("tally = %v", got)
	}
}
