# b-tmux-agent-the-context-share-reads-over-twice-too-261127-2217 — tmux-agent: the context share reads over twice too high below 200k tokens

Fixed on main at the user's word (2026-11-28): "open bug item then fix it
but dont follow workbench ceremony fix it on main". No branch, wb-tester
run or test book. Reviewed by agents that did not write the code, once
for each fix, at the user's word (2026-11-28): "review it if any bugs fix following
bugfix if not release it and deploy".

## Why

On 2026-11-27 the panel showed an agent at 119% of its context, and
others at 106%, 99% and 98%. The user: "huh? 119% of context? how is that
possible?" The pane at 119% held 199,535 tokens; its next call held
201,372 and the panel dropped to 55%. Nothing was over a limit: with
`autoCompactWindow` 400000 Claude Code compacts those sessions at 367,000
tokens.

## Root cause

`contextLimit` took a model's window as 200,000 tokens unless the model
name in the settings or `ANTHROPIC_MODEL` ended in `[1m]`, or the call
already held more than 200,000. The models these sessions run have the
long window under a name with no `[1m]`, so every session below 200,000
tokens was measured against 167,000 instead of 367,000: 2.2 times too
high.

A model's window is in the model catalog Claude Code caches, one file per
account: `cache/model-catalog/*-cc.json` in its folder,
`catalog.config.models[].runtime.max_input_tokens`. On the user's machine
it gives 1,000,000 for every current model and 200,000 for
`claude-haiku-4-5-20251001`. The fix reads the window of the transcript's
last call's model there, and takes 1M for a model no catalog lists. Of
the 36 auto-compactions in the user's transcripts (2026-11-04 to
2026-11-27), none was near 167,000: the lowest was 295,339, the median
367,618.

The catalog gives the most a model takes. Claude Code counts with less
for a model whose long window is an opt-in (`claude-opus-4-6` and
`claude-sonnet-4-6` without `[1m]`: 200,000 against the catalog's 1M) and
for an account it holds to less; that is in its code, not in a file. It
does give the window it counts with to its status line command
(`context_window.context_window_size`). The user, asked whether to release
without it (2026-11-28): "I didnt get why we cannot know exact context
window per agent", then "build it first". So `tmux-agent statusline
claude`, as the status line command, keeps that window on the pane
(`@agent_window`, with its session's id), and the limit takes it before
the catalog's.

## Check

1. With `autoCompactWindow` 400000 and a model the catalog gives a 1M
   window, the SessionStart hook of a session whose last call held 199,535
   tokens sets the pane's `@agent_context` to `54`
   (`TestContextShare/a_long_window`).
2. The same with a model the catalog gives a 200k window and a last call
   of 100,200 tokens sets it to `60` (`TestContextShare/a_short_window`).
   Added for the review's finding.
3. With a model the catalog gives a 1M window, a last call of 100,200
   tokens and `autoCompactWindow` 400000, `tmux-agent statusline claude`
   with input that gives the pane's session a 200,000 window sets
   `@agent_context` to `60`, also when it runs before the session's start
   hook; input of another session leaves it at `27`
   (`TestContextShare/the_status_line's_window`,
   `TestContextShare/the_status_line_before_the_session's_start`,
   `TestContextShare/the_status_line_of_another_session`). Added at the
   user's word, "build it first" (2026-11-28).

## Proof

Step 1 on the code before the fix, and step 2 on the first fix (1M for
every model), 2026-11-28:

```
$ go test ./tmux-agent/internal/contract/ -run 'TestContextShare'
--- FAIL: TestContextShare (0.00s)
    --- FAIL: TestContextShare/a_long_window (0.14s)
        context_test.go:48: @agent_context = "119", want "54"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/contract	0.144s
FAIL
```

```
$ go test ./tmux-agent/internal/contract/ -run 'TestContextShare'
--- FAIL: TestContextShare (0.00s)
    --- FAIL: TestContextShare/a_short_window (0.12s)
        context_test.go:48: @agent_context = "27", want "60"
```

(the pane's screen, which the failed test prints after that line, left
out.)

