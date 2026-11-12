package conformance

// Agent hook events → flags, records and window names: what claude-tmux does
// today (dotfiles common/scripts/CLAUDE.md, "claude-tmux"), with the
// filtering settings.json's matchers do.

import (
	"strings"
	"testing"
	"time"
)

const sessionA = "11111111-2222-4333-8444-555555555555"

func startEvent(source, transcript, cwd string) Event {
	return Event{Name: "SessionStart", Payload: map[string]any{
		"session_id": sessionA, "transcript_path": transcript, "cwd": cwd, "source": source,
	}}
}

func toolEvent(name, tool string, input map[string]any) Event {
	return Event{Name: name, Payload: map[string]any{"session_id": sessionA, "tool_name": tool, "tool_input": input}}
}

func TestHookStartRecordsTheSession(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		e.d.Set(t, e.Server, p, Flag{State: "done", Quiet: true})
		tr := e.writeTranscript(t)
		e.d.Hook(t, e.Server, p, startEvent("startup", tr, e.Home))
		e.wantFlag(t, p, "", "")
		r := e.d.Record(t, e.Server, p)
		if !r.Exists || r.SessionID != sessionA || r.Transcript != tr || r.Cwd != e.Home {
			t.Errorf("record = %+v, want session %s, transcript and cwd", r, sessionA)
		}
	})
}

// Compaction restarts the session mid-turn; nothing changes for the pane.
func TestHookCompactionIsIgnored(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		e.d.Hook(t, e.Server, p, startEvent("startup", e.writeTranscript(t), e.Home))
		e.d.Hook(t, e.Server, p, Event{Name: "UserPromptSubmit", Payload: map[string]any{"prompt": "go"}})
		compact := startEvent("compact", e.writeTranscript(t), e.Home)
		compact.Payload["session_id"] = "99999999-2222-4333-8444-555555555555"
		e.d.Hook(t, e.Server, p, compact)
		e.wantFlag(t, p, "working", "")
		if r := e.d.Record(t, e.Server, p); r.SessionID != sessionA {
			t.Errorf("record session = %q after compaction, want %q", r.SessionID, sessionA)
		}
	})
}

func TestHookWorkingEvents(t *testing.T) {
	events := []Event{
		{Name: "UserPromptSubmit", Payload: map[string]any{"prompt": "go"}},
		toolEvent("PreToolUse", "Bash", map[string]any{"command": "ls"}),
		toolEvent("PostToolUse", "Bash", map[string]any{"command": "ls"}),
	}
	for _, ev := range events {
		t.Run(ev.Name, func(t *testing.T) {
			each(t, func(t *testing.T, e *env) {
				p := e.claudeWindow(t, "proj-a")
				e.d.Hook(t, e.Server, p, ev)
				e.wantFlag(t, p, "working", "")
				if got := e.opt(t, "-p", p, "@attention_owner"); got != "claude" {
					t.Errorf("owner = %q, want claude", got)
				}
			})
		})
	}
}

func TestHookQuestion(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"with a question", map[string]any{"questions": []any{map[string]any{"question": "Which store?"}}}, "question: Which store?"},
		{"without", map[string]any{}, "question: Claude asks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(t *testing.T, e *env) {
				p := e.claudeWindow(t, "proj-a")
				e.d.Hook(t, e.Server, p, toolEvent("PreToolUse", "AskUserQuestion", c.input))
				e.wantFlag(t, p, "needs-you", c.want)
				if r := e.d.Record(t, e.Server, p); r.State != "needs-you" || r.Reason != c.want {
					t.Errorf("record state %q %q, want needs-you %q", r.State, r.Reason, c.want)
				}
			})
		})
	}
}

