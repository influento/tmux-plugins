package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// hookAgent stands in for an adapter whose hook payload is its session id.
type hookAgent struct{}

func (hookAgent) Name() string    { return "fakeagent" }
func (hookAgent) Command() string { return "fakeagent" }
func (hookAgent) ReadHook(payload []byte) (Event, bool, error) {
	return Event{Kind: TurnEnd, SessionID: string(payload)}, true, nil
}
func (hookAgent) ProcessNames() []string { return []string{"fakeagent"} }

const (
	parentSession = "aaaaaaaa-1111-4222-8333-444444444444"
	runSession    = "bbbbbbbb-1111-4222-8333-444444444444"
)

// lineageEnv is an Env for a run's hooks: its agent process is 200-7, the
// one above it 100-5, and every hook is nested.
func lineageEnv(t *testing.T) *Env {
	t.Helper()
	runtime := t.TempDir()
	return &Env{
		Store: &store.Store{Dir: t.TempDir()}, Registry: NewRegistry(hookAgent{}), Pane: "%999",
		NotesDir: filepath.Join(runtime, "agents"), LockPath: filepath.Join(runtime, "lock"),
		Getenv:  func(k string) string { return map[string]string{"XDG_RUNTIME_DIR": runtime}[k] },
		Lineage: func([]string) (string, string) { return "200-7", "100-5" },
		Nested:  func([]string, int) bool { return true },
	}
}

func parentOf(t *testing.T, e *Env) string {
	t.Helper()
	u, _, err := e.Store.LoadUsage("fakeagent", runSession)
	if err != nil {
		t.Fatal(err)
	}
	return u.Parent
}

func TestRunRecordsItsParent(t *testing.T) {
	e := lineageEnv(t)
	ctx := context.Background()
	if err := e.Hook(ctx, hookAgent{}, []byte(runSession)); err != nil {
		t.Fatal(err)
	}
	if p := parentOf(t, e); p != "" {
		t.Fatalf("parent %q with no note of the agent above", p)
	}
	// The run's own note, for runs it starts.
	if data, _ := os.ReadFile(filepath.Join(e.NotesDir, "200-7")); string(data) != "fakeagent "+runSession {
		t.Errorf("run's note = %q", data)
	}
	e.noteSession(hookAgent{}, parentSession, "100-5") // the parent's hooks
	if err := e.Hook(ctx, hookAgent{}, []byte(runSession)); err != nil {
		t.Fatal(err)
	}
	if p := parentOf(t, e); p != parentSession {
		t.Errorf("parent = %q, want %q", p, parentSession)
	}
	// A later note of the agent above (a /clear) does not move it.
	e.noteSession(hookAgent{}, "cccccccc-1111-4222-8333-444444444444", "100-5")
	if err := e.Hook(ctx, hookAgent{}, []byte(runSession)); err != nil {
		t.Fatal(err)
	}
	if p := parentOf(t, e); p != parentSession {
		t.Errorf("parent moved to %q", p)
	}
	u, _, _ := e.Store.LoadUsage("fakeagent", runSession)
	if u.ParentAgent != "fakeagent" || u.Offset != 0 || len(u.Hours) != 0 {
		t.Errorf("tally = %+v", u)
	}
}

// A note changes with its session: /clear starts a new one in the same
// process.
func TestNoteFollowsTheSession(t *testing.T) {
	e := lineageEnv(t)
	e.noteSession(hookAgent{}, parentSession, "100-5")
	e.noteSession(hookAgent{}, runSession, "100-5")
	if data, _ := os.ReadFile(filepath.Join(e.NotesDir, "100-5")); string(data) != "fakeagent "+runSession {
		t.Errorf("note = %q", data)
	}
	names, _ := os.ReadDir(e.NotesDir)
	if len(names) != 1 {
		t.Errorf("notes folder holds %d files, want 1", len(names))
	}
}

// statLine is a /proc/<pid>/stat line with a parent and a start time.
func statLine(pid, ppid int, start string) string {
	fields := []string{"S", strconv.Itoa(ppid)}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	return fmt.Sprintf("%d (fakeagent) %s %s", pid, strings.Join(fields, " "), start)
}

func TestPruneNotes(t *testing.T) {
	root := t.TempDir()
	for pid, start := range map[int]string{10: "500", 11: "600"} {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(statLine(pid, 1, start)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })

	notes := t.TempDir()
	for _, n := range []string{"10-500", "11-599", "12-700", "10-500.tmp-1", "12-700.tmp-2", "junk"} {
		if err := os.WriteFile(filepath.Join(notes, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneNotes(notes); err != nil {
		t.Fatal(err)
	}
	des, _ := os.ReadDir(notes)
	var left []string
	for _, de := range des {
		left = append(left, de.Name())
	}
	// 10 runs since tick 500; 11 since 600, so 11-599 was another process
	// with its pid; 12 is gone.
	if want := "10-500 10-500.tmp-1"; strings.Join(left, " ") != want {
		t.Errorf("left %q, want %q", left, want)
	}
	if err := PruneNotes(filepath.Join(notes, "none")); err != nil {
		t.Errorf("no folder: %v", err)
	}
}

func TestProcLineage(t *testing.T) {
	root := t.TempDir()
	// 1 init, 50 the parent agent, 60 a shell, 70 the run's agent, and the
	// test process as its child.
	self := os.Getpid()
	procs := map[int]string{
		50:   statLine(50, 1, "111"),
		60:   strings.Replace(statLine(60, 50, "222"), "(fakeagent)", "(bash)", 1),
		70:   statLine(70, 60, "333"),
		self: strings.Replace(statLine(self, 70, "444"), "(fakeagent)", "(hook)", 1),
	}
	for pid, stat := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })
	got, up := lineageFrom(self, []string{"fakeagent"})
	if got != "70-333" || up != "50-111" {
		t.Errorf("lineage = %q, %q; want 70-333, 50-111", got, up)
	}
}
