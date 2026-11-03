package conformance

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/selftest"
	"github.com/influento/tmux-plugins/tmux-agent/internal/store"
)

func TestMain(m *testing.M) { selftest.Main(m) }

// goDriver runs tmux-agent, wired the way dotfiles wires it: every hook
// event through `tmux-agent hook claude`, no matchers.
type goDriver struct{}

func (d *goDriver) Name() string { return "go" }

func (d *goDriver) Setup(t *testing.T, sb *tmuxtest.Sandbox) {
	t.Helper()
	selftest.Install(t, sb)
}

// agentIn runs tmux-agent as if from inside pane; "" means the first pane.
func agentIn(t *testing.T, s *tmuxtest.Server, pane string, args ...string) string {
	t.Helper()
	if pane == "" {
		pane = s.MustTmux(t, "display-message", "-p", "-t", "proj-a:0.0", "#{pane_id}")
	}
	return mustRun(t, s, pane, "", "tmux-agent", args...)
}

func (d *goDriver) Set(t *testing.T, s *tmuxtest.Server, pane string, f Flag) string {
	t.Helper()
	args := []string{"flag", f.State, "--reason", f.Reason, "--pane", pane}
	if f.Owner != "" {
		args = append(args, "--owner", f.Owner)
	}
	if f.Quiet {
		args = append(args, "--quiet")
	}
	return strings.TrimSuffix(agentIn(t, s, pane, args...), "\n")
}

func (d *goDriver) Clear(t *testing.T, s *tmuxtest.Server, pane string) {
	agentIn(t, s, pane, "flag", "clear", "--pane", pane)
}

func (d *goDriver) Seen(t *testing.T, s *tmuxtest.Server, pane string) {
	agentIn(t, s, pane, "seen", pane)
}

func (d *goDriver) Status(t *testing.T, s *tmuxtest.Server) { agentIn(t, s, "", "status") }

func (d *goDriver) Sweep(t *testing.T, s *tmuxtest.Server) { agentIn(t, s, "", "sweep") }

// List reads the JSON list, which F3 and F5 build on.
func (d *goDriver) List(t *testing.T, s *tmuxtest.Server) []ListRow {
	t.Helper()
	var rows []ListRow
	if err := json.Unmarshal([]byte(agentIn(t, s, "", "list", "--json")), &rows); err != nil {
		t.Fatalf("list --json: %v", err)
	}
	return rows
}

func (d *goDriver) SessionRows(t *testing.T, s *tmuxtest.Server) []SessionRow {
	t.Helper()
	var rows []SessionRow
	for _, line := range strings.Split(strings.TrimSuffix(agentIn(t, s, "", "rows"), "\n"), "\n") {
		if line == "" {
			continue
		}
		name, display, _ := strings.Cut(line, "\t")
		rows = append(rows, SessionRow{Session: name, Display: ansiCode.ReplaceAllString(display, "")})
	}
	return rows
}

func (d *goDriver) KillSession(t *testing.T, s *tmuxtest.Server, name string) {
	agentIn(t, s, "", "kill-session", name)
}

func (d *goDriver) Jump(t *testing.T, s *tmuxtest.Server, from string) { agentIn(t, s, from, "jump") }

func (d *goDriver) Borders(t *testing.T, s *tmuxtest.Server, window string) {
	agentIn(t, s, "", "borders", window)
}

func (d *goDriver) Hook(t *testing.T, s *tmuxtest.Server, pane string, ev Event) string {
	t.Helper()
	return mustRun(t, s, pane, ev.JSON(), "tmux-agent", "hook", "claude")
}

func (d *goDriver) HookOutsideTmux(t *testing.T, s *tmuxtest.Server, ev Event) (string, error) {
	t.Helper()
	return run(t, s, "", ev.JSON(), "tmux-agent", "hook", "claude")
}

// AgentSweep is the sweep tmux runs when a pane closes.
func (d *goDriver) AgentSweep(t *testing.T, s *tmuxtest.Server) { agentIn(t, s, "", "sweep") }

func paneOpt(t *testing.T, s *tmuxtest.Server, scope, pane, name string) string {
	t.Helper()
	out, err := s.Tmux("show-options", "-qv", scope, "-t", pane, name)
	if err != nil {
		t.Fatalf("show-options %s %s: %v", scope, name, err)
	}
	return out
}

// Record reads the pane's live record. A conversation resumes after a
// restart when the pane carries a resume command for tmux-revive to type.
func (d *goDriver) Record(t *testing.T, s *tmuxtest.Server, pane string) Record {
	t.Helper()
	return Record{
		Exists:     paneOpt(t, s, "-p", pane, "@revive_cmd") != "",
		SessionID:  paneOpt(t, s, "-p", pane, "@agent_session"),
		Transcript: paneOpt(t, s, "-p", pane, "@agent_transcript"),
		Cwd:        paneOpt(t, s, "-p", pane, "@agent_cwd"),
		State:      paneOpt(t, s, "-p", pane, "@agent_state"),
		Reason:     paneOpt(t, s, "-p", pane, "@agent_reason"),
		Title:      paneOpt(t, s, "-w", pane, "@agent_title"),
	}
}

// SeedRecord leaves the session's record in the store, and what the
// launcher leaves on the pane: for a resume, the run's --resume session;
// otherwise the session of an agent already running.
func (d *goDriver) SeedRecord(t *testing.T, s *tmuxtest.Server, pane string, r Record, resuming bool) {
	t.Helper()
	st := &store.Store{Dir: filepath.Join(s.StateHome, "tmux-agent")}
	if err := st.Save(store.Session{
		Agent: "claude", ID: r.SessionID, Transcript: r.Transcript, Cwd: r.Cwd,
		State: r.State, Reason: r.Reason, Title: r.Title, Updated: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	opt := "@agent_session"
	if resuming {
		opt = "@agent_resume"
	}
	s.MustTmux(t, "set-option", "-p", "-t", pane, "@agent", "claude", ";", "set-option", "-p", "-t", pane, opt, r.SessionID)
}

func (d *goDriver) TitleOption() string { return "@agent_title" }

func (d *goDriver) KnownBug(string) string { return "" }