Review finding, fixed: the first fix took 1M for every model, on the
claim that no file says a model's window. The catalog does; a session on
a 200k model read 2.2 to 6 times too low. Its command is step 2: `27`
before the fix (above), and after it:

```
$ go test ./tmux-agent/internal/contract/ -run 'TestContextShare' -v
--- PASS: TestContextShare (0.00s)
    --- PASS: TestContextShare/a_short_window (0.09s)
    --- PASS: TestContextShare/a_long_window (0.09s)
PASS
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/contract	0.099s
```

Both steps on a throwaway tmux server with the fixed build, the user's
own catalog file and `{"model":"fable","autoCompactWindow":400000}` as
settings; each line is one pane whose SessionStart hook read a one-call
transcript:

```
$ bash run.sh <tmp> bin/tmux-agent ~/.claude/cache/model-catalog/<account>-cc.json
claude-opus-5-5, 199535 tokens: @agent_context=54
claude-fable-5-2, 360100 tokens: @agent_context=98
claude-haiku-4-5-20251001, 100200 tokens: @agent_context=60
model-not-listed, 199535 tokens: @agent_context=54
```

The same hook with the installed release, v0.3.39, before any fix, set
`@agent_context=119` for 199,535 tokens. `run.sh`:

```
#!/usr/bin/env bash
set -uo pipefail
D=$1; BIN=$2; CATALOG=$3
mkdir -p "$D/home/.claude/cache/model-catalog" "$D/state" "$D/run"; chmod 700 "$D/run"
cp "$CATALOG" "$D/home/.claude/cache/model-catalog/"
printf '%s\n' '{"model":"fable","autoCompactWindow":400000}' > "$D/home/.claude/settings.json"
L="ctxfix-$$"
t() { env -i PATH=/usr/bin:/bin HOME="$D/home" XDG_STATE_HOME="$D/state" XDG_RUNTIME_DIR="$D/run" TERM=xterm-256color tmux -L "$L" "$@"; }
t -f /dev/null new-session -d -s t -x 100 -y 30 /bin/sh
i=0
while read -r model tokens; do
  i=$((i+1)); sid="1111111$i-1111-4111-8111-111111111111"; T="$D/s$i.jsonl"
  printf '%s\n' "{\"type\":\"assistant\",\"isSidechain\":false,\"message\":{\"model\":\"$model\",\"usage\":{\"input_tokens\":$tokens,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0,\"output_tokens\":1}}}" > "$T"
  t new-window -d -n "w$i" /bin/sh
  printf '%s' "{\"hook_event_name\":\"SessionStart\",\"source\":\"startup\",\"session_id\":\"$sid\",\"transcript_path\":\"$T\",\"cwd\":\"$D/home\"}" > "$D/ev$i.json"
  t send-keys -t "t:w$i" "$BIN hook claude < $D/ev$i.json; tmux wait-for -S done$i" Enter
  timeout 30 env -i PATH=/usr/bin:/bin tmux -L "$L" wait-for "done$i" || echo "wait failed"
  echo "$model, $tokens tokens: @agent_context=$(t show-options -pqv -t "t:w$i" @agent_context)"
done <<'CASES'
claude-opus-5-5 199535
claude-fable-5-2 360100
claude-haiku-4-5-20251001 100200
model-not-listed 199535
CASES
t kill-server
```

Tests changed: `TestContextLimit` and `TestContextUsed` expected the old
rule (167,000 with nothing set or no `[1m]`, a model read from the
settings and `ANTHROPIC_MODEL`, a call past 200,000 as proof of the long
window). Their old results are the fault this item fixes (Root cause);
the cases about a model's name are gone with that code. For the review:
`TestContextUsed` no longer reads the machine's managed settings, and the
"unreadable file" case has a weaker file whose value must win.

The review also named faults that were there before the change; they are
one entry in `workbench/BACKLOG.md`.

A second review of the catalog fix found that the catalog is too long for
the opt-in models (Root cause) and three test cases that could not fail
for their reason ("the newest catalog", a catalog row with no window, two
compactions in the tail); each now fails when its line of the code is
taken out. Step 3 before the status line command existed:

