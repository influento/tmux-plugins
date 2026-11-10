// Package claude is the Claude Code adapter: it translates Claude Code's
// hook payloads, arguments and transcripts into the agent layer's terms, and
// touches neither tmux nor flags nor records.
//
// Dotfiles' settings.json sends all of Claude Code's hook events, without
// matchers, to `tmux-agent hook claude`; the filtering the matchers did is
// done here:
//
//	event                                    neutral event
//	SessionStart (source compact: dropped)   session start; resume for source resume
//	UserPromptSubmit, PreToolUse, PostToolUse working
//	PreToolUse AskUserQuestion               needs input  "question: …"
//	PermissionRequest                        needs input  "Bash(…)", "Edit(file)"
//	Notification elicitation_dialog,         needs input  the message
//	  agent_needs_input (others: dropped)
//	Stop                                     turn end
//	SessionEnd                               session end: unknown for reason
//	                                         "other" (a kill, or a -p run
//	                                         ending), exited otherwise
//
// Every event carries the session's folder (cwd), which follows the agent
// into a worktree (EnterWorktree, claude -w).
//
// A turn the user cuts short (Esc, a declined permission) sends no hook;
// Interrupted reads it from the transcript for the agent layer's sweep.
package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/influento/tmux-plugins/tmux-agent/internal/agent"
)

// Claude is Claude Code.
type Claude struct{}

var (
	_ agent.Agent        = Claude{}
	_ agent.HookReader   = Claude{}
	_ agent.OneShotter   = Claude{}
	_ agent.Resumer      = Claude{}
	_ agent.ProcessNamer = Claude{}
	_ agent.Titler       = Claude{}
	_ agent.Interrupter  = Claude{}
)

// Name is the registry name.
func (Claude) Name() string { return "claude" }

// Command is the program.
func (Claude) Command() string { return "claude" }

// ProcessNames are the names claude runs under. An npm-installed claude
// runs as node and sets its process name to claude.
func (Claude) ProcessNames() []string { return []string{"claude"} }

// Mark is Claude Code's mark in the panel: its spinner's star, in its colour.
func (Claude) Mark() (glyph, colour string) { return "✳", "#d97757" }

// OneShot reports a -p/--print run: one turn, then exit.
func (Claude) OneShot(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-p" || a == "--print" || strings.HasPrefix(a, "--print=") {
			return true
		}
	}
	return false
}

// ResumeArgs resume a session.
func (Claude) ResumeArgs(session string) []string { return []string{"--resume", session} }

// ResumedSession is the session --resume (or -r) names; "" for none, or for
// --resume without an id, which opens a picker.
func (Claude) ResumedSession(args []string) string {
	for i, a := range args {
		switch {
		case a == "--":
			return ""
		case a == "--resume" || a == "-r":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return args[i+1]
			}
			return ""
		case strings.HasPrefix(a, "--resume="):
			return strings.TrimPrefix(a, "--resume=")
		}
	}
	return ""
}

// fields is a JSON object read leniently: a field of another type than
// expected reads as empty, never as an error.
type fields map[string]json.RawMessage

// str returns a field as text: a string as it is, a number or a boolean as
// written, anything else as "". Newlines become spaces, so a value fits a
// tmux option and a flag.
func (f fields) str(key string) string {
	raw, ok := f[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t := strings.TrimSpace(string(raw))
		if t == "" || t == "null" || t[0] == '{' || t[0] == '[' || t[0] == '"' {
			return ""
		}
		s = t
	}
	return strings.ReplaceAll(s, "\n", " ")
}

func (f fields) obj(key string) fields {
	var o fields
	if raw, ok := f[key]; ok {
		_ = json.Unmarshal(raw, &o) // not an object: no fields
	}
	return o
}

// ReadHook maps one hook payload to a neutral event.
func (Claude) ReadHook(payload []byte) (agent.Event, bool, error) {
	var f fields
	if err := json.Unmarshal(payload, &f); err != nil {
		return agent.Event{}, false, err
	}
	ev := agent.Event{SessionID: f.str("session_id"), Transcript: f.str("transcript_path"), Cwd: f.str("cwd")}
	switch f.str("hook_event_name") {
	case "SessionStart":
		source := f.str("source")
		if source == "compact" {
			// Compaction restarts the session mid-turn; nothing changed
			// for the pane.
			return agent.Event{}, false, nil
		}
		ev.Kind, ev.Resume = agent.SessionStart, source == "resume"
	case "SessionEnd":
		ev.Kind, ev.End = agent.SessionEnd, agent.EndExited
		if f.str("reason") == "other" {
			ev.End = agent.EndUnknown
		}
	case "UserPromptSubmit":
		ev.Kind = agent.Working
	case "PreToolUse":
		ev.Tool = toolKey(f)
		if f.str("tool_name") == "AskUserQuestion" {
			q := f.obj("tool_input").question()
			ev.Kind, ev.Reason = agent.NeedsInput, "question: "+or(q, "Claude asks")
		} else {
			ev.Kind = agent.Working
		}
	case "PostToolUse":
		ev.Kind, ev.ToolEnd, ev.Tool = agent.Working, true, toolKey(f)
	case "PermissionRequest":
		ev.Kind, ev.Tool = agent.NeedsInput, toolKey(f)
		ev.Reason = label(or(f.str("tool_name"), "tool"), f.obj("tool_input"))
	case "Notification":
		switch f.str("notification_type") {
		case "elicitation_dialog", "agent_needs_input":
			ev.Kind, ev.Reason = agent.NeedsInput, truncRunes(or(f.str("message"), "needs input"), 64)
		default:
			return agent.Event{}, false, nil
		}
	case "Stop":
		ev.Kind = agent.TurnEnd
	default:
		return agent.Event{}, false, nil
	}
	return ev, true, nil
}

// question is the first question an AskUserQuestion input asks.
func (f fields) question() string {
	var qs []fields
	if raw, ok := f["questions"]; ok {
		_ = json.Unmarshal(raw, &qs) // not a list: no question
	}
	if len(qs) == 0 {
		return ""
	}
	return qs[0].str("question")
}

// label is a short name for what a tool wants: Bash(the command),
// Edit(the file).
func label(tool string, input fields) string {
	switch tool {
	case "Bash":
		return "Bash(" + truncRunes(input.str("command"), 48) + ")"
	case "Edit", "Write", "Read", "NotebookEdit":
		p := input.str("file_path")
		return tool + "(" + p[strings.LastIndexByte(p, '/')+1:] + ")"
	case "Grep", "Glob":
		return tool + "(" + truncRunes(input.str("pattern"), 32) + ")"
	}
	return tool
}

// toolKey identifies a tool call by its name and input, which a
// permission request and the tool's end both carry. Keys are compared,
// never shown, so a hash keeps the pane option short.
func toolKey(f fields) string {
	var input any
	if raw, ok := f["tool_input"]; ok {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		_ = dec.Decode(&input) // no input: the name alone
	}
	canon, _ := json.Marshal(input) // maps marshal with sorted keys
	sum := sha256.Sum256(append([]byte(f.str("tool_name")+"\x00"), canon...))
	return hex.EncodeToString(sum[:8])
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
