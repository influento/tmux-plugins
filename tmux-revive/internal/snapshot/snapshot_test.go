package snapshot

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func pane(id, cwd string) Pane { return Pane{ID: id, Cwd: cwd} }

func snapOf(sessions ...Session) *Snapshot {
	return &Snapshot{Version: Version, Time: time.Unix(1000, 0), Sessions: sessions}
}

func sess(name string, windows ...Window) Session { return Session{Name: name, Windows: windows} }

func win(id string, panes ...Pane) Window { return Window{ID: id, Panes: panes} }

func TestCompare(t *testing.T) {
	base := snapOf(sess("proj-a", win("w1", pane("p1", "/a"), pane("p2", "/b"))))
	later := snapOf(sess("proj-a", win("w1", pane("p1", "/a"), pane("p2", "/b"))))
	later.Time = time.Unix(2000, 0)
	moved := snapOf(sess("proj-a", win("w1", pane("p1", "/a"), pane("p2", "/c"))))
	closed := snapOf(sess("proj-a", win("w1", pane("p1", "/a"))))
	renamed := snapOf(sess("proj-b", win("w1", pane("p1", "/a"), pane("p2", "/b"))))

	for _, c := range []struct {
		name             string
		a, b             *Snapshot
		equal, sameShape bool
		lost             []string
	}{
		{"same state, other time", base, later, true, true, nil},
		{"a folder changed", base, moved, false, true, nil},
		{"a pane closed", base, closed, false, false, []string{"p2"}},
		{"a session renamed", base, renamed, false, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Equal(c.a, c.b); got != c.equal {
				t.Errorf("Equal = %v, want %v", got, c.equal)
			}
			if got := SameShape(c.a, c.b); got != c.sameShape {
				t.Errorf("SameShape = %v, want %v", got, c.sameShape)
			}
			if got := Lost(c.a, c.b); !slices.Equal(got, c.lost) {
				t.Errorf("Lost = %v, want %v", got, c.lost)
			}
		})
	}
}

func TestWithoutAndLastUsed(t *testing.T) {
	s := snapOf(
		Session{Name: "proj-a", LastAttached: 10},
		Session{Name: "ov-proj-a", LastAttached: 30},
		Session{Name: "proj-b", LastAttached: 20},
	)
	keep := func(name string) bool { return !strings.HasPrefix(name, "ov-") }
	if got := s.LastUsed(keep); got != "proj-b" {
		t.Errorf("LastUsed = %q, want proj-b", got)
	}
	without, had := s.Without("proj-b")
	if !had || len(without.Sessions) != 2 || len(s.Sessions) != 3 {
		t.Errorf("Without: had=%v, %d sessions left, original has %d", had, len(without.Sessions), len(s.Sessions))
	}
	if got := without.LastUsed(keep); got != "proj-a" {
		t.Errorf("LastUsed without proj-b = %q, want proj-a", got)
	}
	if _, had := s.Without("proj-x"); had {
		t.Error("Without(proj-x) reported a session")
	}
	if got := snapOf(Session{Name: "proj-a"}).LastUsed(keep); got != "" {
		t.Errorf("LastUsed with nothing ever attached = %q, want none", got)
	}
}

func TestStoreKeepsTheNewest(t *testing.T) {
	st := &Store{Dir: t.TempDir(), Keep: 3}
	base := time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)
	for i := range 5 {
		s := snapOf(sess("proj-a", win("w1", pane("p1", "/a"))))
		s.Time = base.Add(time.Duration(i) * time.Minute)
		s.Sessions[0].LastAttached = int64(i)
		if _, err := st.Write(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	es, err := st.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 3 || !es[0].Time.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("entries = %v, want the 3 newest", es)
	}
	latest, e, ok, err := st.Latest()
	if err != nil || !ok || latest.Sessions[0].LastAttached != 4 || !e.Time.Equal(base.Add(4*time.Minute)) {
		t.Errorf("Latest = %+v %v %v %v", latest, e, ok, err)
	}
	at, _, ok, _ := st.At(base.Add(3*time.Minute + 30*time.Second))
	if !ok || at.Sessions[0].LastAttached != 3 {
		t.Errorf("At(14:03:30) = %+v, want the 14:03 snapshot", at)
	}
	if _, _, ok, _ := st.At(base); ok {
		t.Error("At(before every kept snapshot) found one")
	}
}

