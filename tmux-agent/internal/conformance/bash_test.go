package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influento/tmux-plugins/internal/tmux/tmuxtest"
)

// bashDriver runs tmux-attention and claude-tmux from a dotfiles checkout,
// wired the way dotfiles' settings.json wires them.
type bashDriver struct {
	dotfiles string
}

func (d *bashDriver) Name() string { return "bash" }

func (d *bashDriver) Setup(t *testing.T, sb *tmuxtest.Sandbox) {
	t.Helper()
	tmuxtest.Require(t, "python3", "awk")
	for _, name := range []string{"tmux-attention", "claude-tmux"} {
		src := filepath.Join(d.dotfiles, "common", "scripts", name)
		if _, err := os.Stat(src); err != nil {
			t.Fatalf("bash driver: %v", err)
		}
		sb.Link(t, name, src)
	}
}

// attention runs tmux-attention as if from inside pane.
func attention(t *testing.T, s *tmuxtest.Server, pane string, args ...string) string {
	t.Helper()
	if pane == "" {
		pane = s.MustTmux(t, "display-message", "-p", "-t", "proj-a:0.0", "#{pane_id}")
	}
	return mustRun(t, s, pane, "", "tmux-attention", args...)
}

func (d *bashDriver) Set(t *testing.T, s *tmuxtest.Server, pane string, f Flag) string {
	t.Helper()
	args := []string{"set", f.State, "--reason", f.Reason, "--pane", pane}
	if f.Owner != "" {
		args = append(args, "--owner", f.Owner)
	}
	if f.Quiet {
		args = append(args, "--quiet")
	}
	return strings.TrimSuffix(attention(t, s, pane, args...), "\n")
}

func (d *bashDriver) Clear(t *testing.T, s *tmuxtest.Server, pane string) {
	attention(t, s, pane, "clear", "--pane", pane)
}

func (d *bashDriver) Seen(t *testing.T, s *tmuxtest.Server, pane string) {
	attention(t, s, pane, "seen", pane)
}

func (d *bashDriver) Status(t *testing.T, s *tmuxtest.Server) { attention(t, s, "", "status") }

func (d *bashDriver) Sweep(t *testing.T, s *tmuxtest.Server) { attention(t, s, "", "sweep") }

func (d *bashDriver) List(t *testing.T, s *tmuxtest.Server) []ListRow {
	t.Helper()
	var rows []ListRow
	for _, line := range strings.Split(strings.TrimSuffix(attention(t, s, "", "list"), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			t.Fatalf("list line %q: %d fields, want 7", line, len(f))
		}
		rank, err := strconv.Atoi(f[0])
		if err != nil {
			t.Fatalf("list line %q: rank: %v", line, err)
		}
		rows = append(rows, ListRow{Rank: rank, Pane: f[1], Glyph: f[2], Window: f[3], Target: f[4], Detail: f[5], Age: f[6]})
	}
	return rows
}

var ansiCode = regexp.MustCompile("\x1b\\[[0-9;]*m")

func (d *bashDriver) SessionRows(t *testing.T, s *tmuxtest.Server) []SessionRow {
	t.Helper()
	var rows []SessionRow
	for _, line := range strings.Split(strings.TrimSuffix(attention(t, s, "", "rows"), "\n"), "\n") {
		if line == "" {
			continue
		}
		name, display, _ := strings.Cut(line, "\t")
		rows = append(rows, SessionRow{Session: name, Display: ansiCode.ReplaceAllString(display, "")})
	}
	return rows
}

func (d *bashDriver) KillSession(t *testing.T, s *tmuxtest.Server, name string) {
	attention(t, s, "", "kill-session", name)
}

func (d *bashDriver) Jump(t *testing.T, s *tmuxtest.Server, from string) {
	attention(t, s, from, "jump")
}

func (d *bashDriver) Borders(t *testing.T, s *tmuxtest.Server, window string) {
	attention(t, s, "", "borders", window)
}

