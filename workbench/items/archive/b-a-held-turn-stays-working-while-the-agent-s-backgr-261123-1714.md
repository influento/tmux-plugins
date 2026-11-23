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
