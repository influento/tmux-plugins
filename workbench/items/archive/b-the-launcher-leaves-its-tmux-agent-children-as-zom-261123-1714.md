# b-the-launcher-leaves-its-tmux-agent-children-as-zom-261123-1714 — The launcher leaves its tmux-agent children as zombies

Fixed on main at the user's word (2026-11-23): "You can fix it without
following our bench protocol. So just do it here in main." No branch,
review, wb-tester run or test book.

## Why

Seen on 2026-11-23 while the user asked about the ui window: its
launcher (`tmux-agent run claude`, pid 4177006, running since 12:28) had
five `tmux-agent` children in state `Zs <defunct>`, started at 12:29,
12:39, 12:40, 12:57 and 13:14. The user chose to open a bug item for it.

## Root cause

Shown on 2026-11-23 (see Proof): the launcher runs the watcher in
its own process, and the watcher's flag changes start the notifier through
`spawnNotify` (`tmux-agent/internal/cli/cli.go`), which calls
`Process.Release` and never waits; the launcher waits only on its
agent's pid (`launch.go`). A short-lived hook exits and init reaps
the child; the launcher lives on, so each notifier it starts stays a
zombie until the agent ends. `spawnFill`, `spawnScan`, `spawnTokenScan`
and `spawnWatch` start their children the same way.

## Check

1. On a throwaway tmux server, a stub `claude` under `tmux-agent run
   claude` has three turns. Each Stop hook runs beside a second "hook",
   so the turn is held (`@agent_held hook`), and the stub's process file
   then goes idle, so the launcher's watcher ends it as done and starts a
   notifier. Twelve seconds after the start:
   `ps -o stat= --ppid <launcher pid> | grep -c '^Z'` prints `0`.

## Proof

The user agreed the check on 2026-11-23 ("Agree").

