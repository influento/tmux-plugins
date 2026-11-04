package contract

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
	"github.com/influento/tmux-plugins/tmux-agent/internal/agent/claude"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

// agent runs tmux-agent as tmux runs it from a hook, for the pane.
func (e *env) agent(t *testing.T, args ...string) {
	t.Helper()
	if out, err := e.Command(t, e.pane, "tmux-agent", args...).CombinedOutput(); err != nil {
		t.Fatalf("tmux-agent %v: %v: %s", args, err, out)
	}
}

// A claude started without the launcher, as a script runs it, sets the
// resume command only for a session it can resume: not for claude -p.
func TestReviveCommandWithoutLauncher(t *testing.T) {
	resume := "tmux-agent revive claude " + sessionA
	for _, c := range []struct{ name, args, want string }{
		{"interactive", "", resume},
		{"one-shot", " -p 'say hi'", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			hooks := e.hooks(t, "start.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home))
			e.launch(t, "rc", "STUB_MODE=wait STUB_HOOK="+shellQuote(hooks)+" claude"+c.args)
			e.file(t, "agent.pid")
			e.wantOpt(t, "@agent_session", sessionA)
			e.wantOpt(t, "@revive_cmd", c.want)
			e.release(t)
			e.number(t, "rc")
		})
	}
}

// A claude that dies without a word, such as one killed or crashed, takes
// its resume command with it on the next sweep. A stopped one keeps it.
func TestReviveCommandOfAGoneAgent(t *testing.T) {
	resume := "tmux-agent revive claude " + sessionA
	t.Run("killed", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		hooks := e.hooks(t, "start.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home))
		e.launch(t, "rc", "STUB_MODE=wait STUB_HOOK="+shellQuote(hooks)+" claude")
		pid := e.number(t, "agent.pid")
		e.wantOpt(t, "@revive_cmd", resume)
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		if rc := e.number(t, "rc"); rc != 128+int(syscall.SIGKILL) {
			t.Fatalf("exit code = %d, want %d", rc, 128+int(syscall.SIGKILL))
		}
		for _, cmd := range []string{"status", "seen", "sweep"} {
			t.Run(cmd, func(t *testing.T) {
				e.MustTmux(t, "set-option", "-p", "-t", e.pane, "@revive_cmd", resume)
				e.MustTmux(t, "set-option", "-p", "-t", e.pane, "@agent", "claude")
				e.agent(t, cmd)
				e.wantOpt(t, "@revive_cmd", "")
				e.wantOpt(t, "@agent", "")
			})
		}
	})
	t.Run("stopped", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)
		hooks := e.hooks(t, "start.jsonl", hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home))
		e.launch(t, "rc", "STUB_MODE=raw STUB_HOOK="+shellQuote(hooks)+" claude")
		pid := e.number(t, "agent.pid")
		e.file(t, "agent.raw")
		e.keys(t, "C-z")
		if st := waitState(t, pid, 'T'); st != 'T' {
			t.Fatalf("agent %d state = %c, want stopped", pid, st)
		}
		e.agent(t, "status")
		e.wantOpt(t, "@revive_cmd", resume)
		e.wantOpt(t, "@agent_session", sessionA)
	})
}

// A restore names the window after the session's title and types the
// resume command. When the resume fails, as it does for a session whose
// transcript is gone, the window gets its automatic name back, and the
// pane drops the command.
func TestRestoredResumeFails(t *testing.T) {
	for _, c := range []struct {
		name       string
		transcript bool   // the session's transcript is still there
		code       string // what the agent exits with
		wantRC     int
		started    bool // the agent ran
	}{
		{"the agent fails", true, "1", 1, true},
		{"the transcript is gone", false, "0", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			transcript := filepath.Join(e.Home, "transcript.jsonl")
			if c.transcript {
				if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			st := &store.Store{Dir: filepath.Join(e.StateHome, "tmux-agent")}
			if err := st.Save(store.Session{
				Agent: "claude", ID: sessionA, Cwd: e.Home, Transcript: transcript,
				Title: "write the docs", End: "killed", Updated: time.Now().Unix(),
			}); err != nil {
				t.Fatal(err)
			}
			// The pane as a restore leaves it: the saved name, and the command.
			cmd, ok := agent.ReviveCommand(claude.Claude{}, sessionA)
			if !ok {
				t.Fatal("no resume command")
			}
			e.MustTmux(t, "rename-window", "-t", e.pane, "write the docs")
			e.MustTmux(t, "set-option", "-p", "-t", e.pane, "@revive_cmd", cmd)
			e.launch(t, "rc", "STUB_CODE="+c.code+" "+cmd)
			if rc := e.number(t, "rc"); rc != c.wantRC {
				t.Errorf("exit code = %d, want %d", rc, c.wantRC)
			}
			_, err := os.Stat(filepath.Join(e.dir, "agent.pid"))
			if started := err == nil; started != c.started {
				t.Errorf("agent started = %v, want %v", started, c.started)
			}
			if got := e.MustTmux(t, "show-options", "-wv", "-t", e.pane, "automatic-rename"); got != "on" {
				t.Errorf("automatic-rename = %q, want on", got)
			}
			if got := e.MustTmux(t, "show-options", "-wqv", "-t", e.pane, "@agent_title"); got != "" {
				t.Errorf("@agent_title = %q, want none", got)
			}
			e.wantOpt(t, "@revive_cmd", "")
		})
	}
}

// The resume command a restore types brings the session back through the
// launcher, with its title; the title goes when the run ends.
func TestRestoredResume(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	transcript := filepath.Join(e.Home, "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{Dir: filepath.Join(e.StateHome, "tmux-agent")}
	if err := st.Save(store.Session{
		Agent: "claude", ID: sessionA, Cwd: e.Home, Transcript: transcript, State: "needs-you",
		Reason: "Bash(make)", Title: "write the docs", End: "killed", Updated: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	cmd, _ := agent.ReviveCommand(claude.Claude{}, sessionA)
	e.MustTmux(t, "rename-window", "-t", e.pane, "write the docs")
	e.MustTmux(t, "set-option", "-p", "-t", e.pane, "@revive_cmd", cmd)
	hooks := e.hooks(t, "resume.jsonl", hookEvent("SessionStart", sessionA, "source", "resume", "cwd", e.Home, "transcript_path", transcript))
	e.launch(t, "rc", "STUB_MODE=wait STUB_HOOK="+shellQuote(hooks)+" "+cmd)
	if got := e.args(t, "agent"); len(got) != 2 || got[0] != "--resume" || got[1] != sessionA {
		t.Errorf("agent args = %q, want --resume %s", got, sessionA)
	}
	e.wantFlag(t, "needs-you", "before restart: Bash(make)")
	if got := e.MustTmux(t, "show-options", "-wqv", "-t", e.pane, "@agent_title"); got != "write the docs" {
		t.Errorf("@agent_title = %q, want the recorded title", got)
	}
	e.wantOpt(t, "@revive_cmd", cmd)
	e.release(t)
	if rc := e.number(t, "rc"); rc != 0 {
		t.Errorf("exit code = %d, want 0", rc)
	}
	if got := e.MustTmux(t, "show-options", "-wv", "-t", e.pane, "automatic-rename"); got != "on" {
		t.Errorf("automatic-rename after the run = %q, want on", got)
	}
}