func TestHookPermissionLabels(t *testing.T) {
	long := strings.Repeat("x", 60)
	cases := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"Bash", map[string]any{"command": "make test"}, "Bash(make test)"},
		{"Bash", map[string]any{"command": long}, "Bash(" + long[:48] + ")"},
		{"Edit", map[string]any{"file_path": "/src/proj-a/main.go"}, "Edit(main.go)"},
		{"Write", map[string]any{"file_path": "notes.md"}, "Write(notes.md)"},
		{"Read", map[string]any{"file_path": "/etc/hosts"}, "Read(hosts)"},
		{"NotebookEdit", map[string]any{"file_path": "/n/a.ipynb"}, "NotebookEdit(a.ipynb)"},
		{"Grep", map[string]any{"pattern": long}, "Grep(" + long[:32] + ")"},
		{"Glob", map[string]any{"pattern": "**/*.go"}, "Glob(**/*.go)"},
		{"WebFetch", map[string]any{"url": "https://example.com"}, "WebFetch"},
	}
	each(t, func(t *testing.T, e *env) {
		for _, c := range cases {
			p := e.claudeWindow(t, "proj-a")
			e.d.Hook(t, e.Server, p, toolEvent("PermissionRequest", c.tool, c.input))
			e.wantFlag(t, p, "needs-you", c.want)
		}
	})
}

// Only the notifications that wait on the user flag the pane; the matcher
// in settings.json drops the rest.
func TestHookNotificationFilter(t *testing.T) {
	long := strings.Repeat("y", 80)
	cases := []struct {
		kind, message string
		wantState     string
		wantReason    string
	}{
		{"elicitation_dialog", "Pick a store", "needs-you", "Pick a store"},
		{"agent_needs_input", long, "needs-you", long[:64]},
		{"elicitation_dialog", "", "needs-you", "needs input"},
		{"permission_prompt", "Claude needs your permission", "working", ""},
		{"idle_prompt", "Claude is waiting for your input", "working", ""},
		{"auth_success", "ok", "working", ""},
	}
	each(t, func(t *testing.T, e *env) {
		for _, c := range cases {
			p := e.claudeWindow(t, "proj-a")
			e.d.Hook(t, e.Server, p, Event{Name: "UserPromptSubmit"})
			e.d.Hook(t, e.Server, p, Event{Name: "Notification", Payload: map[string]any{
				"notification_type": c.kind, "message": c.message,
			}})
			e.wantFlag(t, p, c.wantState, c.wantReason)
		}
	})
}

func TestHookStopFlagsDone(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		e.d.Hook(t, e.Server, p, Event{Name: "UserPromptSubmit"})
		e.d.Hook(t, e.Server, p, Event{Name: "Stop", Payload: map[string]any{"transcript_path": e.writeTranscript(t)}})
		e.wantFlag(t, p, "done", "turn ended")
		if r := e.d.Record(t, e.Server, p); r.State != "done" {
			t.Errorf("record state = %q, want done", r.State)
		}
	})
}

// stop is a Stop whose background_tasks lists what still runs, as Claude
// Code 2.1.322 sends it.
func stop(transcript string, tasks ...map[string]any) Event {
	bg := []any{}
	for _, task := range tasks {
		bg = append(bg, task)
	}
	return Event{Name: "Stop", Payload: map[string]any{"transcript_path": transcript, "background_tasks": bg}}
}

func subagentTask(status string) map[string]any {
	return map[string]any{"id": "a1b2c3d4e5f6a7b8c", "type": "subagent", "status": status,
		"description": "probe", "agent_type": "general-purpose"}
}

var shellTask = map[string]any{"id": "b4oc37ih8", "type": "shell", "status": "running",
	"description": "make serve", "command": "make serve"}

// fromSubagent marks an event as sent by a subagent of the session: Claude
// Code sends a subagent's tool hooks with its agent_id and agent_type.
func fromSubagent(ev Event) Event {
	ev.Payload["agent_id"] = "a1b2c3d4e5f6a7b8c"
	ev.Payload["agent_type"] = "general-purpose"
	return ev
}