```
$ go test ./tmux-agent/internal/contract/ -run 'TestContextShare'
--- FAIL: TestContextShare (0.00s)
    --- FAIL: TestContextShare/the_status_line's_window (0.16s)
        context_test.go:62: @agent_context = "27", want "60"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/contract	0.165s
FAIL
```

Step 3 with a real Claude Code (2.1.332) on a throwaway tmux server, its
`statusLine` command a script that ran the build's `tmux-agent statusline
claude` on its input; no prompt was sent. Claude Code ran the command
once at the start, with `TMUX_PANE` and `CLAUDE_PROJECT_DIR` in its
environment and
`"context_window": {"total_input_tokens": 0, "total_output_tokens": 0, "context_window_size": 1000000, "current_usage": null, "used_percentage": null, "remaining_percentage": null}`
for `claude-fable-5-2`, and the pane then held:

```
@agent claude
@agent_session <session id>
@agent_window 1000000
```

A status line command that prints nothing costs one empty row under
Claude Code's input box. The user, shown where (2026-11-28): "we can have
it at empty line as well if claude supports that I dont mind. but my main
request is to have it in sidebar properly".

A third review, of the status line code, found that a status line run
before the session-start hook was dropped (Claude Code does not wait for
that hook), and that a session started with no end of the one before took
its window. The window is now kept with its session's id. Before that
fix:

```
$ go test ./tmux-agent/internal/agent/ -run 'TestStatusWindow'
--- FAIL: TestStatusWindow (0.12s)
    gauge_test.go:148: told before the session's start: the pane holds a window: false, want true
    gauge_test.go:145: the session's start: the share was read with window 0, want 200000
    gauge_test.go:148: the session's start: the pane holds a window: false, want true
    gauge_test.go:145: told again, the same: the share was read with window 200000, want -1
    gauge_test.go:145: another session starts with none told: the share was read with window 300000, want 0
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/agent	0.123s
FAIL
$ go test ./tmux-agent/internal/contract/ -run 'TestContextShare'
--- FAIL: TestContextShare (0.00s)
    --- FAIL: TestContextShare/the_status_line_before_the_session's_start (0.15s)
        context_test.go:67: @agent_context = "27", want "60"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/contract	0.158s
FAIL
```

After it:

```
$ go test ./tmux-agent/internal/contract/ -run 'TestContextShare' -v
--- PASS: TestContextShare (0.00s)
    --- PASS: TestContextShare/a_long_window (0.18s)
    --- PASS: TestContextShare/a_short_window (0.19s)
    --- PASS: TestContextShare/the_status_line_before_the_session's_start (0.21s)
    --- PASS: TestContextShare/the_status_line's_window (0.22s)
    --- PASS: TestContextShare/the_status_line_of_another_session (0.22s)
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/contract	0.224s
```

That review's two faults from before the change are entries in
`workbench/BACKLOG.md`.

A fourth review, of the fix that keeps the window with its session's id,
found no fault that changes a number in the panel. Fixed from it: a
status line run for a pane that is gone failed at the write. The
project's stop hook ran the new test step before that fix:

```
    gauge_test.go:164: a status for a pane that is gone: tmux set-option -p -t %0 @agent_window s-3 100000: no such pane: %0
--- FAIL: TestStatusWindow (0.18s)
```

and after it:

```
$ go test ./tmux-agent/internal/agent/ -run 'TestStatusWindow' -v
--- PASS: TestStatusWindow (0.17s)
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/agent	0.174s
```

Three orders in which the told window is wrong until the agent's next
status line run are one entry in `workbench/BACKLOG.md`. The user, asked
whether to release with them (2026-11-28): "Release and deploy".

Before the release: `bash workbench/checks` passed, and

```
$ go test -race -p 1 ./tmux-agent/internal/agent/... ./tmux-agent/internal/cli/ ./tmux-agent/internal/contract/
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/agent	13.073s
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/agent/claude	(cached)
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/cli	57.071s
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/contract	63.258s
```
