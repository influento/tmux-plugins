# b-a-tmux-agent-or-tmux-revive-hook-that-fails-or-pri-261123-1940 — A tmux-agent or tmux-revive hook that fails or prints can crash tmux

Fixed on main at the user's word (2026-11-23): "bug item but dont follow
full workbench protcol fix it here". No branch, review, wb-tester run or
test book.

## Why

On 2026-11-23 the tmux server segfaulted during a fork storm that another
agent's command started, and the user lost every terminal. The coredump's
backtrace (symbols from Arch's debuginfod) ends in tmux 3.8's
cmd_run_shell_print, cmd-run-shell.c:94, printing "'tmux-agent sidebar
follow' returned 1" for a hook's `run-shell -b` job.

## Root cause

tmux prints a `run-shell -b` job's output, and its exit status when not 0,
into the current pane of the job's client. Line 93 checks that the client
has a session; line 94 then reads `session->curw->window`, and the
client's session (one of tmux-overlay's, `ov-shell-…`, closing) had no
window: curw was NULL. Under the load, `sidebar follow` failed (its tmux
calls timed out) and exited 1, which sent tmux down that path. Every hook
job tmux-agent and tmux-revive set can fail or print the same way.

## Check

1. A hook's job that fails (here with tmux-agent's or tmux-revive's lock
   folder replaced by a file) puts no pane into a mode to show its output
   or failure, and its error is in the plugin's log
   (`TestTmuxHookFailureShowsNothing` in tmux-agent/internal/cli and in
   tmux-revive/internal/revive).

## Proof

Before the fix, 2026-11-23:

```
$ go test -run TestTmuxHookFailureShowsNothing ./tmux-agent/internal/cli/
--- FAIL: TestTmuxHookFailureShowsNothing (0.08s)
    log_test.go:94: pane %1 shows the hook's output: "'tmux-agent sidebar follow' returned 1\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	0.082s
FAIL
```

```
$ go test -run TestTmuxHookFailureShowsNothing ./tmux-revive/internal/revive/
--- FAIL: TestTmuxHookFailureShowsNothing (0.20s)
    revive_test.go:548: pane %1 shows the hook's output: "'tmux-revive save' returned 1\n'tmux-revive save' returned 1\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-revive/internal/revive	0.200s
FAIL
```

The fix: each hook job runs as `<plugin> --tmux-hook <command>`
(internal/hookjob): it prints nothing, exits 0, and logs what it would
have printed. After it, both tests pass, and `make test` passes in full.

A test that changed: `TestTmuxInitOwnsIndex20` checked that tmux-revive's
hook at [20] contains `tmux-revive save`; the hook is now `tmux-revive
--tmux-hook save`, which this item's fix makes it, so the check now looks
for that text.

Not changed (dotfiles' tmux.conf, outside this repository): its own
`run-shell -b` hooks and the `prefix f` binding (`run-shell -b
"tmux-agent popup …"`) can reach the same tmux bug.