Scripts of the check (outside the repository; `<REDACTED>` is the
session's scratch folder):

`run.sh`:

```bash
#!/usr/bin/env bash
# The check: a stub agent under the launcher has three turns that the
# watcher ends (its process file goes idle); count the launcher's zombies.
set -euo pipefail
d="$1"; repo="$2"; sock="zomb-$$"
: > "$d/tmux.conf"
env -i HOME="$d/home" XDG_STATE_HOME="$d/state" XDG_RUNTIME_DIR="$3" \
  PATH="$d/stubs:$repo/bin:/usr/bin" TERM=xterm-256color TMUX_AGENT_DEBUG=1 \
  tmux -L "$sock" -f "$d/tmux.conf" new-session -d -s s 'tmux-agent run claude'
sleep 6; tmux -L "$sock" show -p -t s | grep -E "^@(attention|agent)"; sleep 6
pane_pid=$(tmux -L "$sock" display -p -t s '#{pane_pid}')
launcher=$pane_pid
echo "launcher: $(ps -o pid=,args= -p "$launcher")"
echo "flag: $(tmux -L "$sock" show -pv -t s @attention 2>/dev/null || echo none)"
echo "children:"; ps -o pid=,stat=,args= --ppid "$launcher"
echo -n "zombies: "; ps -o stat= --ppid "$launcher" | grep -c '^Z' || true
tmux -L "$sock" kill-server
```

`stubs/claude`:

```bash
#!/usr/bin/env bash
set -euo pipefail
s=11111111-1111-1111-1111-111111111111
t="$HOME/proj/t.jsonl"
mkdir -p "$HOME/proj" "$HOME/.claude/sessions"
: > "$t"
hook() {
  printf '{"hook_event_name":"%s","source":"startup","prompt":"hi","session_id":"%s","transcript_path":"%s","cwd":"%s"}' \
    "$1" "$s" "$t" "$HOME/proj" | bash -c 'tmux-agent hook claude; :'
}
status() {
  printf '{"pid":%d,"sessionId":"%s","status":"%s","statusUpdatedAt":%s}' \
    "$$" "$s" "$1" "$(date +%s%3N)" > "$HOME/.claude/sessions/$$.json"
}
status idle
hook SessionStart
for _ in 1 2 3; do
  status busy
  hook UserPromptSubmit
  sleep 0.5
  bash -c 'sleep 2; :' &
  hook Stop
  echo "after Stop: held=$(tmux show -pv @agent_held 2>/dev/null) flag=$(tmux show -pv @attention)" >> "$HOME/trace"
  sleep 0.3
  status idle
  sleep 2
done
echo turns-done
sleep 600
```

`stubs/notify-send`:

```bash
#!/usr/bin/env bash
n=$(ps -o ppid= -p "$PPID" | tr -d ' ')
echo "notify-send: notifier $PPID, its parent $n: $(ps -o args= -p "$n")" >> "$HOME/trace"
```

Step 1 before the change, 2026-11-23:

```
$ $D/run.sh $D /home/<REDACTED>/tmux-plugins $(mktemp -d)
launcher: 1948997 claude run claude
flag: done
children:
1949015 S+   bash <REDACTED>/z6.E58v/stubs/claude
1949125 Zs   [tmux-agent] <defunct>
1949279 Zs   [tmux-agent] <defunct>
1949512 Zs   [tmux-agent] <defunct>
zombies: 3
$ cat $D/home/trace
after Stop: held=hook flag=working
notify-send: notifier 1949125, its parent 1948997: claude run claude
notify-send: notifier 1949125, its parent 1948997: claude run claude
after Stop: held=hook flag=working
notify-send: notifier 1949279, its parent 1948997: claude run claude
notify-send: notifier 1949279, its parent 1948997: claude run claude
after Stop: held=hook flag=working
notify-send: notifier 1949512, its parent 1948997: claude run claude
notify-send: notifier 1949512, its parent 1948997: claude run claude
```


The test `TestSpawnedReaped` (tmux-agent/internal/cli/cli_test.go), before
the fix:

```
$ go test -run TestSpawnedReaped ./tmux-agent/internal/cli/
--- FAIL: TestSpawnedReaped (25.10s)
    --- FAIL: TestSpawnedReaped/notify (5.00s)
        cli_test.go:264: children left after 5s, not reaped: [2152106 Z]
    --- FAIL: TestSpawnedReaped/subagents_fill (5.03s)
        cli_test.go:264: children left after 5s, not reaped: [2152106 Z 2152372 Z]
    --- FAIL: TestSpawnedReaped/subagents_scan (5.03s)
        cli_test.go:264: children left after 5s, not reaped: [2152106 Z 2152372 Z 2153034 Z]
    --- FAIL: TestSpawnedReaped/tokens_scan (5.03s)
        cli_test.go:264: children left after 5s, not reaped: [2152106 Z 2152372 Z 2153034 Z 2153316 Z]
    --- FAIL: TestSpawnedReaped/watch (5.01s)
        cli_test.go:264: children left after 5s, not reaped: [2152106 Z 2152372 Z 2153034 Z 2153316 Z 2153685 Z]
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	25.100s
FAIL
```

After the fix it passes, and `make test` passes in full.

Step 1 after the fix (a `make build` of the fixed code), 2026-11-23, run by
the agent that wrote the fix:

```
$ $D/run.sh $D /home/<REDACTED>/tmux-plugins $(mktemp -d)
launcher: 2156137 claude run claude
flag: done
children:
2156154 S+   bash <REDACTED>/chk.tJLI/stubs/claude
zombies: 0
$ cat $D/home/trace
after Stop: held=hook flag=working
notify-send: notifier 2156277, its parent 2156137: claude run claude
notify-send: notifier 2156277, its parent 2156137: claude run claude
after Stop: held=hook flag=working
notify-send: notifier 2156579, its parent 2156137: claude run claude
notify-send: notifier 2156579, its parent 2156137: claude run claude
after Stop: held=hook flag=working
notify-send: notifier 2157232, its parent 2156137: claude run claude
notify-send: notifier 2157232, its parent 2156137: claude run claude
```
