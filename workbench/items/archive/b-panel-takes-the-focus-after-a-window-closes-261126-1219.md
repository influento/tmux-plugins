# b-panel-takes-the-focus-after-a-window-closes-261126-1219 — Panel takes the focus after a window closes

## Why

With the panel open, the user opened a window by hand (prefix c, window
11 of proj-a) and closed it again about 2 s later. The client went back
to window 10, and right after that it went to `proj-b:1` and then
`proj-a:1` with no key pressed for the panel (log, 2026-11-26
13:58:44, `go to the row in .want … [fzf after change-input-label]`).
The user reported it as "it didn't appear in my tmux windows" (the window
was gone, and the client was somewhere else).

Without the panel, tmux puts the client on the last window and the focus
on the pane that window had. With the panel, the focus lands in the panel.
So what the user types next goes into the panel's search, and the panel
takes the client to the first row that matches.

## Root cause

When a window's last main pane closes, `sidebar keep` finds the panel
alone and moves it, with its client, to another window (`Sidebar.alone`).
A panel alone in its window is always the window's active pane, so
`alone` passed `active` true to `moveTo`. `moveTo` then joined the panel
without `-d` and laid the window out with the panel focused. Because
`active` was true for every alone panel, the panel took the focus also
when the user had the focus in the shell that closed.

## Check

1. On a server with tmux-init's hooks, a client on proj-a and the panel
   open: put the focus on window A's main pane, open window B (the panel
   follows), type `exit` and Enter in B's shell. → The client and the
   panel are back on A, A's active pane is its main pane (not the panel),
   and text typed then shows in that main pane.
   Covered by `TestPanelLeftAloneLeavesFocus`
   (`tmux-agent/internal/cli/sidebar_test.go`).

Decision for a case no step covers: when the panel itself had the focus
and the window's last other pane closed (the shell exited on its own),
the focus also goes to the active pane of the window the panel goes to,
as tmux does without a panel. `alone` cannot tell the two cases apart.

## Proof

The user asked on 2026-11-26 to create this item and fix it on main,
without the rest of the workbench steps: "if you reproduced it indeed
then create bug item and fix but dont follow workbench protocol in the
rest so fix it in main".

Repro before the fix (a scratch test, removed after): panel open, a new
window, its shell focused and killed, then `proj-b` typed into the
focused pane:

```
    zz_repro_test.go:17: active in new window before: @3 %3 (shell %4)
    zz_repro_test.go:20: active in home before kill: %0
    zz_repro_test.go:30: home=@0 client went: [proj-a:0/@0]
    zz_repro_test.go:31: active pane in home: %3 (panel? /dev/pts/21)
    zz_repro_test.go:34: after typing: client on proj-b:0/@1
```

The guard before the fix:

```
$ go test -race -run TestPanelLeftAloneLeavesFocus ./tmux-agent/internal/cli/
--- FAIL: TestPanelLeftAloneLeavesFocus (0.39s)
    sidebar_test.go:1217: focus on %2, want %0, the pane it had
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	0.397s
```

The guard after the fix:

```
$ go test -race -run TestPanelLeftAloneLeavesFocus ./tmux-agent/internal/cli/
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/cli	1.397s
```

The whole suite after the fix:

```
$ make test
go vet ./...
ok  	github.com/influento/tmux-plugins/internal/applog	(cached)
ok  	github.com/influento/tmux-plugins/internal/layout	(cached)
ok  	github.com/influento/tmux-plugins/internal/lockfile	(cached)
ok  	github.com/influento/tmux-plugins/internal/shell	(cached)
ok  	github.com/influento/tmux-plugins/internal/tmux	3.388s
ok  	github.com/influento/tmux-plugins/internal/tmux/tmuxtest	(cached)
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/agent	9.603s
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/agent/claude	(cached)
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/attention	3.217s
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/cli	52.290s
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/conformance	29.013s
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/contract	54.899s
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/git	(cached)
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/sidebar	5.436s
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/store	(cached)
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/tally	(cached)
ok  	github.com/influento/tmux-plugins/tmux-agent/internal/ui	1.020s
ok  	github.com/influento/tmux-plugins/tmux-revive/internal/cli	(cached)
ok  	github.com/influento/tmux-plugins/tmux-revive/internal/conformance	4.667s
ok  	github.com/influento/tmux-plugins/tmux-revive/internal/logind	(cached)
ok  	github.com/influento/tmux-plugins/tmux-revive/internal/revive	(cached)
ok  	github.com/influento/tmux-plugins/tmux-revive/internal/snapshot	(cached)
ok  	github.com/influento/tmux-plugins/tmux-warp	3.769s
 14:20:56 up 9 days, 19:51,  1 user,  load average: 5.91, 3.55, 3.21

[exited with code 0]
```
