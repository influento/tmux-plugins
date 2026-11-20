package contract

import (
	"os"
	"path/filepath"
	"testing"
)

// A hook sent from outside its pane counts no agent above it as its own:
// tests that send hooks to a private server run under the agent that runs
// them, a claude -p of a review inside an agent's session. The pane's
// session is the hook's, with a resume command and no role, working.
func TestHookOutsideItsPane(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	hooks := e.hooks(t, "inner.jsonl",
		hookEvent("SessionStart", sessionA, "source", "startup", "cwd", e.Home),
		hookEvent("UserPromptSubmit", sessionA, "prompt", "go"),
	)
	if err := os.WriteFile(filepath.Join(e.dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := e.Command(t, e.pane, "claude")
	cmd.Env = append(cmd.Env, "STUB_MODE=nested", "STUB_INNER_HOOK="+hooks, "STUB_INNER=claude -p inner --agent role-r")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claude: %v: %s", err, out)
	}
	if got := e.file(t, "inner.rc"); got != "0" {
		t.Fatalf("the inner agent's exit code = %s, want 0", got)
	}
	e.wantOpt(t, "@agent_session", sessionA)
	e.wantOpt(t, "@revive_cmd", "tmux-agent revive claude "+sessionA)
	e.wantOpt(t, "@agent_role", "")
	e.wantOpt(t, "@agent_state", "working")
}
