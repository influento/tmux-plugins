package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/attention"
	"github.com/influento/tmux-plugins/tmux-agent/internal/git"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// fileAgent reads what its adapter would from a transcript that holds one
// JSON state; a file named after a subagent in the folder "subagents"
// beside it says the subagent ended.
type fileAgent struct{}

type fileState struct {
	Used, Limit int64
	Cut         time.Time
	Title       string
}

func (fileAgent) Name() string           { return "fakeagent" }
func (fileAgent) Command() string        { return "fakeagent" }
func (fileAgent) ProcessNames() []string { return []string{"fakeagent"} }

func (fileAgent) read(transcript string) fileState {
	var s fileState
	if data, err := os.ReadFile(transcript); err == nil {
		_ = json.Unmarshal(data, &s) // a write in progress reads as none
	}
	return s
}

func (f fileAgent) Context(transcript, _, _ string, _ func(string) string) (int64, int64, bool) {
	s := f.read(transcript)
	return s.Used, s.Limit, s.Used > 0
}

func (f fileAgent) Interrupted(transcript string) (time.Time, bool) {
	s := f.read(transcript)
	return s.Cut, !s.Cut.IsZero()
}

func (f fileAgent) Title(transcript string) (string, bool) { return f.read(transcript).Title, true }

func (fileAgent) WatchedFiles(transcript string) []string {
	return []string{filepath.Join(filepath.Dir(transcript), "subagents")}
}

func (fileAgent) SubagentEnded(transcript, id string) Outcome {
	if _, err := os.Stat(filepath.Join(filepath.Dir(transcript), "subagents", id)); err == nil {
		return OutcomeStopped
	}
	return ""
}