func TestStoreReplace(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	s := snapOf(sess("proj-a"))
	first, err := st.Write(s, "")
	if err != nil {
		t.Fatal(err)
	}
	s2 := snapOf(sess("proj-a"))
	s2.Time = s.Time.Add(time.Second)
	second, err := st.Write(s2, first.Path)
	if err != nil {
		t.Fatal(err)
	}
	es, _ := st.Entries()
	if len(es) != 1 || es[0].Path != second.Path {
		t.Errorf("entries = %v, want only %s", es, second.Path)
	}
	// Two snapshots in the same nanosecond still get two files.
	if _, err := st.Write(s2, ""); err != nil {
		t.Fatal(err)
	}
	if es, _ := st.Entries(); len(es) != 2 {
		t.Errorf("entries = %v, want 2", es)
	}
}

// A file cut short by a crash, or in a format this version cannot read,
// is passed over for the newest one that reads.
func TestLatestSkipsUnreadable(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	good := snapOf(sess("proj-a"))
	if _, err := st.Write(good, ""); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(st.Dir, "snapshots")
	for name, body := range map[string]string{
		"20990101T000000.000000000Z.json": `{"version": 1, "sess`,
		"20990102T000000.000000000Z.json": `{"version": 99, "sessions": []}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	latest, _, ok, err := st.Latest()
	if err != nil || !ok || latest.Sessions[0].Name != "proj-a" {
		t.Errorf("Latest = %+v, %v, %v; want the readable one", latest, ok, err)
	}
}

// Scrollback can hold secrets: the folders are 0700, the files 0600.
func TestStoreModes(t *testing.T) {
	st := &Store{Dir: filepath.Join(t.TempDir(), "tmux-revive")}
	e, err := st.Write(snapOf(sess("proj-a", win("w1", pane("p1", "/a")))), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteScrollback("p1", []byte("text\n")); err != nil {
		t.Fatal(err)
	}
	sb, _ := st.ScrollbackPath("p1")
	st.Logf(time.Now(), "line")
	for path, want := range map[string]os.FileMode{
		st.Dir: 0o700, filepath.Dir(e.Path): 0o700, filepath.Dir(sb): 0o700,
		e.Path: 0o600, sb: 0o600, filepath.Join(st.Dir, "log"): 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", path, got, want)
		}
	}
}

func TestScrollbackFiles(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	if _, err := st.ScrollbackPath("../x"); err == nil {
		t.Error("a pane id with a path in it was accepted")
	}
	if changed, err := st.WriteScrollback("p1", []byte("a\n")); err != nil || !changed {
		t.Errorf("first write: changed=%v err=%v", changed, err)
	}
	if changed, err := st.WriteScrollback("p1", []byte("a\n")); err != nil || changed {
		t.Errorf("same text again: changed=%v err=%v, want unchanged", changed, err)
	}
	for _, id := range []string{"p2", "p3", "p4"} {
		if _, err := st.WriteScrollback(id, []byte("b\n")); err != nil {
			t.Fatal(err)
		}
	}
	// p1 is in a snapshot, p2 is live; p3 and p4 are neither.
	if _, err := st.Write(snapOf(sess("proj-a", win("w1", pane("p1", "/a")))), ""); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneScrollback(map[string]bool{"p2": true}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"p1": true, "p2": true, "p3": false, "p4": false} {
		path, _ := st.ScrollbackPath(id)
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s kept = %v, want %v", id, err == nil, want)
		}
	}
}

func TestLogMovesAsideWhenLarge(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	path := filepath.Join(st.Dir, "log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", logLimit+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	st.Logf(time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC), "restore %s", "x")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "2026-11-03T14:00:00Z restore x\n" {
		t.Errorf("log = %q", got)
	}
	if _, err := os.Stat(path + ".old"); err != nil {
		t.Errorf("log.old: %v", err)
	}
}

// Temporary files a crash left behind go; one being written now stays.
func TestPruneRemovesStaleTemps(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	if _, err := st.Write(snapOf(sess("proj-a", win("w1", pane("p1", "/a")))), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteScrollback("p1", []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	var stale, fresh []string
	for _, dir := range []string{st.snapDir(), st.scrollDir()} {
		s, f := filepath.Join(dir, ".tmp-1"), filepath.Join(dir, ".tmp-2")
		for _, p := range []string{s, f} {
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(s, old, old); err != nil {
			t.Fatal(err)
		}
		stale, fresh = append(stale, s), append(fresh, f)
	}
	if _, err := st.Write(snapOf(sess("proj-b", win("w2", pane("p2", "/b")))), ""); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneScrollback(nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range stale {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	for _, p := range fresh {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}
