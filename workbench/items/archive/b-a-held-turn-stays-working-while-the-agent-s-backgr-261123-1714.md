# b-a-held-turn-stays-working-while-the-agent-s-backgr-261123-1714 — A held turn stays working while the agent's background shell runs

Worked outside the workbench protocol, on main, at the user's word
(2026-11-23): "lets open bug item but not follow workbench protocol and
fix it right here". No branch, review, wb-tester run or test book.

## Why

The user (2026-11-23): "Why it's marked as agent working while in fact it
doesn't? Is it because of running shell?" The ui window's agent (Claude
Code 2.1.330) had ended its turn at the prompt, with a vite dev server
running in a background shell, and the panel kept it working.

## Root cause

The turn ended while the agent's other Stop hooks ran, so the hook held it
working (`@agent_held hook`). The watcher ends such a turn when Claude
Code's process file (`sessions/<pid>.json`) says `idle`. Claude Code
2.1.330 writes `shell` there instead of `idle` while a background shell
it started runs (its code: `io==="idle"&&go?"shell":io`, and it counts
`idle||shell` as idle). `Claude.IdleSince` took only `idle`, so the
turn stayed working until the next prompt. The same reading ends a turn
cut short by Esc before the first token, so that one stayed working too.

## Check

1. `go test -run TestIdleSince ./tmux-agent/internal/agent/claude/`:
   the case "idle with a background shell" passes (IdleSince is ok, at the
   file's statusUpdatedAt).

## Proof

The live pane, 2026-11-23: `@agent_held hook`, `@attention working`, and
`~/.claude/sessions/4177022.json` with `"status":"shell"`,
`statusUpdatedAt` 1795452241740, the second of the transcript's
`stop_hook_summary` (16:44:01.716Z).

The test, before the fix:

```
$ go test -run TestIdleSince ./tmux-agent/internal/agent/claude/
--- FAIL: TestIdleSince (0.00s)
    --- FAIL: TestIdleSince/idle_with_a_background_shell (0.00s)
        status_test.go:41: IdleSince = 0001-01-01 00:00:00 +0000 UTC, false; want ok true at 2026-11-15 01:19:26.044 +0200 EET
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/agent/claude	0.003s
FAIL
```

After the fix, `make test` passed in full.

End to end, 2026-11-23, run by an agent that did not write the fix
(`<REDACTED>` is the session's scratch folder): the scripts `run.sh`,
`stubs/claude` and `stubs/notify-send` of
`b-the-launcher-leaves-its-tmux-agent-children-as-zom-261123-1714`, with
one change in `stubs/claude`: the status that follows `hook Stop` is
`shell`, not `idle`:

```
$ f=workbench/items/archive/b-the-launcher-leaves-its-tmux-agent-children-as-zom-261123-1714.md
$ diff <(sed -n '/^`stubs\/claude`:/,/^`stubs\/notify/p' $f) "$D/stubs/claude" | head
1,3d0
< `stubs/claude`:
< 
< ```bash
28c25
<   status idle
---
>   status shell
33,35d29
< ```
```

Before the fix, `$OLD/bin/tmux-agent` a link to the installed v0.3.36:

```
$ $D/run.sh $D $OLD $(mktemp -d)
@agent claude
@agent_cwd <REDACTED>/held.MvB4/home/proj
@agent_prompt_got "1795466432552 8f434346648f6b96"
@agent_run 1885454
@agent_session 11111111-1111-1111-1111-111111111111
@agent_started 1795466426
@agent_state working
@agent_transcript <REDACTED>/held.MvB4/home/proj/t.jsonl
@attention working
@attention_at 1795466426
@attention_at_ms 1795466426820
@attention_mark "#[fg=blue]● "
@attention_owner claude
@attention_reason ''
launcher: 1885454 claude run claude
flag: working
children:
1885471 S+   bash <REDACTED>/held.MvB4/stubs/claude
zombies: 0
$ cat $D/home/trace
after Stop: held=hook flag=working
after Stop: held=hook flag=working
after Stop: held=hook flag=working
```

After the fix, a `make build` of main at a37018c (`v0.3.36-8-ga37018c`):

```
$ $D/run.sh $D /home/<REDACTED>/tmux-plugins $(mktemp -d)
@agent claude
@agent_cwd <REDACTED>/held.MvB4/home/proj
@agent_prompt_got "1795466450052 8f434346648f6b96"
@agent_run 1888494
@agent_session 11111111-1111-1111-1111-111111111111
@agent_started 1795466444
@agent_state working
@agent_transcript <REDACTED>/held.MvB4/home/proj/t.jsonl
@attention working
@attention_at 1795466450
@attention_at_ms 1795466450058
@attention_mark "#[fg=blue]● "
@attention_owner claude
@attention_reason ''
launcher: 1888494 claude run claude
flag: done
children:
1888512 S+   bash <REDACTED>/held.MvB4/stubs/claude
zombies: 0
$ cat $D/home/trace
after Stop: held=hook flag=working
notify-send: notifier 1888719, its parent 1888494: claude run claude
notify-send: notifier 1888719, its parent 1888494: claude run claude
after Stop: held=hook flag=working
notify-send: notifier 1889165, its parent 1888494: claude run claude
notify-send: notifier 1889165, its parent 1888494: claude run claude
after Stop: held=hook flag=working
notify-send: notifier 1889759, its parent 1888494: claude run claude
notify-send: notifier 1889759, its parent 1888494: claude run claude
```

The options list is read 6 s after the start, during the second turn;
`flag:` is read at 12 s, after the third turn's `shell`.