// A turn that ends while a subagent still runs leaves the pane working: the
// subagent's report starts the next turn, and that turn's end is done. A
// background shell (a dev server) does not count. The window is named at
// either turn end.
func TestHookStopWithSubagentsKeepsWorking(t *testing.T) {
	cases := []struct {
		name       string
		tasks      []map[string]any
		wantState  string
		wantReason string
	}{
		{"subagent running", []map[string]any{subagentTask("running")}, "working", ""},
		{"subagent and shell", []map[string]any{shellTask, subagentTask("running")}, "working", ""},
		{"only a shell", []map[string]any{shellTask}, "done", "turn ended"},
		{"subagent finished", []map[string]any{subagentTask("completed")}, "done", "turn ended"},
		{"nothing", nil, "done", "turn ended"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(t *testing.T, e *env) {
				p := e.claudeWindow(t, "proj-a")
				tr := e.writeTranscript(t, userPrompt("fix the waybar clock"))
				e.d.Hook(t, e.Server, p, Event{Name: "UserPromptSubmit"})
				e.d.Hook(t, e.Server, p, stop(tr, c.tasks...))
				e.wantFlag(t, p, c.wantState, c.wantReason)
				if r := e.d.Record(t, e.Server, p); r.State != c.wantState {
					t.Errorf("record state = %q, want %s", r.State, c.wantState)
				}
				if name, _ := e.windowName(t, p); name != "fix the waybar" {
					t.Errorf("window = %q, want %q", name, "fix the waybar")
				}
			})
		})
	}
}

// A decision pending for one agent of the session (the main agent or a
// subagent, which run side by side) goes only with that agent's own
// events: another agent's tool calls, and the main agent's turn end while
// the subagent waits, leave it.
func TestHookSubagentDecisions(t *testing.T) {
	question := toolEvent("PreToolUse", "AskUserQuestion", map[string]any{
		"questions": []any{map[string]any{"question": "Which store?"}},
	})
	touch := map[string]any{"command": "touch probe-file"}
	t.Run("subagent tool beside a question", func(t *testing.T) {
		each(t, func(t *testing.T, e *env) {
			p := e.claudeWindow(t, "proj-a")
			e.d.Hook(t, e.Server, p, question)
			e.d.Hook(t, e.Server, p, fromSubagent(toolEvent("PreToolUse", "Bash", map[string]any{"command": "ls"})))
			e.d.Hook(t, e.Server, p, fromSubagent(toolEvent("PostToolUse", "Bash", map[string]any{"command": "ls"})))
			e.wantFlag(t, p, "needs-you", "question: Which store?")
		})
	})
	t.Run("main tool beside a subagent's permission", func(t *testing.T) {
		each(t, func(t *testing.T, e *env) {
			p := e.claudeWindow(t, "proj-a")
			e.d.Hook(t, e.Server, p, fromSubagent(toolEvent("PermissionRequest", "Bash", touch)))
			e.d.Hook(t, e.Server, p, toolEvent("PreToolUse", "Read", map[string]any{"file_path": "/src/proj-a/main.go"}))
			e.wantFlag(t, p, "needs-you", "Bash(touch probe-file)")
			// Granted: the subagent's tool ends.
			e.d.Hook(t, e.Server, p, fromSubagent(toolEvent("PostToolUse", "Bash", touch)))
			e.wantFlag(t, p, "working", "")
		})
	})
	t.Run("turn end while a subagent waits", func(t *testing.T) {
		each(t, func(t *testing.T, e *env) {
			p := e.claudeWindow(t, "proj-a")
			e.d.Hook(t, e.Server, p, Event{Name: "UserPromptSubmit"})
			e.d.Hook(t, e.Server, p, fromSubagent(toolEvent("PermissionRequest", "Bash", touch)))
			e.d.Hook(t, e.Server, p, stop(e.writeTranscript(t), subagentTask("running")))
			e.wantFlag(t, p, "needs-you", "Bash(touch probe-file)")
		})
	})
	t.Run("turn end after the waiting subagent stopped", func(t *testing.T) {
		// The subagent was stopped while it waited; another one still runs.
		each(t, func(t *testing.T, e *env) {
			p := e.claudeWindow(t, "proj-a")
			e.d.Hook(t, e.Server, p, Event{Name: "UserPromptSubmit"})
			e.d.Hook(t, e.Server, p, fromSubagent(toolEvent("PermissionRequest", "Bash", touch)))
			other := subagentTask("running")
			other["id"] = "f0e1d2c3b4a596870"
			e.d.Hook(t, e.Server, p, stop(e.writeTranscript(t), other))
			e.wantFlag(t, p, "working", "")
		})
	})
	t.Run("subagent's own next tool", func(t *testing.T) {
		// Declined with a note: the subagent goes on with another tool.
		each(t, func(t *testing.T, e *env) {
			p := e.claudeWindow(t, "proj-a")
			e.d.Hook(t, e.Server, p, fromSubagent(toolEvent("PermissionRequest", "Bash", touch)))
			e.d.Hook(t, e.Server, p, fromSubagent(toolEvent("PreToolUse", "Read", map[string]any{"file_path": "/src/proj-a/main.go"})))
			e.wantFlag(t, p, "working", "")
		})
	})
}