// signals maps a hook event to the claude-tmux calls dotfiles'
// settings.json makes for it, matchers included.
func signals(ev Event) []string {
	switch ev.Name {
	case "SessionStart":
		return []string{"start"}
	case "SessionEnd":
		return []string{"end"}
	case "UserPromptSubmit", "PostToolUse":
		return []string{"working"}
	case "PreToolUse":
		if ev.Payload["tool_name"] == "AskUserQuestion" {
			return []string{"working", "question"}
		}
		return []string{"working"}
	case "PermissionRequest":
		return []string{"permission"}
	case "Notification":
		switch ev.Payload["notification_type"] {
		case "elicitation_dialog", "agent_needs_input":
			return []string{"notification"}
		}
		return nil
	case "Stop":
		return []string{"stop"}
	}
	return nil
}

func (d *bashDriver) Hook(t *testing.T, s *tmuxtest.Server, pane string, ev Event) string {
	t.Helper()
	var out strings.Builder
	for _, sig := range signals(ev) {
		out.WriteString(mustRun(t, s, pane, ev.JSON(), "claude-tmux", "signal", sig))
	}
	return out.String()
}

func (d *bashDriver) HookOutsideTmux(t *testing.T, s *tmuxtest.Server, ev Event) (string, error) {
	t.Helper()
	var out strings.Builder
	for _, sig := range signals(ev) {
		o, err := run(t, s, "", ev.JSON(), "claude-tmux", "signal", sig)
		out.WriteString(o)
		if err != nil {
			return out.String(), err
		}
	}
	return out.String(), nil
}

// AgentSweep runs the sweep as tmux's hooks do: inside the server.
func (d *bashDriver) AgentSweep(t *testing.T, s *tmuxtest.Server) {
	t.Helper()
	pane := s.MustTmux(t, "display-message", "-p", "-t", "proj-a:0.0", "#{pane_id}")
	mustRun(t, s, pane, "", "claude-tmux", "sweep")
}

func (d *bashDriver) recordPath(s *tmuxtest.Server, pane string) string {
	return filepath.Join(s.StateHome, "claude-tmux", strings.ReplaceAll(pane, "/", "_"))
}

func (d *bashDriver) Record(t *testing.T, s *tmuxtest.Server, pane string) Record {
	t.Helper()
	data, err := os.ReadFile(d.recordPath(s, pane))
	if os.IsNotExist(err) {
		return Record{}
	}
	if err != nil {
		t.Fatal(err)
	}
	f := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			f[k] = v
		}
	}
	return Record{
		Exists:     true,
		SessionID:  f["session_id"],
		Transcript: f["transcript"],
		Cwd:        f["cwd"],
		State:      f["state"],
		Reason:     f["reason"],
		Title:      f["title"],
	}
}

func (d *bashDriver) SeedRecord(t *testing.T, s *tmuxtest.Server, pane string, r Record, resuming bool) {
	t.Helper()
	target := s.MustTmux(t, "display-message", "-p", "-t", pane, "#{session_name}:#{window_index}.#{pane_index}")
	lines := []string{
		"target=" + target,
		"session_id=" + r.SessionID,
		"transcript=" + r.Transcript,
		"cwd=" + r.Cwd,
		"state=" + r.State,
		"reason=" + r.Reason,
		"title=" + r.Title,
	}
	if resuming {
		lines = append(lines, "restore=1")
	}
	lines = append(lines, fmt.Sprintf("updated=%d", time.Now().Unix()))
	p := d.recordPath(s, pane)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (d *bashDriver) TitleOption() string { return "@claude_title" }

func (d *bashDriver) KnownBug(name string) string {
	switch name {
	case "TestNotifyNotForVisiblePane":
		return "pane_visible reads #{client_focused}, which tmux 3.8 does not have, so the pane on screen still notifies"
	}
	return ""
}
