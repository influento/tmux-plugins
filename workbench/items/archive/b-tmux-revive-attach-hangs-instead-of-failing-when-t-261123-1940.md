# b-tmux-revive-attach-hangs-instead-of-failing-when-t-261123-1940 — tmux-revive attach hangs instead of failing when the tmux server does not answer

Fixed on main at the user's word (2026-11-23): "bug item but dont follow
workbench protocol fix it here". No branch, review, wb-tester run or test
book.

## Why

On 2026-11-23, during a fork storm, the tmux server was alive but starved:
`tmux-revive attach` printed "tmux list-sessions …: signal: killed:
context deadline exceeded", and the user got no terminal. dotfiles'
tmux-attach will start a plain shell when attach exits non-zero; attach
must then exit instead of hanging.

## Root cause

After any error, attach printed it and still exec'd `tmux new-session -A
-s main`. That plain tmux client has no deadline: on a server that takes
connections but does not answer, it hangs, so the terminal's script never
sees an exit status.

## Check

1. With the tmux server stopped (SIGSTOP: it takes connections and does
   not answer), `tmux-revive attach` in a terminal exits non-zero within
   30 s and prints `tmux-revive attach: tmux does not answer: <reason>`
   (`TestAttachFailsWhenServerDoesNotAnswer`).

## Proof

Before the fix, 2026-11-23:

```
$ go test -run TestAttachFailsWhenServerDoesNotAnswer ./tmux-revive/internal/revive/
--- FAIL: TestAttachFailsWhenServerDoesNotAnswer (30.04s)
    revive_test.go:577: timed out after 30s waiting for tmux-revive attach to exit
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-revive/internal/revive	30.039s
FAIL
```

After the fix it passes (6 s: one tmux call's 5 s timeout), and `make
test` passes in full.

A case no step covers, decided as the project handles errors today: an
error that is not a timeout (a restore that partly failed, a refused
command) still attaches, as before; so does a timeout once attach has a
session to go to (a restore whose typing ran out of time).