// The window takes its name from the first prompt on Stop: openers dropped,
// three words, cut at 15 characters, trailing punctuation trimmed. Slash
// commands, tagged harness messages and tool results are not prompts.
func TestHookStopTitlesTheWindow(t *testing.T) {
	toolResult := map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "content": "done"},
	}}}
	textParts := map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "update the"},
		map[string]any{"type": "text", "text": "readme now"},
	}}}
	cases := []struct {
		name  string
		lines []map[string]any
		want  string
	}{
		{"opener", []map[string]any{userPrompt("can you fix the waybar clock please")}, "fix the waybar"},
		{"cut at 15", []map[string]any{userPrompt("Hello, please refactor the parser module")}, "refactor the pa"},
		{"trailing punctuation", []map[string]any{userPrompt("fix: the thing! now")}, "fix: the thing"},
		{"several openers", []map[string]any{userPrompt("ok so let's add tests for jump")}, "add tests for"},
		{"skips non-prompts", []map[string]any{
			userPrompt("/clear"),
			userPrompt("<command-name>/model</command-name>"),
			toolResult,
			textParts,
		}, "update the read"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			each(t, func(t *testing.T, e *env) {
				p := e.claudeWindow(t, "proj-a")
				tr := e.writeTranscript(t, c.lines...)
				e.d.Hook(t, e.Server, p, Event{Name: "Stop", Payload: map[string]any{"transcript_path": tr}})
				name, auto := e.windowName(t, p)
				if name != c.want || auto {
					t.Errorf("window = %q (automatic %v), want %q, named", name, auto, c.want)
				}
				if got := e.opt(t, "-w", p, e.d.TitleOption()); got != c.want {
					t.Errorf("%s = %q, want %q", e.d.TitleOption(), got, c.want)
				}
				if r := e.d.Record(t, e.Server, p); r.Title != c.want {
					t.Errorf("record title = %q, want %q", r.Title, c.want)
				}
			})
		})
	}
}

// A window named once is never renamed, and a name the user set is never
// touched.
func TestHookStopKeepsNamedWindows(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		stop := func(prompt string) {
			tr := e.writeTranscript(t, userPrompt(prompt))
			e.d.Hook(t, e.Server, p, Event{Name: "Stop", Payload: map[string]any{"transcript_path": tr}})
		}
		stop("first task here")
		stop("second task here")
		if name, _ := e.windowName(t, p); name != "first task here" {
			t.Errorf("window = %q after a second Stop, want the first title", name)
		}

		q := e.claudeWindow(t, "proj-a")
		e.MustTmux(t, "rename-window", "-t", q, "mine")
		tr := e.writeTranscript(t, userPrompt("another task here"))
		e.d.Hook(t, e.Server, q, Event{Name: "Stop", Payload: map[string]any{"transcript_path": tr}})
		if name, _ := e.windowName(t, q); name != "mine" {
			t.Errorf("user-named window = %q, want mine", name)
		}
	})
}

// No prompt yet, no name.
func TestHookStopWithoutPromptKeepsTheName(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		before, _ := e.windowName(t, p)
		e.d.Hook(t, e.Server, p, Event{Name: "Stop", Payload: map[string]any{"transcript_path": e.writeTranscript(t)}})
		if name, auto := e.windowName(t, p); name != before || !auto {
			t.Errorf("window = %q (automatic %v), want %q, automatic", name, auto, before)
		}
	})
}