// A watcher keeps the pane live from what no hook tells: the context
// share, the title, a turn cut short, a subagent stopped, a branch
// switched elsewhere and the runs the agent starts, which hold its turn
// until they end. It is the pane's only one, and it ends the session when
// the agent exits without a word.
func TestWatch(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	ctx := context.Background()

	// The agent, a script named fakeagent run as fakeagent, starts a
	// nested run of one.
	leaf := filepath.Join(s.Dir, "leaf")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sleep, filepath.Join(leaf, "fakeagent")); err != nil {
		t.Fatal(err)
	}
	agentBin := filepath.Join(s.Dir, "fakeagent")
	write(t, agentBin, "#!/bin/sh\n"+leaf+"/fakeagent 600 &\nsleep 600\n")
	if err := os.Chmod(agentBin, 0o700); err != nil {
		t.Fatal(err)
	}
	// The agent is the pane's process, so tmux names the pane after it; the
	// pane stays when it exits.
	pane := s.MustTmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "exec bash -c 'exec -a fakeagent sh "+agentBin+"'")
	s.MustTmux(t, "set-option", "-w", "-t", pane, "remain-on-exit", "on")
	panePid, err := strconv.Atoi(s.Display(t, pane, "#{pane_pid}"))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"fakeagent"}
	var agentPid, runPid int
	tmuxtest.WaitFor(t, 5*time.Second, "the agent and its run", func() bool {
		tree := processTree()
		agentPid = firstAgentBelow(panePid, names, tree)
		if runs := nestedProcs(panePid, names, tree); len(runs) == 1 {
			runPid = runs[0].pid
		}
		return agentPid > 0 && runPid > 0
	})

	dir := filepath.Join(s.Dir, "proj-a")
	gitDir := filepath.Join(dir, ".git")
	for _, d := range []string{filepath.Join(dir, "subagents"), gitDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(gitDir, "HEAD"), "main")
	transcript := filepath.Join(dir, sessionA+".jsonl")
	state := func(st fileState) {
		t.Helper()
		data, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		// Renamed into place, so a read never sees half of it.
		write(t, transcript+".tmp", string(data))
		if err := os.Rename(transcript+".tmp", transcript); err != nil {
			t.Fatal(err)
		}
	}
	st := fileState{Used: 20_000, Limit: 200_000}
	state(st)

	server := s.Display(t, pane, "#{pid}")
	getenv := func(k string) string {
		return map[string]string{"TMUX": s.Socket + "," + server + ",0", "XDG_RUNTIME_DIR": s.RuntimeDir}[k]
	}
	a := fileAgent{}
	e := &Env{
		Tmux: s.Client, Core: &attention.Core{Tmux: s.Client}, Registry: NewRegistry(a), Pane: pane,
		Store:    &store.Store{Dir: s.StateHome},
		LockPath: filepath.Join(s.RuntimeDir, "lock"),
		Getenv:   getenv,
		Git: func(_ context.Context, d string) (git.Head, error) {
			if d != dir {
				return git.Head{}, git.ErrNotARepo
			}
			head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			return git.Head{Branch: strings.TrimSpace(string(head)), Dir: gitDir}, err
		},
	}
	if err := e.Apply(ctx, a, Event{Kind: SessionStart, SessionID: sessionA, Transcript: transcript, Cwd: dir}); err != nil {
		t.Fatal(err)
	}
	show := func(format string) string { return s.Display(t, pane, format) }
	shows := func(what, format, want string) {
		t.Helper()
		tmuxtest.WaitFor(t, 5*time.Second, what, func() bool { return show(format) == want })
	}
	shows("the share at the start", "#{"+attention.OptContext+"}", "10")

	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- e.Watch(watchCtx, a, agentPid) }()
	tmuxtest.WaitFor(t, 5*time.Second, "the watcher to take wakes", func() bool { return e.wake(pane, wakePing) })
	if err := e.Watch(ctx, a, agentPid); !errors.Is(err, ErrWatched) {
		t.Fatalf("a second watcher: %v, want ErrWatched", err)
	}
	shows("the nested run", "#{"+attention.OptNested+"}", "fakeagent")

	st.Used = 50_000
	state(st)
	shows("the share after a model call", "#{"+attention.OptContext+"}", "25")

	st.Title = "probe"
	state(st)
	shows("the title", "#{window_name}", "probe")

	if err := e.Apply(ctx, a, Event{Kind: Working, SessionID: sessionA}); err != nil {
		t.Fatal(err)
	}
	if got := show("#{@attention}"); got != "working" {
		t.Fatalf("flag %q, want working", got)
	}
	st.Cut = time.Now().Add(time.Second)
	state(st)
	shows("the cut turn's flag gone", "#{@attention}", "")
	st.Cut = time.Time{}
	state(st)

	if err := e.Apply(ctx, a, Event{Kind: SubagentStart, SessionID: sessionA, By: "sub-a", Subagent: &Subagent{ID: "sub-a"}}); err != nil {
		t.Fatal(err)
	}
	shows("the subagent", "#{"+attention.OptSubagents+"}", "sub-a")
	write(t, filepath.Join(dir, "subagents", "sub-a"), "")
	shows("the stopped subagent gone", "#{"+attention.OptSubagents+"}", "")

	write(t, filepath.Join(gitDir, "HEAD"), "topic")
	tmuxtest.WaitFor(t, 5*time.Second, "the new branch", func() bool { return strings.Contains(show("#{@labels}"), "topic") })

	if err := e.Apply(ctx, a, Event{Kind: Working, SessionID: sessionA}); err != nil {
		t.Fatal(err)
	}
	if err := e.paneOpts(ctx, map[string]string{optHeld: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(runPid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	tmuxtest.WaitFor(t, 5*time.Second, "the run's end", func() bool {
		return show("#{"+attention.OptNested+"}|#{"+optHeld+"}|#{@attention}") == "||done"
	})

	if err := syscall.Kill(agentPid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher outlived its agent")
	}
	if got := show("#{" + optAgent + "}|#{" + attention.OptContext + "}"); got != "|" {
		t.Errorf("agent|share = %q after the agent went, want the session ended", got)
	}
	if e.wake(pane, wakePing) {
		t.Error("a wake was taken after the watcher ended")
	}
}

// A hook tells the pane's watcher what may change what it follows; a
// session's end tells nothing.
func TestKeepWatched(t *testing.T) {
	dir, err := os.MkdirTemp("", "tw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	getenv := func(k string) string {
		return map[string]string{"TMUX": "/s,4242,0", "XDG_RUNTIME_DIR": dir}[k]
	}
	e := &Env{Getenv: getenv, Pane: "%3", Registry: NewRegistry(fileAgent{})}
	conn, release, err := listenWakes(watchSocket(getenv, "%3"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, tc := range []struct {
		kind Kind
		want string
	}{
		{SessionStart, wakeScan}, {Working, wakePing}, {NeedsInput, wakePing}, {SubagentStart, wakeScan},
		{SubagentEnd, wakePing}, {TurnEnd, wakeScan}, {Compacted, wakeScan}, {SessionEnd, ""},
	} {
		e.keepWatched(fileAgent{}, Event{Kind: tc.kind})
		buf := make([]byte, 16)
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _ := conn.Read(buf)
		if got := string(buf[:n]); got != tc.want {
			t.Errorf("kind %v: wake %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// tmux-init starts a watcher for each agent pane that has none, not for a
// one-turn run's pane or one with a live watcher.
func TestWatchAll(t *testing.T) {
	s := tmuxtest.Start(t, tmuxtest.Options{Session: "proj-a"})
	script := filepath.Join(s.Dir, "fakeagent")
	write(t, script, "#!/bin/sh\nsleep 600\n")
	agentPane := func(oneShot string) (string, int) {
		t.Helper()
		pane := s.MustTmux(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "exec bash -c 'exec -a fakeagent sh "+script+"'")
		s.MustTmux(t, "set-option", "-p", "-t", pane, optAgent, "fakeagent", ";", "set-option", "-p", "-t", pane, optOneShot, oneShot)
		pid, err := strconv.Atoi(s.Display(t, pane, "#{pane_pid}"))
		if err != nil {
			t.Fatal(err)
		}
		tmuxtest.WaitFor(t, 5*time.Second, "the agent", func() bool {
			return firstAgentBelow(pid, []string{"fakeagent"}, processTree()) == pid
		})
		return pane, pid
	}
	unwatched, pid := agentPane("")
	watched, _ := agentPane("")
	oneShot, _ := agentPane("1")
	s.MustTmux(t, "new-window", "-d") // no agent

	getenv := func(k string) string {
		return map[string]string{"TMUX": s.Socket + "," + s.Display(t, unwatched, "#{pid}") + ",0", "XDG_RUNTIME_DIR": s.RuntimeDir}[k]
	}
	_, release, err := listenWakes(watchSocket(getenv, watched))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var started []string
	e := &Env{
		Tmux: s.Client, Registry: NewRegistry(fileAgent{}), Getenv: getenv,
		Watcher: func(a Agent, pane string, pid int) {
			started = append(started, a.Name()+" "+pane+" "+strconv.Itoa(pid))
		},
	}
	if err := e.WatchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"fakeagent " + unwatched + " " + strconv.Itoa(pid)}
	if strings.Join(started, ",") != strings.Join(want, ",") {
		t.Errorf("watchers started %q, want %q (one-shot pane %s)", started, want, oneShot)
	}
}
