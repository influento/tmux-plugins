# b-a-hook-job-that-dies-outside-go-s-error-handling-c-261123-2028 — A hook job that dies outside Go's error handling can still crash tmux

Fixed on main: the user asked for the agent's call (2026-11-23): "I need
you to be able to fix errors... if something goes wrong, I ask you and
you fix it anyways." No branch, review, wb-tester run or test book.

No test: the runtime's fatal error reaches the log — no test can make
the runtime fail without code that only tests use; shown once by hand
(Proof).

## Why

After a6085c2 a hook's job prints nothing and exits 0 when the Go code
handles its error. A job that dies outside it still ends non-zero, and
tmux 3.8 then reaches the print that crashed it on 2026-11-23: the
runtime's fatal errors (likely under a fork storm: no new OS thread), a
shell that cannot run the program (127). Its cause was lost too: tmux
shows a job's exit status, never its stderr.

## Root cause

The hook's command line ended in the plugin's command, so the job's exit
status was the program's, and nothing caught the runtime's output.

## Check

1. A hook's job whose program dies (a stub that writes to stderr and
   exits 3) puts no pane into a mode (`TestTmuxHookDeathShowsNothing`
   in tmux-agent/internal/cli and tmux-revive/internal/revive).

## Proof

Before the fix, 2026-11-23:

```
$ go test -run TestTmuxHookDeathShowsNothing ./tmux-agent/internal/cli/
--- FAIL: TestTmuxHookDeathShowsNothing (0.08s)
    log_test.go:133: pane %1 shows the hook's end: "'tmux-agent --tmux-hook sidebar follow' returned 3\n'tmux-agent --tmux-hook sidebar reload' returned 3\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	0.083s
FAIL
```

tmux-revive, with its guard taken out of the hook command lines:

```
$ go test -run TestTmuxHookDeathShowsNothing ./tmux-revive/internal/revive/
--- FAIL: TestTmuxHookDeathShowsNothing (0.04s)
    revive_test.go:581: pane %0 shows the hook's end: "'tmux-revive --tmux-hook save' returned 3\n'tmux-revive --tmux-hook save' returned 3\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-revive/internal/revive	0.038s
FAIL
```

The fix: each event hook's command line ends in `|| true`
(hookjob.Guard), and hookjob.Run points the runtime's crash output
(debug.SetCrashOutput) at the plugin's log. scrollback-loop and
shutdown-guard, started once by tmux-init, keep no guard: `|| true`
keeps sh alive as their parent, and tmux's SIGTERM at exit would reach
sh, not them. After the fix both tests pass; `make test` and the gate
pass.

By hand: a throwaway program under hookjob.Run whose goroutine panics
exited non-zero and its log held `panic: goroutine died` and the stack.