// On SessionEnd the flag goes, the name goes back to tmux if it is still the
// title, and the conversation is kept for a resume only when it was killed
// (reason "other"), not when the user ended it.
func TestHookEnd(t *testing.T) {
	cases := []struct {
		reason    string
		resumable bool
	}{
		{"prompt_input_exit", false},
		{"clear", false},
		{"logout", false},
		{"other", true},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			each(t, func(t *testing.T, e *env) {
				p := e.claudeWindow(t, "proj-a")
				tr := e.writeTranscript(t, userPrompt("write the docs"))
				e.d.Hook(t, e.Server, p, startEvent("startup", tr, e.Home))
				e.d.Hook(t, e.Server, p, Event{Name: "Stop", Payload: map[string]any{"transcript_path": tr}})
				e.d.Hook(t, e.Server, p, Event{Name: "SessionEnd", Payload: map[string]any{"reason": c.reason}})

				e.wantFlag(t, p, "", "")
				if _, auto := e.windowName(t, p); !auto {
					t.Error("the window still carries the title")
				}
				if got := e.opt(t, "-w", p, e.d.TitleOption()); got != "" {
					t.Errorf("%s = %q, want unset", e.d.TitleOption(), got)
				}
				r := e.d.Record(t, e.Server, p)
				if got := r.Exists && r.SessionID == sessionA; got != c.resumable {
					t.Errorf("resumable = %v (record %+v), want %v", got, r, c.resumable)
				}
				if c.resumable && r.State != "done" {
					t.Errorf("kept record state = %q, want done", r.State)
				}
			})
		})
	}
}

// A name the user set after the title is left alone on SessionEnd.
func TestHookEndKeepsUserRename(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		tr := e.writeTranscript(t, userPrompt("write the docs"))
		e.d.Hook(t, e.Server, p, startEvent("startup", tr, e.Home))
		e.d.Hook(t, e.Server, p, Event{Name: "Stop", Payload: map[string]any{"transcript_path": tr}})
		e.MustTmux(t, "rename-window", "-t", p, "mine")
		e.d.Hook(t, e.Server, p, Event{Name: "SessionEnd", Payload: map[string]any{"reason": "prompt_input_exit"}})
		if name, auto := e.windowName(t, p); name != "mine" || auto {
			t.Errorf("window = %q (automatic %v), want mine, named", name, auto)
		}
	})
}

// Hooks never write to stdout: Claude Code adds hook output to the model's
// context.
func TestHookPrintsNothing(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		tr := e.writeTranscript(t, userPrompt("write the docs"))
		events := []Event{
			startEvent("startup", tr, e.Home),
			{Name: "UserPromptSubmit", Payload: map[string]any{"prompt": "write the docs"}},
			toolEvent("PreToolUse", "Bash", map[string]any{"command": "ls"}),
			toolEvent("PreToolUse", "AskUserQuestion", map[string]any{}),
			toolEvent("PermissionRequest", "Bash", map[string]any{"command": "ls"}),
			toolEvent("PostToolUse", "Bash", map[string]any{"command": "ls"}),
			{Name: "Notification", Payload: map[string]any{"notification_type": "elicitation_dialog", "message": "m"}},
			{Name: "Stop", Payload: map[string]any{"transcript_path": tr}},
			{Name: "SessionEnd", Payload: map[string]any{"reason": "other"}},
		}
		for _, ev := range events {
			if out := e.d.Hook(t, e.Server, p, ev); out != "" {
				t.Errorf("%s printed %q", ev.Name, out)
			}
		}
	})
}

// Outside tmux a hook does nothing and succeeds.
func TestHookOutsideTmux(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		for _, ev := range []Event{
			startEvent("startup", e.writeTranscript(t), e.Home),
			{Name: "UserPromptSubmit"},
			{Name: "Stop"},
			{Name: "SessionEnd", Payload: map[string]any{"reason": "other"}},
		} {
			out, err := e.d.HookOutsideTmux(t, e.Server, ev)
			if err != nil || out != "" {
				t.Errorf("%s outside tmux: output %q, error %v; want neither", ev.Name, out, err)
			}
		}
	})
}

// A resumed conversation brings back what it was waiting on, as
// information: a pending decision as "before restart", a turn cut mid-work
// as "interrupted mid-turn", an unread done as done. Its title comes back
// too.
func TestHookResumeReflags(t *testing.T) {
	cases := []struct {
		state, reason         string
		wantState, wantReason string
	}{
		{"needs-you", "Bash(make)", "needs-you", "before restart: Bash(make)"},
		{"needs-you", "", "needs-you", "before restart: waiting on you"},
		{"working", "", "needs-you", "interrupted mid-turn"},
		{"done", "turn ended", "done", "turn ended"},
		{"idle", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.state+" "+c.reason, func(t *testing.T) {
			each(t, func(t *testing.T, e *env) {
				p := e.claudeWindow(t, "proj-a")
				tr := e.writeTranscript(t, userPrompt("write the docs"))
				e.d.SeedRecord(t, e.Server, p, Record{
					SessionID: sessionA, Transcript: tr, Cwd: e.Home,
					State: c.state, Reason: c.reason, Title: "write the docs",
				}, true)
				e.d.Hook(t, e.Server, p, startEvent("resume", tr, e.Home))
				e.wantFlag(t, p, c.wantState, c.wantReason)
				if name, _ := e.windowName(t, p); name != "write the docs" {
					t.Errorf("window = %q, want the recorded title", name)
				}
			})
		})
	}
}

// A different conversation in a pane marked for resume starts clean.
func TestHookResumeOfAnotherSessionStartsClean(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		tr := e.writeTranscript(t)
		e.d.SeedRecord(t, e.Server, p, Record{
			SessionID: "99999999-2222-4333-8444-555555555555", Transcript: tr, Cwd: e.Home,
			State: "needs-you", Reason: "Bash(make)",
		}, true)
		e.d.Hook(t, e.Server, p, startEvent("resume", tr, e.Home))
		e.wantFlag(t, p, "", "")
	})
}

// A window that carries a title but runs no agent any more gets its
// automatic name back: a pane killed with the agent inside fires no
// SessionEnd.
func TestAgentSweepReleasesTitlesOfDeadAgents(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		agent := e.claudeWindow(t, "proj-a")
		shell := e.split(t, agent, "")
		tr := e.writeTranscript(t, userPrompt("write the docs"))
		e.d.Hook(t, e.Server, agent, Event{Name: "Stop", Payload: map[string]any{"transcript_path": tr}})
		e.MustTmux(t, "kill-pane", "-t", agent)
		e.d.AgentSweep(t, e.Server)
		if _, auto := e.windowName(t, shell); !auto {
			t.Error("the window kept a dead agent's title")
		}
		if got := e.opt(t, "-w", shell, e.d.TitleOption()); got != "" {
			t.Errorf("%s = %q, want unset", e.d.TitleOption(), got)
		}
	})
}

// The other way round: an agent running under the automatic name gets its
// recorded title back, which is what a restore onto a live server leaves.
func TestAgentSweepPutsRecordedTitlesBack(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		e.d.SeedRecord(t, e.Server, p, Record{SessionID: sessionA, Title: "write the docs"}, false)
		e.d.AgentSweep(t, e.Server)
		if name, _ := e.windowName(t, p); name != "write the docs" {
			t.Errorf("window = %q, want the recorded title", name)
		}
		// A live titled agent is left alone by the next sweep.
		e.d.AgentSweep(t, e.Server)
		if name, _ := e.windowName(t, p); name != "write the docs" {
			t.Errorf("window = %q after a second sweep, want the title", name)
		}
	})
}

// SessionEnd hooks share a 1.5 s budget; the end must land well inside it.
func TestHookEndIsFast(t *testing.T) {
	each(t, func(t *testing.T, e *env) {
		p := e.claudeWindow(t, "proj-a")
		tr := e.writeTranscript(t, userPrompt("write the docs"))
		e.d.Hook(t, e.Server, p, startEvent("startup", tr, e.Home))
		e.d.Hook(t, e.Server, p, Event{Name: "Stop", Payload: map[string]any{"transcript_path": tr}})
		start := time.Now()
		e.d.Hook(t, e.Server, p, Event{Name: "SessionEnd", Payload: map[string]any{"reason": "other"}})
		if d := time.Since(start); d > time.Second {
			t.Errorf("SessionEnd took %v, want well under the 1.5s budget", d)
		}
	})
}
