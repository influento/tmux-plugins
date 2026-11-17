# f-panel-the-cursor-s-row-shows-its-whole-name-and-br-261116-1550 — panel: the cursor's row shows its whole name and branch

## Why

The panel is 40 columns wide, so a long window or task name and its
branch are cut with `…`. Items that workbench starts have long names
(`a-popup-closed-by-its-hotkey-lea…`). The user wants the whole name
where they look, without a
new key, a popup or a footer, and without a change to the other rows.

The row under the panel's cursor shows its whole name, and its whole
labels line, over as many lines as they take. The rows below it move
down. When the cursor leaves the row, the row is cut again. A demo of
this on a throwaway server was approved on 2026-11-16.

## Check

1. In the panel, put the cursor on a row whose name is cut. Expected: the
   row shows the whole name over two or more lines, each line broken
   after a `-` or a space where the name has one; the role, share and age
   stay on the line below the name; the rows below move down.
2. On the same row, the labels line (the branch) is cut too. Expected:
   it shows whole, over two or more lines, the lines after the first
   indented two more columns.
3. Move the cursor to the next row. Expected: the row it left shows one
   cut line again, as before the change.
4. Put the cursor on a row whose name and labels fit. Expected: the row
   looks as before the change.
5. Keep the cursor on an open row while the panel reloads: the panel
   joins the window, an age ticks, a flag changes. Expected: the row stays
   open the whole time; it never shows cut and then whole again.
6. Hold the down arrow over rows with long names, then let go. Expected:
   the cursor and the client stop together on one row, which is open; no
   moves run on after the key is let go.
7. Click a row with a long name, and scroll onto one with the wheel.
   Expected: the row opens, as with the arrows.
8. In a window split into two panes that each run an agent, put the
   cursor on one pane's row. Expected: only that row opens.
9. Type a search that keeps a row with a long name, and put the cursor on
   it. Expected: the row opens.
10. `bash go-gate/scripts/gate.sh` and `make vet`. Expected: both exit 0.

## Proof

<for each step: what the user said at the demo, and the date>

### Before the demo

#### Decisions (cases the check has no step for)

- A row without an agent (a dim window row) opens too: its whole name,
  its command on the line below at the row's right edge. Test:
  `TestOpenRowWithoutAgent`.
- A name is broken after the last `-` or space that fits on the line; a
  line with neither is cut at its width. A space at a break goes. Labels
  break the same way.
- A count after the name (`⑂2`) stays after the name's last line.
- A search that matches no row, cleared, brings the rows back with the
  client's row open. fzf 0.75 runs no command that names a field while
  no row is under the cursor, so only a move's reload names the row
  (`--open={10}`); every other reload keeps the row the last rows had
  open. Test: `TestPanelSearchWithoutMatch`.
- The picker outside the panel uses the same rows: its cursor's row
  opens too.
- `.want` and `.gone` hold the row's key (a pane's, window's or session's
  id) instead of its target, and `.gone` also notes a row a go gave up
  on an error. Place waits for a row while it is not in `.gone`. Without
  these, a held key's move was lost now that each move reloads the rows.
  This log is from `TestPanelOpensRowOnMouseAndHeldKey` before the fix:
  place found no go while the go was between removing `.going` and
  taking it again, and a key's `.want` target (`=proj-a:2.1`) no longer
  matched the rows after the panel left that window:
  ```
  2026-11-16T18:09:24.772+02:00 1163364 place: client on =proj-a:1, cursor row 3 → 2 =proj-a:1: the client's window, noted in .want (.want "=proj-a:2.0", .gone "=proj-a:1", a go running false) [fzf after down]
  2026-11-16T18:09:24.775+02:00 1162959 go to the row in .want =proj-a:2.0: client /dev/pts/23 @1 → @2, panel %1 @1 → @2 [fzf after rebind]
  2026-11-16T18:10:50.702+02:00 1168345 place: client on =proj-a:3, cursor row 3 → 4 =proj-a:3: the client's window, noted in .want (.want "=proj-a:2.1", .gone "=proj-a:3", a go running false) [fzf after up]
  ```
  No test shows the `.going` race on its own: its window is a few
  microseconds, and a break of either fix passed the held-key test.
- Backlog entry added on this branch: "Panel: a search's cursor goes back
  to its first row". The same on main (checked on a throwaway server with
  a build of main).

#### Old test changed

`TestPickLinesSteps` (tmux-agent/internal/ui/ui_test.go): its fields 7
and 9 were the target of the row up and down land on; they are now the
row's key, and the test's rows and expectations use keys. The old result
is wrong now because a target names a pane by its index, which the
panel's join renumbers, so the noted row was lost (see the log above).
The item's Check, steps 5 and 6, say so: "the row stays open the whole
time", and "the cursor and the client stop together on one row".

`openPanel` (sidebar_test.go) moved its body to `openPanelAfter`, which
runs a setup before the panel opens; `openPanel` still waits for the fold
as before.

#### Tests failing on broken code

Break 1: `Display` ignores the whole name (`if false && r.Open && r.whole != ""`).
```
$ go test ./tmux-agent/internal/attention/ -run 'TestOpenRow'
--- FAIL: TestOpenRow (0.00s)
    pick_test.go:404: rows:
        proj-a
         ● ✳  a-popup-closed-by-its-hotkey-lea…
                                           3m
              ⎇ f-a-popup-closed-by-its-hotkey-
                leaves-the-client wt
         ● ✳  short-one                    3m
              ⎇ main
        want:
        proj-a
         ● ✳  a-popup-closed-by-its-hotkey-
              leaves-the-client
                                           3m
              ⎇ f-a-popup-closed-by-its-hotkey-
                leaves-the-client wt
         ● ✳  short-one                    3m
              ⎇ main
--- FAIL: TestOpenRowWithoutAgent (0.00s)
    pick_test.go:429: open: "      a-very-long-window-name-for-… zsh", want "      a-very-long-window-name-for-a-\n      shell-here\n                                    zsh"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/attention	0.002s
FAIL
```
The same break, end to end:
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run 'TestPanelOpensCursorRow|TestPanelOpensRowOnMouseAndHeldKey' 2>&1 | grep -v "^        20" | grep -E "^(---|    sidebar_test.go:[0-9]+: [a-z]|ok|FAIL)"
--- FAIL: TestPanelOpensCursorRow (10.56s)
    sidebar_test.go:675: the split window's row never opened:
    sidebar_test.go:129: tmux-agent log (<nil>):
--- FAIL: TestPanelOpensRowOnMouseAndHeldKey (5.51s)
    sidebar_test.go:715: timed out after 5s waiting for the clicked row open
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	16.093s
FAIL
```

Break 2: the labels never open (`if true || !r.Open || ...`).
```
$ go test ./tmux-agent/internal/attention/ -run 'TestOpenRow$'
--- FAIL: TestOpenRow (0.00s)
    pick_test.go:404: rows:
        proj-a
         ● ✳  a-popup-closed-by-its-hotkey-
              leaves-the-client
                                           3m
              ⎇ f-a-popup-closed-by-its-hotkey…
         ● ✳  short-one                    3m
              ⎇ main
        want:
        proj-a
         ● ✳  a-popup-closed-by-its-hotkey-
              leaves-the-client
                                           3m
              ⎇ f-a-popup-closed-by-its-hotkey-
                leaves-the-client wt
         ● ✳  short-one                    3m
              ⎇ main
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/attention	0.001s
FAIL
```

Break 3: a split window's row keyed by its best pane (`row.Key = r.Pane`).
```
$ go test ./tmux-agent/internal/attention/ -run 'TestRowKeys'
--- FAIL: TestRowKeys (0.00s)
    pick_test.go:445: rows:
        =proj-a:|
        =proj-a:1|%5
        =proj-a:1.1|%5
        =proj-a:1.2|%6
        =proj-a:2|%7
        want:
        =proj-a:|
        =proj-a:1|@3
        =proj-a:1.1|%5
        =proj-a:1.2|%6
        =proj-a:2|%7
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/attention	0.002s
FAIL
```

Break 4: a move does not read the rows again (`openRow` returns `"true"`).
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run 'TestPanelOpensPaneRow' 2>&1 | grep -v "^        20"
--- FAIL: TestPanelOpensPaneRow (5.54s)
    sidebar_test.go:770: timed out after 5s waiting for the first pane's row open, the second cut
    sidebar_test.go:759: panel:
          [tree] state                    tab ⇄
         search
         proj-a
          ●    w-one
        ▌   ● .1    sh                      2h
        ▌           ⎇ a-long-branch-name-for-th…
            ● .2    sh                      2h
                    ⎇ a-long-branch-name-for-th…
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	5.555s
FAIL
```

Break 5: a reload does not keep the open row (`open = ""` for `open = a.openNoted()`).
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run 'TestPanelOpensCursorRow' 2>&1 | grep -v "^        20"
--- FAIL: TestPanelOpensCursorRow (1.00s)
    sidebar_test.go:662: the open row closed after 425.138641ms:
          [tree] state                    tab ⇄
         search
         proj-a
          ●    a-popup-closed-by-its-hotkey-lea…
                                            1s
        ▌ ●    a-split-window-with-an-agent-and…
        ▌                                   1s
          ?    short-one                    0s
            1 more window
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
        
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	1.013s
FAIL
```

Break 7: the search's reload names field 10 (`list := opts.Self + " pick-rows --fzf --open={10}"`).
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run 'TestPanelSearchWithoutMatch' 2>&1 | grep -v "^        20" | grep -E "^(---|    sidebar_test.go:[0-9]+: [a-z]|ok|FAIL)"
--- FAIL: TestPanelSearchWithoutMatch (5.35s)
    sidebar_test.go:704: timed out after 5s waiting for the rows back, the client's row open
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	5.367s
FAIL
```

#### Review 1, finding 1: a task's row went to its lowest window

The row of a task with two windows took the client to the window with
the lowest id, not to the one that needs the user. The script, run with
`bash repro.sh` on a throwaway server (own HOME and XDG folders, one
client, session proj-a, `TMUX` set to its socket):
```
tmux-agent start --session proj-a --name task-one-a --task t1 --source wb --title t1-title -- sleep 99999 >/dev/null
tmux-agent start --session proj-a --name task-one-b --task t1 --source wb -- sleep 99999 >/dev/null
a=$(tmux display -p -t =proj-a:task-one-a '#{pane_id}'); b=$(tmux display -p -t =proj-a:task-one-b '#{pane_id}')
tmux-agent flag working --pane $a --quiet >/dev/null
tmux-agent flag needs-you --pane $b --reason permission --quiet >/dev/null
tmux-agent tmux-init; C=$(tmux list-clients -F '#{client_name}'); tmux-agent popup $C; sleep 1.5
P=$(tmux list-panes -a -F '#{?#{@sidebar_client},#{pane_id},}' | tr -d '\n')
tmux send-keys -t $P Up; sleep 1.2
tmux capture-pane -p -t $P | head -5
echo "client on: $(tmux list-clients -F '#{session_name}:#{window_index} #{window_name}')"
```
Before the fix (5977244):
```
  [tree] state                   tab ⇄
 search
 proj-a
▌ ? ◆  t1-title                    3s
    1 more window
client on: proj-a:1 task-one-a
```
After the fix:
```
  [tree] state                   tab ⇄
 search
 proj-a
▌ ? ◆  t1-title                    2s
    1 more window
client on: proj-a:2 task-one-b
```
The task row's key is now the pane it stands for; its other panes' keys
name the row too (`Keys`), so it stays open when that pane changes.
Test `TestPanelTaskRowGoesToWaitingPane`, before the fix:
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run TestPanelTaskRowGoesToWaitingPane 2>&1 | grep -v "^        20"
--- FAIL: TestPanelTaskRowGoesToWaitingPane (5.51s)
    sidebar_test.go:810: timed out after 5s waiting for the client in task-one-b
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	5.520s
FAIL
```
No other row is keyed by a pane outside the window its go goes to: a
window's row by its own pane, a split window's by the window, a fold by
its session.

#### Review 2, finding 1: a split window's open row showed cut for a moment

With the cursor on a split window's open row, a pane that lost its flag
turned the row into a one-pane row, keyed by the pane, not the window:
the reload's noted key matched no row, and the row showed cut until
place read the rows again. The script (`$D` a `mktemp -d` folder; it
takes the build to run, `before` = 3206531, `after` = the fix), run as
`bash $D/run.sh before` and `bash $D/run.sh after`:
```
set -u
D=$D
export HOME=$D/home XDG_STATE_HOME=$D/state XDG_RUNTIME_DIR=$D/run XDG_CONFIG_HOME=$D/home/.config PATH=$D/bin:/usr/bin:/bin
unset TMUX TMUX_PANE
cp $D/tmux-agent-$1 $D/bin/tmux-agent
tmux -S $D/sock kill-server 2>/dev/null; sleep 0.2
tmux -S $D/sock -f $D/tmux.conf new-session -d -s proj-a -n w-one -x 120 -y 30
export TMUX=$D/sock,$(tmux -S $D/sock display -p '#{pid}'),0
[ "$(tmux display -p '#{socket_path}')" = "$D/sock" ] || exit 1
(script -qfc "tmux -S $D/sock attach -t proj-a" /dev/null </dev/null >/dev/null 2>&1 &); sleep 1
a=$(tmux new-window -n split-window-with-two-agents-in-its-panes -P -F '#{pane_id}'); b=$(tmux split-window -d -t $a -P -F '#{pane_id}')
tmux-agent flag working --pane $a --quiet >/dev/null; tmux-agent flag working --pane $b --quiet >/dev/null
tmux-agent tmux-init; C=$(tmux list-clients -F '#{client_name}'); tmux-agent popup $C; sleep 1.5
PANEL=$(tmux list-panes -a -F '#{?#{@sidebar_client},#{pane_id},}' | tr -d '\n')
(for i in $(seq 1 150); do tmux capture-pane -p -t $PANEL | grep -E 'split-window|its-panes'; echo ---; sleep 0.01; done) > $D/mon.txt & mp=$!
sleep 0.2; tmux-agent flag clear --pane $b --quiet; wait $mp
awk '/---/{print s; s=""; next}{s=s" | "$0}' $D/mon.txt | uniq -c
tmux kill-server
```
Output:
```
== before (3206531)
     18  | ▌ ●    split-window-with-two-agents-in- | ▌      its-panes
      4  | ▌ ●    split-window-with-two-agents-in…
    128  | ▌ ●    split-window-with-two-agents-in- | ▌      its-panes
== after
    150  | ▌ ●    split-window-with-two-agents-in- | ▌      its-panes
```
The fix: a window's row that stands for one pane is also named by the
window's id (`Keys`), and a reload opens the row any name of the open
row names (its key and `Keys`), so a row whose key changes stays open.
Place reads the rows again only when the row it puts the cursor on is
not the open one. Same fault elsewhere: a window row without an agent
is keyed by its best pane, which can change; the window's id names it
too now. Test `TestPanelSplitRowStaysOpen`, before the fix:
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run TestPanelSplitRowStaysOpen 2>&1 | grep -v "^        20"
--- FAIL: TestPanelSplitRowStaysOpen (0.45s)
    sidebar_test.go:838: the open row showed cut:
          [tree] state                    tab ⇄
         search
         proj-a
        ▌ ●    split-window-with-two-agents-in-…
        ▌                                   0s
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	0.460s
FAIL
```

#### How a review runs check step 7 (click and wheel)

fzf in the panel reads xterm SGR mouse sequences from its pane's input,
so `tmux send-keys -l` with the sequence acts as a real click or wheel
move. On a throwaway server (own HOME and XDG folders, `TMUX` set to its
socket, one client attached through `script(1)`), with agent windows
whose names the panel cuts:
```
C=$(tmux list-clients -F '#{client_name}')
tmux-agent tmux-init; tmux-agent popup "$C"; sleep 1.5
PANEL=$(tmux list-panes -a -F '#{?#{@sidebar_client},#{pane_id},}' | tr -d '\n')
# y: the panel's screen line (from 1) of a row, by a part of its name
y=$(tmux capture-pane -p -t "$PANEL" | grep -n 'part-of-a-name' | head -1 | cut -d: -f1)
# click: press, then release, of the left button at column 10, line y
tmux send-keys -t "$PANEL" -l $'\e[<0;10;'"$y"'M'
tmux send-keys -t "$PANEL" -l $'\e[<0;10;'"$y"'m'
# wheel: one step down (65; 64 is up) over the list, at column 10, line y
tmux send-keys -t "$PANEL" -l $'\e[<65;10;'"$y"'M'
sleep 1; tmux capture-pane -p -t "$PANEL"
```
The column and line count from 1, in the panel pane's own coordinates.
Send the press and the release as two calls: one string with both, or a
`y` that matched two lines, types the rest into the search. The click
puts the cursor on the row at line y; one wheel step moves it one row.
After each, the row under the cursor shows its whole name, and the row
it left shows cut. `TestPanelOpensRowOnMouseAndHeldKey` does the same.

#### Review 2 of 889b9ef, finding 1: two rows open as a window splits

With the cursor on the open row of a window with one agent pane and a
split beside it, a flag on the split turned the row into a header over
a row per pane, and a reload opened both the header (keyed by the
window's id, which also named the one-pane row) and the cursor's pane
row; for a moment the cursor's row then showed cut. The script, in a
`mktemp -d` folder with the two builds beside it (`before` = 889b9ef,
`after` = the fix), run as `bash run.sh before` and `bash run.sh after`
(the ages are replaced by `Ns`, so frames that differ only in an age
count as one):
```
set -u
D=$(cd "$(dirname "$0")" && pwd)
export HOME=$D/home XDG_STATE_HOME=$D/state XDG_RUNTIME_DIR=$D/run XDG_CONFIG_HOME=$D/home/.config PATH=$D/bin:/usr/bin:/bin
unset TMUX TMUX_PANE
rm -f $D/bin/tmux-agent; cp $D/tmux-agent-$1 $D/bin/tmux-agent
tmux -S $D/sock kill-server 2>/dev/null; sleep 0.2
tmux -S $D/sock -f $D/tmux.conf new-session -d -s proj-a -n w-one -x 120 -y 30
export TMUX=$D/sock,$(tmux -S $D/sock display -p '#{pid}'),0
[ "$(tmux display -p '#{socket_path}')" = "$D/sock" ] || exit 1
(script -qfc "tmux -S $D/sock attach -t proj-a" /dev/null </dev/null >/dev/null 2>&1 &); sleep 1
C=$(tmux list-clients -F '#{client_name}')
a=$(tmux new-window -n a-split-window-whose-name-is-cut-in-the-panel -P -F '#{pane_id}'); b=$(tmux split-window -d -h -t $a -P -F '#{pane_id}')
tmux-agent flag working --pane $a --quiet; tmux-agent report --source git --text '⎇ a-long-branch-name-for-the-first-pane-here' --pane $a
tmux-agent tmux-init; tmux-agent popup $C; sleep 2
PANEL=$(tmux list-panes -a -F '#{?#{@sidebar_client},#{pane_id},}' | tr -d '\n')
(for i in $(seq 1 100); do tmux capture-pane -p -t $PANEL | sed -n 4,9p; echo ---; sleep 0.01; done) > $D/mon.txt & mp=$!
sleep 0.2; tmux-agent flag working --pane $b --quiet; wait $mp
awk '/---/{print s; s=""; next}{s=s" | "$0}' $D/mon.txt | sed 's/ [0-9]s / Ns /g' | uniq -c
tmux kill-server
```
Output:
```
== before (889b9ef)
idle
idle
     17  | ▌ ●    a-split-window-whose-name-is- | ▌      cut-in-the-panel | ▌                                  Ns | ▌      ⎇ a-long-branch-name-for-the- | ▌        first-pane-here |     1 more window
     62  |   ●    a-split-window-whose-name-is- |        cut-in-the-panel | ▌   ● .1    zsh                    Ns | ▌           ⎇ a-long-branch-name-for- | ▌             the-first-pane-here |     ● .2    zsh                    0s
      3  |   ●    a-split-window-whose-name-is- |        cut-in-the-panel | ▌   ● .1    zsh                    Ns | ▌           ⎇ a-long-branch-name-for-t… |     ● .2    zsh                    Ns |     1 more window
     18  |   ●    a-split-window-whose-name-is-cu… | ▌   ● .1    zsh                    Ns | ▌           ⎇ a-long-branch-name-for- | ▌             the-first-pane-here |     ● .2    zsh                    Ns |     1 more window
== after
idle
idle
     17  | ▌ ●    a-split-window-whose-name-is- | ▌      cut-in-the-panel | ▌                                  Ns | ▌      ⎇ a-long-branch-name-for-the- | ▌        first-pane-here |     1 more window
     83  |   ●    a-split-window-whose-name-is-cu… | ▌   ● .1    zsh                    Ns | ▌           ⎇ a-long-branch-name-for- | ▌             the-first-pane-here |     ● .2    zsh                    Ns |     1 more window
```
The fix: a reload opens one row, the first whose own key is the open
row's key (or, in order, one of its other names), else the first row
one of those names names. Same fault elsewhere: a task's row's other
names are its own panes, which have no row of their own while the task
has its row. Test `TestPanelOneRowOpensAsWindowSplits`, before the fix:
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run TestPanelOneRowOpensAsWindowSplits 2>&1 | grep -v "^        20" | sed '/^ *$/d'
--- FAIL: TestPanelOneRowOpensAsWindowSplits (1.30s)
    sidebar_test.go:869: a row other than the cursor's open, or the cursor's cut:
          [tree] state                    tab ⇄
         search
         proj-a
          ●    a-split-window-whose-name-is-cut-
               in-the-panel
        ▌   ● .1    sh                      2s
        ▌           ⎇ a-long-branch-name-for-th…
            ● .2    sh                      1s
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	1.309s
FAIL
```

### Demo (wb-demo, 2026-11-16)

Build 6921ce8 (`make build`, `bin/tmux-agent` copied to the demo's
`bin/`), on a throwaway tmux 3.8 server (`tmux -S <tmp>/sock -f
<tmp>/tmux.conf`, its own HOME and XDG folders, PATH `<tmp>/bin:/usr/bin:/bin`,
fzf 0.75.0), attached in a ghostty window (66x37) opened with `swaymsg
exec`. Session proj-a: `w-one` (done, `⎇ main`),
`a-popup-closed-by-its-hotkey-leaves-the-client` (working, `⎇
f-a-popup-closed-by-its-hotkey-leaves-the-client wt`),
`another-long-window-name-for-holding-the-down-key` (working, a long
branch), `a-third-long-name-that-the-panel-cuts-at-its-edge` (needs-you,
`⎇ main`), `split-window-with-two-agents-in-its-panes` (two panes, both
working, each a long branch), `short-two` (done, `⎇ main`),
`a-plain-shell-window-with-a-very-long-name-here` (no flag). Flags and
labels set with `tmux-agent flag` and `tmux-agent report --source git`.

#### Step 1

```
$ tmux-agent popup /dev/pts/24; sleep 1.5
$ tmux send-keys -t %8 Up   # repeated, 0.6 s apart, until the client is on window 2
Up 1 → client on 3 another-long-window-name-for-holding-the-down-key
Up 2 → client on 2 a-popup-closed-by-its-hotkey-leaves-the-client
$ tmux capture-pane -p -t %8 | sed '/^ *$/d'
  [tree] state                    tab ⇄
 search
 proj-a
▌ ●    a-popup-closed-by-its-hotkey-
▌      leaves-the-client
▌                                  50s
▌      ⎇ f-a-popup-closed-by-its-hotkey-
▌        leaves-the-client wt
  ●    another-long-window-name-for-hol…
                                   50s
       ⎇ f-another-long-window-name-for…
  ?    a-third-long-name-that-the-panel…
                                   50s
       ⎇ main
  ●    split-window-with-two-agents-in-…
    ● .1    sh                     50s
            ⎇ a-long-branch-name-for-th…
    ● .2    sh                     50s
            ⎇ a-long-branch-name-for-th…
  ✓    short-two                   50s
       ⎇ main
    2 more windows
```
(The user clicked rows in the panel at the same time, `fzf after click`
in the log at 20:54:50–20:55:06; so the first Up started from window 3's
row.)

wb-demo: PASS. The whole name over two lines, broken after a `-`
(`…hotkey-` / `leaves-the-client`); the age on the line below the name;
the rows below moved down two lines; the other rows stay cut.

User (2026-11-16): "what are those numbers? it looks weird. as I
understand its listed panels per window. its fine. remove numbers"
(about the `.1` and `.2` of the split window's pane rows; main shows
them the same way). Then: "PASS".

#### Step 2

The user had moved the cursor to window 4; Up brought it back.
```
$ for i in 1 2 3 4 5 6; do l=$(tmux list-clients -F '#{window_index}'); [ "$l" = 2 ] && break; tmux send-keys -t %8 Up; sleep 0.6; done; sleep 1
$ tmux list-clients -F '#{window_index} #{window_name}'
2 a-popup-closed-by-its-hotkey-leaves-the-client
$ tmux capture-pane -p -t %8 | sed -n 1,11p | sed 's/$/|/'
  [tree] state                    tab ⇄|
 search|
 proj-a|
▌ ●    a-popup-closed-by-its-hotkey-|
▌      leaves-the-client|
▌                                   3m|
▌      ⎇ f-a-popup-closed-by-its-hotkey-|
▌        leaves-the-client wt|
  ●    another-long-window-name-for-hol…|
                                    3m|
       ⎇ f-another-long-window-name-for…|
$ tmux show -pv -t %1 @labels
[{"source":"git","text":"⎇ f-a-popup-closed-by-its-hotkey-leaves-the-client wt"}]
```
wb-demo: PASS. The label shows whole over two lines, broken after a `-`;
the first line starts at column 7, the second at column 9 (two more);
the other rows' labels stay cut.

User (2026-11-16): "PASS".

#### Step 3

```
$ tmux list-clients -F '#{window_index} #{window_name}'
2 a-popup-closed-by-its-hotkey-leaves-the-client
$ tmux send-keys -t %8 Down; sleep 1.2
$ tmux list-clients -F '#{window_index} #{window_name}'
3 another-long-window-name-for-holding-the-down-key
$ tmux capture-pane -p -t %8 | sed -n 1,14p | sed 's/$/|/'
  [tree] state                    tab ⇄|
 search|
 proj-a|
  ●    a-popup-closed-by-its-hotkey-lea…|
                                    4m|
       ⎇ f-a-popup-closed-by-its-hotkey…|
▌ ●    another-long-window-name-for-|
▌      holding-the-down-key|
▌                                   4m|
▌      ⎇ f-another-long-window-name-for-|
▌        holding-the-down-key wt|
  ?    a-third-long-name-that-the-panel…|
                                    4m|
       ⎇ main|
```
wb-demo's own run (not a step): main (958b265, the branch's merge base)
built in the demo's temporary folder, and both builds' `pick-rows` (no
row open) against the same server, ages replaced by `AGE`:
```
$ $D/tmux-agent-main pick-rows > $D/rows-main.txt; $D/bin/tmux-agent pick-rows > $D/rows-branch.txt
$ sed 's/ [0-9]\+[smh] *$/ AGE/' $D/rows-main.txt > $D/a; sed 's/ [0-9]\+[smh] *$/ AGE/' $D/rows-branch.txt > $D/b; diff $D/a $D/b
1c1
< proj-a	=proj-a:	^[[1mproj-a^[[0m			up		down	
---
> proj-a	=proj-a:	^[[1mproj-a^[[0m			up		down		
4c4
<       ^[[38;2;108;112;134mM-bM-^NM-^G f-a-popup-closed-by-its-hotkeyM-bM-^@M-&^[[0m	=proj-a:2		ignore	=proj-a:2	down	=proj-a:3
---
>       ^[[38;2;108;112;134mM-bM-^NM-^G f-a-popup-closed-by-its-hotkeyM-bM-^@M-&^[[0m	=proj-a:2		ignore	%1	down	%2	%1
7c7
<       ^[[38;2;108;112;134mM-bM-^NM-^G f-another-long-window-name-forM-bM-^@M-&^[[0m	=proj-a:3		up	=proj-a:2	down	=proj-a:4
---
>       ^[[38;2;108;112;134mM-bM-^NM-^G f-another-long-window-name-forM-bM-^@M-&^[[0m	=proj-a:3		up	%1	down	%3	%2
10,11c10,11
<       ^[[38;2;108;112;134mM-bM-^NM-^G main^[[0m	=proj-a:4		up	=proj-a:3	down	=proj-a:5
< 	=proj-a:5	 ^[[38;2;137;180;250mM-bM-^WM-^O^[[0m    split-window-with-two-agents-in-M-bM-^@M-&^[[0m	=proj-a:5		up	=proj-a:4	down	=proj-a:5.1
---
>       ^[[38;2;108;112;134mM-bM-^NM-^G main^[[0m	=proj-a:4		up	%2	down	@4	%3
> 	=proj-a:5	 ^[[38;2;137;180;250mM-bM-^WM-^O^[[0m    split-window-with-two-agents-in-M-bM-^@M-&^[[0m	=proj-a:5		up	%3	down	%4	@4
13c13
<            ^[[38;2;108;112;134mM-bM-^NM-^G a-long-branch-name-for-thM-bM-^@M-&^[[0m	=proj-a:5.1		up	=proj-a:5	down	=proj-a:5.2
---
>            ^[[38;2;108;112;134mM-bM-^NM-^G a-long-branch-name-for-thM-bM-^@M-&^[[0m	=proj-a:5.1		up	@4	down	%5	%4
15c15
<            ^[[38;2;108;112;134mM-bM-^NM-^G a-long-branch-name-for-thM-bM-^@M-&^[[0m	=proj-a:5.2		up	=proj-a:5.1	down	=proj-a:6
---
>            ^[[38;2;108;112;134mM-bM-^NM-^G a-long-branch-name-for-thM-bM-^@M-&^[[0m	=proj-a:5.2		up	%4	down	%6	%5
17,18c17,18
<       ^[[38;2;108;112;134mM-bM-^NM-^G main^[[0m	=proj-a:6		up	=proj-a:5.2	down	=proj-a:
< 	=proj-a:	^[[38;2;108;112;134m   2 more windows^[[0m		$0	up	=proj-a:6	ignore	=proj-a:
---
>       ^[[38;2;108;112;134mM-bM-^NM-^G main^[[0m	=proj-a:6		up	%5	down	$0	%6
> 	=proj-a:	^[[38;2;108;112;134m   2 more windows^[[0m		$0	up	%6	ignore	$0	$0
```
The shown text of every row is the same; only fzf's hidden fields
differ: up and down name a row's key (`%1`, `@4`, `$0`) where main named
a target, and a last field holds the row's key.

wb-demo: PASS. The row the cursor left shows one cut name line again,
with its age line and its cut label line, as main shows it; the next
row opened; the client followed to window 3.

User (2026-11-16): "PASS".

#### Step 4

```
$ for i in 1 2 3 4 5 6 7; do l=$(tmux list-clients -F '#{window_index}'); [ "$l" = 6 ] && break; tmux send-keys -t %8 Down; sleep 0.6; done; sleep 1
$ tmux list-clients -F '#{window_index} #{window_name}'
6 short-two
$ tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
  [tree] state                    tab ⇄|
 search|
 proj-a|
  ●    a-popup-closed-by-its-hotkey-lea…|
                                    6m|
       ⎇ f-a-popup-closed-by-its-hotkey…|
  ●    another-long-window-name-for-hol…|
                                    6m|
       ⎇ f-another-long-window-name-for…|
  ?    a-third-long-name-that-the-panel…|
                                    6m|
       ⎇ main|
  ●    split-window-with-two-agents-in-…|
    ● .1    sh                      6m|
            ⎇ a-long-branch-name-for-th…|
    ● .2    sh                      6m|
            ⎇ a-long-branch-name-for-th…|
▌ ✓    short-two                    6m|
▌      ⎇ main|
    2 more windows|
```
wb-demo's own runs (not a step):
```
$ $D/tmux-agent-main pick-rows | grep -A1 'short-two' | cut -f3 | cat -v
 ^[[38;2;166;227;161mM-bM-^\M-^S^[[0m    short-two^[[0m                  ^[[0m ^[[38;2;108;112;134m 6m^[[0m

$ $D/bin/tmux-agent pick-rows | grep -A1 short-two | cut -f3 | cat -v
 ^[[38;2;166;227;161mM-bM-^\M-^S^[[0m    short-two^[[0m                  ^[[0m ^[[38;2;108;112;134m 7m^[[0m
$ $D/bin/tmux-agent pick-rows --open=%6; echo "exit $?"
exit 0
$ for o in %6 %1; do echo "== --fzf --open=$o"; FZF_COLUMNS=40 FZF_QUERY= $D/bin/tmux-agent pick-rows --fzf --open=$o | tr '\0' '\n' | grep -A3 -E "short-two|a-popup" | cut -f3 | sed 's/\x1b\[[0-9;]*m//g' | sed 's/$/|/'; done
== --fzf --open=%6
 ●    a-popup-closed-by-its-hotkey-lea…|
                                   7m|
|
 ●    another-long-window-name-for-hol…|
                                   7m|
|
--|
 ✓    short-two                    7m|
|
   2 more windows|
== --fzf --open=%1
 ●    a-popup-closed-by-its-hotkey-|
      leaves-the-client|
                                   7m|
      ⎇ f-a-popup-closed-by-its-hotkey-|
|
 ●    another-long-window-name-for-hol…|
                                   7m|
--|
 ✓    short-two                    7m|
|
   2 more windows|
```
wb-demo: PASS. The short row under the cursor is one line with its age,
its label below, the same text main prints. Seen besides:
`pick-rows --open=%6` without `--fzf` prints nothing and exits 0 (the
flag is taken as a query; the usage lists `--open` only under `--fzf`).

User (2026-11-16): "PASS".

#### Step 5

Cursor first on window 3's row (Up, 0.6 s apart, until the client was
on window 3). Then:
```
$ rm -f $D/mon5.txt
$ (for i in $(seq 1 1000); do printf '%s ' "$(date +%T.%2N)"; tmux capture-pane -p -t %8 | sed -n 4,6p | tr '\n' '|'; echo; sleep 0.01; done) > $D/mon5.txt & mp=$!
$ sleep 0.5; echo "$(date +%T.%2N) Up (cursor to a-popup row: the go joins the panel into window 2)"; tmux send-keys -t %8 Up
$ sleep 3; echo "$(date +%T.%2N) flag needs-you on %1 (a flag change; the age restarts in seconds)"; tmux-agent flag needs-you --pane %1 --reason permission --quiet
$ sleep 4; echo "$(date +%T.%2N) flag working on %1 (another flag change)"; tmux-agent flag working --pane %1 --quiet
$ wait $mp
$ echo "client on: $(tmux list-clients -F '#{window_index} #{window_name}'), panel in window $(tmux display -p -t %8 '#{window_index}')"
$ awk '{t=$1; $1=""; if ($0!=p) {if (n) printf "%5d  %s %s\n", n, ft, p; p=$0; ft=t; n=0} n++} END {printf "%5d  %s %s\n", n, ft, p}' $D/mon5.txt
monitor pid 2401118
21:02:38.02 Up (cursor to a-popup row: the go joins the panel into window 2)
21:02:41.03 flag needs-you on %1 (a flag change; the age restarts in seconds)
working
21:02:45.09 flag working on %1 (another flag change)
needs-you
client on: 2 a-popup-closed-by-its-hotkey-leaves-the-client, panel in window 2
--- frames, consecutive equal frames counted once (time of the first):
   32  21:02:37.52  ● a-popup-closed-by-its-hotkey-lea…| 8m| ⎇ f-a-popup-closed-by-its-hotkey…|
    3  21:02:38.03  ▌ ● a-popup-closed-by-its-hotkey-lea…|▌ 8m|▌ ⎇ f-a-popup-closed-by-its-hotkey…|
  176  21:02:38.10  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 8m|
   55  21:02:41.12  ▌ ? a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 0s|
   55  21:02:42.11  ▌ ? a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 1s|
   55  21:02:43.10  ▌ ? a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 2s|
   56  21:02:44.10  ▌ ? a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 3s|
    4  21:02:45.11  ▌ ? a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 4s|
   51  21:02:45.18  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 0s|
   55  21:02:46.10  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 1s|
   55  21:02:47.11  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 2s|
   56  21:02:48.11  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 3s|
   54  21:02:49.11  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 4s|
   56  21:02:50.10  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 5s|
   53  21:02:51.12  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 6s|
   55  21:02:52.10  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 7s|
   55  21:02:53.11  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 8s|
   53  21:02:54.13  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 9s|
   21  21:02:55.11  ▌ ● a-popup-closed-by-its-hotkey-|▌ leaves-the-client|▌ 10s|
$ grep -E '^2026-11-16T21:02:(3[7-9]|4|5)' $XDG_STATE_HOME/tmux-agent/log | cut -c1-300
2026-11-16T21:02:38.053+02:00 2401374 go to the row in .want =proj-a:2: client /dev/pts/24 @2 → @1, panel %8 @2 → @1 [fzf after rebind]
2026-11-16T21:02:38.196+02:00 2401440 follow: client "/dev/pts/24" on @1, panel %8 in @1
2026-11-16T21:02:38.202+02:00 2401474 follow: client "/dev/pts/24" on @1, panel %8 in @1
```
wb-demo: PASS. The row opened 70 ms after the cursor came (3 cut frames
before it first opened), then showed open in every frame: through the
panel's join into window 2 (38.053) and the follow hook's reloads, 14
age ticks, and two flag changes (● → ? → ●). Seen besides: `flag
--quiet` still prints the previous state, as its usage says.

User (2026-11-16): "PASS".

#### Step 6

A first simulated run is void: the user moved the client to window 5
while it ran, and `short-two` had gone idle into the fold (`3 more
windows`). Second run, from the top row:
```
$ for i in $(seq 1 10); do l=$(tmux list-clients -F '#{window_index}'); [ "$l" = 2 ] && break; tmux send-keys -t %8 Up; sleep 0.6; done; sleep 1.5
$ (for i in $(seq 1 80); do echo "$(date +%T.%2N) $(tmux list-clients -F '#{window_index}.#{pane_index}')"; sleep 0.05; done) > $D/mon6.txt & mp=$!
$ sleep 0.3; echo "$(date +%T.%2N) first Down"; for i in 1 2 3 4; do tmux send-keys -t %8 Down; sleep 0.033; done; echo "$(date +%T.%2N) last Down (4 keys, 33 ms apart)"
$ wait $mp; awk '$2!=p {print; p=$2}' $D/mon6.txt
$ awk -v s="$(head -1 $D/mon6.txt | cut -d' ' -f1)" '{split($1,a,"T"); if (substr(a[2],1,11) >= s) print}' $XDG_STATE_HOME/tmux-agent/log | cut -c12-250
$ tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
start: client on 2.1 a-popup-closed-by-its-hotkey-leaves-the-client
▌ ●    a-popup-closed-by-its-hotkey-
▌      leaves-the-client
▌                                   2m
21:05:01.11 first Down
21:05:01.27 last Down (4 keys, 33 ms apart)
--- client's window.pane, changes only:
21:05:00.81 2.1
21:05:01.20 3.1
21:05:01.37 5.1
(last sample 21:05:05.29 5.1)
--- log:
21:05:01.147+02:00 2421696 go to the row in .want =proj-a:3: client /dev/pts/24 @1 → @2, panel %8 @1 → @2 [fzf after rebind]
21:05:01.295+02:00 2421751 follow: client "/dev/pts/24" on @2, panel %8 in @2
21:05:01.328+02:00 2421696 go to the row in .want =proj-a:5: client /dev/pts/24 @2 → @4, panel %8 @2 → @4 [fzf after rebind]
21:05:01.371+02:00 2421996 follow: client "/dev/pts/24" on @4, panel %8 in @4
21:05:01.388+02:00 2421961 follow: client "/dev/pts/24" on @4, panel %8 in @4
21:05:01.431+02:00 2421744 follow: client "/dev/pts/24" on @4, panel %8 in @4
--- panel:
  [tree] state                    tab ⇄|
 search|
 proj-a|
  ●    a-popup-closed-by-its-hotkey-lea…|
                                    2m|
       ⎇ f-a-popup-closed-by-its-hotkey…|
  ●    another-long-window-name-for-hol…|
                                   10m|
       ⎇ f-another-long-window-name-for…|
  ?    a-third-long-name-that-the-panel…|
                                   10m|
       ⎇ main|
  ●    split-window-with-two-agents-in-…|
▌   ● .2    sh                     10m|
▌           ⎇ a-long-branch-name-for-|
▌             the-first-pane-of-the-|
▌             split|
    ● .3    sh                     10m|
            ⎇ a-long-branch-name-for-th…|
    3 more windows|
```
wb-demo: PASS (simulated keys). The cursor and the client stopped
together in window 5, on its first pane's row, which is open (its
label over three lines); one go took the moves, skipped window 4, and
its last move came 60 ms after the last key; nothing ran after. Seen
besides: in window 5 the split's pane rows are `.2` and `.3`, elsewhere
`.1` and `.2`.

The user then held ↓ in the demo window and sent two screenshots: the
split's pane rows `.1`/`.2` with the panel in window 4, and `.2`/`.3`
with the cursor stopped on the split window's open row.

User (2026-11-16): "this I found bug. see there is no "first" pane (with
.1) in the list. but im not sure what are those numbers". Then: "PASS".

wb-demo's run on the user's finding (not a step): the numbers are tmux
pane indexes; the panel's own pane takes index 1 in the window it joins,
and main shows the same:
```
$ echo "panel in window $(tmux display -p -t %8 '#{window_index}')"; tmux list-panes -t @4 -F '#{pane_index} #{pane_id} #{?@sidebar_client,panel,}'
$ $D/tmux-agent-main pick-rows | grep -E '^	=proj-a:5' | cut -f3 | sed 's/\x1b\[[0-9;]*m//g'
panel in window 5
1 %8 panel
2 %4 
3 %5 
--- main's pick-rows, split window rows:
 ●    split-window-with-two-agents-in-…
   ● .2    sh                     13m
   ● .3    sh                     13m
```

#### Step 7

wb-demo's run, with SGR mouse sequences (the method above):
```
$ y=$(tmux capture-pane -p -t %8 | grep -n 'a-third-long' | head -1 | cut -d: -f1)
$ tmux send-keys -t %8 -l $'\e[<0;10;'"$y"'M'; tmux send-keys -t %8 -l $'\e[<0;10;'"$y"'m'; sleep 1.5
$ tmux list-clients -F '#{window_index} #{window_name}'; tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
$ y=$(tmux capture-pane -p -t %8 | grep -n 'a-third-long' | head -1 | cut -d: -f1)
$ tmux send-keys -t %8 -l $'\e[<64;10;'"$y"'M'; sleep 1.5
$ tmux list-clients -F '#{window_index} #{window_name}'; tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
client on: 5 split-window-with-two-agents-in-its-panes
click: line 10 (a-third-long…)
after click, client on: 4 a-third-long-name-that-the-panel-cuts-at-its-edge
  [tree] state                    tab ⇄|
 search|
 proj-a|
  ●    a-popup-closed-by-its-hotkey-lea…|
                                    5m|
       ⎇ f-a-popup-closed-by-its-hotkey…|
  ●    another-long-window-name-for-hol…|
                                   14m|
       ⎇ f-another-long-window-name-for…|
▌ ?    a-third-long-name-that-the-panel-|
▌      cuts-at-its-edge|
▌                                  14m|
▌      ⎇ main|
  ●    split-window-with-two-agents-in-…|
    ● .1    sh                     14m|
            ⎇ a-long-branch-name-for-th…|
    ● .2    sh                     14m|
            ⎇ a-long-branch-name-for-th…|
    3 more windows|
wheel up one step: line 10
after wheel, client on: 3 another-long-window-name-for-holding-the-down-key
  [tree] state                    tab ⇄|
 search|
 proj-a|
  ●    a-popup-closed-by-its-hotkey-lea…|
                                    6m|
       ⎇ f-a-popup-closed-by-its-hotkey…|
▌ ●    another-long-window-name-for-|
▌      holding-the-down-key|
▌                                  14m|
▌      ⎇ f-another-long-window-name-for-|
▌        holding-the-down-key wt|
  ?    a-third-long-name-that-the-panel…|
                                   14m|
       ⎇ main|
  ●    split-window-with-two-agents-in-…|
    ● .1    sh                     14m|
            ⎇ a-long-branch-name-for-th…|
    ● .2    sh                     14m|
            ⎇ a-long-branch-name-for-th…|
    3 more windows|
```
Near case (wb-demo's own run): a click on the session label line:
```
$ tmux send-keys -t %8 -l $'\e[<0;10;3M'; tmux send-keys -t %8 -l $'\e[<0;10;3m'; sleep 1.5
after a click on the session label 'proj-a' (line 3), client on: 3 another-long-window-name-for-holding-the-down-key
  [tree] state                    tab ⇄|
 search|
 proj-a|
  ●    a-popup-closed-by-its-hotkey-lea…|
                                    6m|
       ⎇ f-a-popup-closed-by-its-hotkey…|
▌ ●    another-long-window-name-for-|
▌      holding-the-down-key|
▌                                  14m|
▌      ⎇ f-another-long-window-name-for-|
▌        holding-the-down-key wt|
  ?    a-third-long-name-that-the-panel…|
```
wb-demo: PASS. The clicked row opened and the client went to window 4;
one wheel step moved the cursor one row up, which opened, and the row
it left showed cut. A click on the session label left the cursor and
the open row where they were.

The user then clicked and wheeled in the demo window.

User (2026-11-16): "PASS".

#### Step 8

```
$ for i in 1 2 3; do tmux send-keys -t %8 Down; sleep 0.8; done; sleep 1
$ tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
$ tmux send-keys -t %8 Down; sleep 1.5
$ tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed -n '/split-window/,$p' | sed 's/$/|/'
client on: 3 another-long-window-name-for-holding-the-down-key
after 3 Downs, client on: 5 split-window-with-two-agents-in-its-panes, active pane %8
  [tree] state                    tab ⇄|
 search|
 proj-a|
  ●    a-popup-closed-by-its-hotkey-lea…|
                                    6m|
       ⎇ f-a-popup-closed-by-its-hotkey…|
  ●    another-long-window-name-for-hol…|
                                   15m|
       ⎇ f-another-long-window-name-for…|
  ?    a-third-long-name-that-the-panel…|
                                   15m|
       ⎇ main|
  ●    split-window-with-two-agents-in-…|
▌   ● .2    sh                     15m|
▌           ⎇ a-long-branch-name-for-|
▌             the-first-pane-of-the-|
▌             split|
    ● .3    sh                     15m|
            ⎇ a-long-branch-name-for-th…|
    3 more windows|
--- Down once more (the second pane's row):
  ●    split-window-with-two-agents-in-…|
    ● .2    sh                     15m|
            ⎇ a-long-branch-name-for-th…|
▌   ● .3    sh                     15m|
▌           ⎇ a-long-branch-name-for-|
▌             the-second-pane-of-the-|
▌             split|
    3 more windows|
```
wb-demo: PASS. Only the cursor's pane row opened (its label whole over
three lines); the window's row and the other pane's row stayed cut; one
Down moved the opening to the other pane's row.

User (2026-11-16): "PASS".

#### Step 9

```
$ tmux send-keys -t %8 -l 'third'; sleep 2
$ tmux list-clients -F '#{window_index} #{window_name}'; tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
after typing 'third', client on: 4 a-third-long-name-that-the-panel-cuts-at-its-edge
  [tree] state                    tab ⇄|
 third|
 proj-a|
▌ ?    a-third-long-name-that-the-panel-|
▌      cuts-at-its-edge|
▌                                  16m|
▌      ⎇ main|
```
Near case (wb-demo's own run): a search that keeps three rows, then Down:
```
$ tmux send-keys -t %8 C-u; sleep 0.5; tmux send-keys -t %8 -l 'long'; sleep 2
$ tmux send-keys -t %8 Down   # captures at 0.3 s and 2.3 s after it
after 'long', client on: 3 another-long-window-name-for-holding-the-down-key
  [tree] state                    tab ⇄|
 long|
 proj-a|
▌ ●    another-long-window-name-for-|
▌      holding-the-down-key|
▌                                  16m|
▌      ⎇ f-another-long-window-name-for-|
▌        holding-the-down-key wt|
  ?    a-third-long-name-that-the-panel…|
                                   16m|
       ⎇ main|
       a-plain-shell-window-with-a-v… sh|
0.3 s after Down, client on: 4 a-third-long-name-that-the-panel-cuts-at-its-edge
  [tree] state                    tab ⇄|
 long|
 proj-a|
  ●    another-long-window-name-for-hol…|
                                   16m|
       ⎇ f-another-long-window-name-for…|
▌ ?    a-third-long-name-that-the-panel-|
▌      cuts-at-its-edge|
▌                                  16m|
▌      ⎇ main|
       a-plain-shell-window-with-a-v… sh|
2.3 s after Down, client on: 4 a-third-long-name-that-the-panel-cuts-at-its-edge
(the same panel as at 0.3 s)
```
wb-demo: PASS. The searched row under the cursor showed open. In the
near case the moved-to row opened and stayed; the backlog entry "a
search's cursor goes back to its first row" did not show in this run.

User (2026-11-16): "PASS".

#### Step 10

```
$ bash go-gate/scripts/gate.sh; echo "gate.sh exit $?"
go-gate-legacy.txt: 74 files the gate does not check yet. To bring one in, delete its line, then fix what the gate reports in it. The first line is the one to take next: internal/applog/applog.go
gate.sh exit 0
$ make vet; echo "make vet exit $?"
go vet ./...
make vet exit 0
```
wb-demo: PASS. Both exit 0. The gate ran about 40 s (load 1.85 → 5.98).

User (2026-11-16): "PASS".

#### Decision: a row without an agent opens too

```
$ tmux send-keys -t %8 C-u; sleep 0.5; tmux send-keys -t %8 -l 'plain'; sleep 2
$ tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
search 'plain', client on: 7 a-plain-shell-window-with-a-very-long-name-here
  [tree] state                    tab ⇄|
 plain|
 proj-a|
▌      a-plain-shell-window-with-a-very-|
▌      long-name-here|
▌                                     sh|
```
wb-demo: as decided. The whole name over two lines, broken after a `-`;
the command `sh` on the line below at the row's right edge.

User (2026-11-16): a screenshot of the a-third row of the next decision,
then "Fine".

#### Decision: breaking after `-` or a space; a line with neither is cut at its width

wb-demo's run, a label with a word longer than a line (restored to
`⎇ main` after):
```
$ tmux-agent report --source git --text '⎇ abranchnamewithoutanydashesorspacesatallthatislong then a few short words' --pane %3
tmux-agent report: bad label: text longer than 64 characters
$ tmux-agent report --source git --text '⎇ nodashesnospacesbranchnamelongerthanaline then short words' --pane %3; echo "report exit $?"; sleep 1.5
$ tmux send-keys -t %8 C-u; sleep 0.5; tmux send-keys -t %8 -l 'third'; sleep 2
$ tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
report exit 0
label on %3: [{"source":"git","text":"⎇ nodashesnospacesbranchnamelongerthanaline then short words"}]
  [tree] state                    tab ⇄|
 third|
 proj-a|
▌ ?    a-third-long-name-that-the-panel-|
▌      cuts-at-its-edge|
▌                                  22m|
▌      ⎇|
▌        nodashesnospacesbranchnamelonge|
▌        rthanaline then short words|
```
wb-demo: as decided. The only break that fits on the first line is the
space after `⎇` (it goes), so `⎇` stands alone on its line; the word
with no break is cut at the line's width and goes on over the next line,
no letter lost. Near case: a label text over 64 characters is refused.

User (2026-11-16): "Fine".

#### Decision: a count after the name stays after the name's last line

```
$ tmux set -p -t %1 @agent_subagents 'sub-a sub-b'
$ tmux send-keys -t %8 C-u; sleep 0.5; tmux send-keys -t %8 -l 'popup'; sleep 2
$ tmux capture-pane -p -t %8 | sed '/^ *$/d' | sed 's/$/|/'
@agent_subagents on %1: sub-a sub-b
  [tree] state                    tab ⇄|
 popup|
 proj-a|
▌ ●    a-popup-closed-by-its-hotkey-|
▌      leaves-the-client ⑂2|
▌                                  15m|
▌      ⎇ f-a-popup-closed-by-its-hotkey-|
▌        leaves-the-client wt|
$ FZF_COLUMNS=40 FZF_QUERY= tmux-agent pick-rows --fzf | tr '\0' '\n' | grep -A1 'a-popup' | sed 's/\x1b\[[0-9;]*m//g' | cut -f3 | sed 's/$/|/'
 ●    a-popup-closed-by-its-hotkey-… ⑂2|
                                  15m|
|
 ●    another-long-window-name-for-hol…|
$ tmux set -pu -t %1 @agent_subagents
```
wb-demo: as decided. `⑂2` after the open name's last line, and after
the `…` of the cut row.

User (2026-11-16): "Fine".

#### Decision: a search with no match, cleared, brings the rows back with the client's row open

```
$ tmux send-keys -t %8 C-u; sleep 0.5; tmux send-keys -t %8 -l 'zzzq'; sleep 2
search 'zzzq' (no match), client on: 2 a-popup-closed-by-its-hotkey-leaves-the-client
  [tree] state                    tab ⇄|
 zzzq|
$ tmux send-keys -t %8 C-u; sleep 2
search cleared, client on: 2 a-popup-closed-by-its-hotkey-leaves-the-client
  [tree] state                    tab ⇄|
 search|
 proj-a|
▌ ●    a-popup-closed-by-its-hotkey-|
▌      leaves-the-client|
▌                                  16m|
▌      ⎇ f-a-popup-closed-by-its-hotkey-|
▌        leaves-the-client wt|
  ●    another-long-window-name-for-hol…|
                                   24m|
       ⎇ f-another-long-window-name-for…|
  ?    a-third-long-name-that-the-panel…|
                                   24m|
       ⎇ main|
  ●    split-window-with-two-agents-in-…|
    ● .1    sh                     24m|
            ⎇ a-long-branch-name-for-th…|
    ● .2    sh                     24m|
            ⎇ a-long-branch-name-for-th…|
    3 more windows|
```
wb-demo: as decided.

User (2026-11-16): "Fine".

#### Decision: the picker outside the panel opens its cursor's row

In a temporary window of the demo server (`@7`, killed by its id after):
```
$ tmux new-window -d -n picker-demo -P -F '#{pane_id} #{window_id}' "env TMUX_AGENT_DEMO=1 $D/bin/tmux-agent pick; sleep 600"
%9 @7
$ tmux resize-window -t @7 -x 40 -y 30; sleep 1.5; tmux capture-pane -p -t %9 | sed '/^ *$/d' | sed 's/$/|/'
  [tree] state                    tab ⇄|
 search|
 proj-a|
▌ ●    a-popup-closed-by-its-hotkey-|
▌      leaves-the-client|
▌                                  16m|
▌      ⎇ f-a-popup-closed-by-its-hotkey-|
▌        leaves-the-client wt|
  ●    another-long-window-name-for-hol…|
                                   25m|
       ⎇ f-another-long-window-name-for…|
  ?    a-third-long-name-that-the-panel…|
                                   25m|
       ⎇ main|
  ●    split-window-with-two-agents-in-…|
    ● .1    sh                     25m|
            ⎇ a-long-branch-name-for-th…|
    ● .2    sh                     25m|
            ⎇ a-long-branch-name-for-th…|
    4 more windows|
$ tmux send-keys -t %9 Down; sleep 1.5
--- after Down, client on: 3 another-long-window-name-for-holding-the-down-key
  [tree] state                    tab ⇄|
 search|
 proj-a|
  ●    a-popup-closed-by-its-hotkey-lea…|
                                   16m|
       ⎇ f-a-popup-closed-by-its-hotkey…|
▌ ●    another-long-window-name-for-|
▌      holding-the-down-key|
▌                                  25m|
▌      ⎇ f-another-long-window-name-for-|
▌        holding-the-down-key wt|
  ?    a-third-long-name-that-the-panel…|
                                   25m|
       ⎇ main|
  ●    split-window-with-two-agents-in-…|
    ● .1    sh                     25m|
            ⎇ a-long-branch-name-for-th…|
    ● .2    sh                     25m|
            ⎇ a-long-branch-name-for-th…|
    4 more windows|
$ tmux send-keys -t %9 Escape; sleep 1.5
after Esc, client on: 3 another-long-window-name-for-holding-the-down-key
$ tmux kill-window -t @7
```
wb-demo: as decided. Seen besides: after Esc the client stayed on
window 3, where the cursor took it, not window 2 where the picker
opened (started with no CLIENT or ORIGIN, in a window the client was not
in); the picker's `panel-<pid>-9.want`, `.gone` and `.place` stayed in
`$XDG_RUNTIME_DIR/tmux-agent/` after its pane was gone (main not
checked).

User (2026-11-16): "Fine".

#### Decision: `.want` and `.gone` hold the row's key

```
$ for f in $XDG_RUNTIME_DIR/tmux-agent/panel-2312430-8.{want,gone}; do echo "$(basename $f): $(cat $f)"; done
panel-2312430-8.want: %2
panel-2312430-8.gone: %2
$ tmux send-keys -t %8 Down; sleep 1.5
--- after Down in the panel, client on: 4 @3 a-third-long-name-that-the-panel-cuts-at-its-edge
panel-2312430-8.gone: %3
panel-2312430-8.want: %3
$ cat $XDG_RUNTIME_DIR/tmux-agent/panel-2312430-8.place
[{"Target":"=proj-a:","Fold":"","Session":"proj-a","Label":"proj-a","Key":""},{"Target":"=proj-a:2","Fold":"","Session":"proj-a","Label":"","Key":"%1","Keys":["@1"]},{"Target":"=proj-a:3","Fold":"","Session":"proj-a","Label":"","Key":"%2","Keys":["@2"]},{"Target":"=proj-a:4","Fold":"","Session":"proj-a","Label":"","Key":"%3","Keys":["@3"],"Open":true},{"Target":"=proj-a:5","Fold":"","Session":"proj-a","Label":"","Key":"@4"},{"Target":"=proj-a:5.1","Fold":"","Session":"proj-a","Label":"","Key":"%4"},{"Target":"=proj-a:5.2","Fold":"","Session":"proj-a","Label":"","Key":"%5"},{"Target":"=proj-a:","Fold":"$0","Session":"proj-a","Label":"","Key":"$0"}]
```
wb-demo: as decided. The files hold the pane id of the row, not a
target; steps 5 and 6 showed no row lost through the panel's joins.

User (2026-11-16): "Fine".

#### Backlog entries this branch adds

- "Conformance hook tests fail inside a Claude Code Stop hook (bug)".
  User (2026-11-16): "Keep entry".
- "Panel: a search's cursor goes back to its first row (bug)". It did
  not show in this demo (step 9's near case: search `long`, one Down to
  window 4, the cursor stayed 2.3 s). User (2026-11-16): "Keep entry".

#### Open after the demo

- The user's request, about the `.1`/`.2` before a split window's pane
  rows (main shows them the same way): "remove numbers".
- The user's finding: "this I found bug. see there is no "first" pane
  (with .1) in the list. but im not sure what are those numbers". The
  numbers are tmux pane indexes; with the panel in the split window, the
  panel's own pane is index 1 and the agent panes show `.2` and `.3`.

#### Review 4 of 8f0f9a9, finding 1: a search match across the open row's break

A search word that the open row's name breaks across lines (`a-lo` in
`extra-window-number-1-with-a-` / `long-name-cut`) showed without its
underline on the open row; cut rows underlined it. The user, asked on
2026-11-16 whether to fix it, accept it or note it in the backlog,
chose "Fix it (Recommended)". The script, in a `mktemp -d` folder with
the build in its `bin/` (8f0f9a9 before, the fix after), run as
`bash run.sh a-lo` and `bash run.sh number-1`:
```
set -u
D=$(cd "$(dirname "$0")" && pwd)
export HOME=$D/home XDG_STATE_HOME=$D/state XDG_RUNTIME_DIR=$D/run XDG_CONFIG_HOME=$D/home/.config PATH=$D/bin:/usr/bin:/bin
unset TMUX TMUX_PANE
tmux -S $D/sock -f $D/tmux.conf new-session -d -s proj-a -n w-one -x 120 -y 30
export TMUX=$D/sock,$(tmux -S $D/sock display -p '#{pid}'),0
[ "$(tmux display -p '#{socket_path}')" = "$D/sock" ] || exit 1
(script -qfc "tmux -S $D/sock attach -t proj-a" /dev/null </dev/null >/dev/null 2>&1 &); sleep 1
C=$(tmux list-clients -F '#{client_name}')
for n in 1 2; do p=$(tmux new-window -n extra-window-number-$n-with-a-long-name-cut -P -F '#{pane_id}'); tmux-agent flag working --pane $p --quiet; done
tmux select-window -t :1
tmux-agent tmux-init; tmux-agent popup $C; sleep 2
PANEL=$(tmux list-panes -a -F '#{?#{@sidebar_client},#{pane_id},}' | tr -d '\n')
tmux send-keys -t $PANEL -l "$1"; sleep 1.5
tmux capture-pane -e -p -t $PANEL | sed -n 4,10p | cat -v
tmux kill-server
```
Before the fix (8f0f9a9):
```
== search a-lo
idle
idle
^[[1m^[[38;2;245;224;220m^[[48;2;49;50;68mM-bM-^VM-^L^[[38;2;205;214;244m M-bM-^WM-^O    extra-window-number-1-with-a-
^[[38;2;245;224;220mM-bM-^VM-^L^[[38;2;205;214;244m      long-name-cut
^[[38;2;245;224;220mM-bM-^VM-^L^[[38;2;205;214;244m                                  3s
^[[0m^[[38;2;49;50;68m ^[[39m M-bM-^WM-^O    extra-window-number-2-with-^[[4ma-lo^[[0mM-bM-^@M-&
^[[38;2;49;50;68m ^[[39m                                  3s


== search number-1
idle
idle
^[[1m^[[38;2;245;224;220m^[[48;2;49;50;68mM-bM-^VM-^L^[[38;2;205;214;244m M-bM-^WM-^O    extra-window-^[[4mnumber-1^[[0;1m^[[38;2;205;214;244m^[[48;2;49;50;68m-with-a-
^[[38;2;245;224;220mM-bM-^VM-^L^[[38;2;205;214;244m      long-name-cut
^[[38;2;245;224;220mM-bM-^VM-^L^[[38;2;205;214;244m                                  4s
^[[0m
```
After the fix:
```
== search a-lo
idle
idle
^[[1m^[[38;2;245;224;220m^[[48;2;49;50;68mM-bM-^VM-^L^[[38;2;205;214;244m M-bM-^WM-^O    extra-window-number-1-with-^[[4ma-^[[0;1m^[[38;2;205;214;244m^[[48;2;49;50;68m
^[[38;2;245;224;220mM-bM-^VM-^L^[[38;2;205;214;244m      ^[[4mlo^[[0;1m^[[38;2;205;214;244m^[[48;2;49;50;68mng-name-cut
^[[38;2;245;224;220mM-bM-^VM-^L^[[38;2;205;214;244m                                  3s
^[[0m^[[38;2;49;50;68m ^[[39m M-bM-^WM-^O    extra-window-number-2-with-^[[4ma-lo^[[0mM-bM-^@M-&
^[[38;2;49;50;68m ^[[39m                                  3s


== search number-1
idle
idle
^[[1m^[[38;2;245;224;220m^[[48;2;49;50;68mM-bM-^VM-^L^[[38;2;205;214;244m M-bM-^WM-^O    extra-window-^[[4mnumber-1^[[0;1m^[[38;2;205;214;244m^[[48;2;49;50;68m-with-a-
^[[38;2;245;224;220mM-bM-^VM-^L^[[38;2;205;214;244m      long-name-cut
^[[38;2;245;224;220mM-bM-^VM-^L^[[38;2;205;214;244m                                  4s
^[[0m
```
The fix finds the matches on the whole name, then underlines each part
on the line it falls on; both open-row layouts (a window with an agent,
and one without) go through it. Same fault elsewhere: the labels lines
are not searched, so no match is underlined in them, cut or open. Test
`TestOpenRowUnderlinesAcrossBreak`, before the fix:
```
$ go test ./tmux-agent/internal/attention/ -run TestOpenRowUnderlinesAcrossBreak
--- FAIL: TestOpenRowUnderlinesAcrossBreak (0.00s)
    pick_test.go:483: open row underlines "", want "hotkey-leaves":
        " ●\x1b[0m \x1b[38;2;217;119;87m✳\x1b[0m  a-popup-closed-by-its-hotkey-\x1b[0m\n      leaves-the-client\x1b[0m\n                                 \x1b[0m  3m\x1b[0m"
    pick_test.go:483: open row underlines "", want "window-name-for-a-shell":
        "      a-very-long-window-name-for-a-\n      shell-here\n                                    zsh\x1b[0m"
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/attention	0.004s
FAIL
```

#### Review 5 of ebba52d, findings 1 and 2

The user, asked on 2026-11-17 for each finding (both reproduced), chose
"Fix it (Recommended)" for both. The scripts sit in a `mktemp -d`
folder beside three builds: `main`, `branch` (ebba52d) and `fixed`;
each takes the build to run. `common.sh`:
```
set -u
D=$(cd "$(dirname "$0")" && pwd)
export HOME=$D/home XDG_STATE_HOME=$D/state XDG_RUNTIME_DIR=$D/run XDG_CONFIG_HOME=$D/home/.config PATH=$D/bin:/usr/bin:/bin
unset TMUX TMUX_PANE
rm -f $D/bin/tmux-agent; cp $D/tmux-agent-$1 $D/bin/tmux-agent
rm -rf $D/state/* $D/run/*
tmux -S $D/sock -f $D/tmux.conf new-session -d -s proj-a -n w-one -x 120 -y 30
export TMUX=$D/sock,$(tmux -S $D/sock display -p '#{pid}'),0
[ "$(tmux display -p '#{socket_path}')" = "$D/sock" ] || exit 1
(script -qfc "tmux -S $D/sock attach -t proj-a" /dev/null </dev/null >/dev/null 2>&1 &); sleep 1
C=$(tmux list-clients -F '#{client_name}')
```

Finding 1: `tmux-agent pick` typed in a plain shell window starts on the
fold, and each Up's cursor went back to the fold. Outside a panel the
keys write no `.want`, and place kept its own note of the fold row
there, whose window is always the client's. The fix: place uses `.want`
only in a panel. `f1.sh`:
```
. "$(dirname "$0")/common.sh"
for n in a-very-long-window-name-for-agent-one short-one another-very-long-window-name-here; do p=$(tmux new-window -n $n -P -F '#{pane_id}'); tmux-agent flag working --pane $p --quiet; done
s=$(tmux new-window -n w-shell -P -F '#{pane_id}'); sleep 0.5
tmux send-keys -t $s 'tmux-agent pick' Enter; sleep 2
for i in 1 2 3; do tmux send-keys -t $s Up; sleep 1.5; echo "-- after Up $i: client on window $(tmux list-clients -F '#{window_index}')"; tmux capture-pane -p -t $s | grep -v '^ *$' | tail -6; done
tmux kill-server
```
Before (`bash f1.sh main`, then `bash f1.sh branch`):
```
===== finding 1, main
idle
idle
idle
-- after Up 1: client on window 3
 search
 proj-a
  ●    a-very-long-window-name-for-agent-one                                0s
  ●    short-one                                                            0s
▌ ●    another-very-long-window-name-here                                   0s
    2 more windows
-- after Up 2: client on window 2
 search
 proj-a
  ●    a-very-long-window-name-for-agent-one                                0s
▌ ●    short-one                                                            0s
  ●    another-very-long-window-name-here                                   0s
    2 more windows
-- after Up 3: client on window 1
 search
 proj-a
▌ ●    a-very-long-window-name-for-agent-one                                0s
  ●    short-one                                                            0s
  ●    another-very-long-window-name-here                                   0s
    2 more windows
===== finding 1, branch
idle
idle
idle
-- after Up 1: client on window 3
 search
 proj-a
  ●    a-very-long-window-name-for-agent-one                                3s
  ●    short-one                                                            3s
  ●    another-very-long-window-name-here                                   3s
▌   2 more windows
-- after Up 2: client on window 3
 search
 proj-a
  ●    a-very-long-window-name-for-agent-one                                4s
  ●    short-one                                                            4s
  ●    another-very-long-window-name-here                                   4s
▌   2 more windows
-- after Up 3: client on window 3
 search
 proj-a
  ●    a-very-long-window-name-for-agent-one                                6s
  ●    short-one                                                            6s
  ●    another-very-long-window-name-here                                   6s
▌   2 more windows
```
After (`bash f1.sh fixed`):
```
===== finding 1, fixed
idle
idle
idle
-- after Up 1: client on window 3
 search
 proj-a
  ●    a-very-long-window-name-for-agent-one                                3s
  ●    short-one                                                            3s
▌ ●    another-very-long-window-name-here                                   3s
    2 more windows
-- after Up 2: client on window 2
 search
 proj-a
  ●    a-very-long-window-name-for-agent-one                                4s
▌ ●    short-one                                                            4s
  ●    another-very-long-window-name-here                                   4s
    2 more windows
-- after Up 3: client on window 1
 search
 proj-a
▌ ●    a-very-long-window-name-for-agent-one                                6s
  ●    short-one                                                            6s
  ●    another-very-long-window-name-here                                   6s
    2 more windows
```
Test `TestPickMovesFromFold`, before the fix. Its first version passed
on the unfixed code: it pressed Up before the picker's start had placed
its cursor, and checked the cursor only as it reached the row; it now
waits a second before the first key and checks the cursor a second
after each move:
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run '^TestPickMovesFromFold$' 2>&1 | grep -v "^        20" | sed '/^ *$/d'
--- FAIL: TestPickMovesFromFold (2.46s)
    picker_test.go:256: the cursor left agent-two; client on agent-two:
          [tree] state                                                                                                    tab ⇄
         search
         proj-a
          ●    agent-one                                                                                                    1s
          ●    agent-two                                                                                                    1s
        ▌   2 more windows
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	2.479s
FAIL
```

Finding 2: with the panel's cursor on an agent pane's row in a window
where another pane has the focus, the go took the client to the window,
not the pane, so `@sidebar_last` kept the other pane and the README's
`prefix |` split it. The fix: a pane's key goes to that pane, named as
main named it (`=session:index.pane` in a split window), read under the
lock just before the go. `f2.sh` (the tmux.conf holds the README's
`bind |` line):
```
. "$(dirname "$0")/common.sh"
a=$(tmux new-window -n w-split -P -F '#{pane_id}'); b=$(tmux split-window -h -t $a -P -F '#{pane_id}'); tmux select-pane -t $a
tmux-agent flag working --pane $b --quiet
tmux select-window -t :0
tmux-agent tmux-init; tmux-agent popup $C; sleep 2
PANEL=$(tmux list-panes -a -F '#{?#{@sidebar_client},#{pane_id},}' | tr -d '\n')
tmux send-keys -t $PANEL Up; sleep 1.5
echo "agent pane $b, shell pane $a"
echo "client window: $(tmux list-clients -F '#{window_name}'), @sidebar_last: $(tmux show -wqv -t =proj-a:w-split @sidebar_last)"
tmux send-keys -K -c $C C-b '|'; sleep 1
tmux list-panes -t =proj-a:w-split -F '#{pane_id} #{pane_width} #{?@sidebar_client,panel,}'
tmux kill-server
```
Before:
```
===== finding 2, main
idle
agent pane %2, shell pane %1
client window: w-split, @sidebar_last: %2
%3 39 panel
%1 20 
%2 9 
%4 9 
===== finding 2, branch
idle
agent pane %2, shell pane %1
client window: w-split, @sidebar_last: %1
%3 39 panel
%1 10 
%4 9 
%2 19
```
After:
```
===== finding 2, fixed
idle
agent pane %2, shell pane %1
client window: w-split, @sidebar_last: %2
%3 39 panel
%1 20 
%2 9 
%4 9
```
The review's variant with both panes flagged, the cursor on the `.2`
row (`f2b.sh`, f2.sh with the shell pane flagged too and the cursor's
row printed):
```
. "$(dirname "$0")/common.sh"
a=$(tmux new-window -n w-split -P -F '#{pane_id}'); b=$(tmux split-window -h -t $a -P -F '#{pane_id}'); tmux select-pane -t $a
tmux-agent flag working --pane $b --quiet; tmux-agent flag working --pane $a --quiet
tmux select-window -t :0
tmux-agent tmux-init; tmux-agent popup $C; sleep 2
PANEL=$(tmux list-panes -a -F '#{?#{@sidebar_client},#{pane_id},}' | tr -d '\n')
tmux send-keys -t $PANEL Up; sleep 1.5; tmux capture-pane -p -t $PANEL | grep "▌"
echo "agent pane $b, shell pane $a"
echo "client window: $(tmux list-clients -F '#{window_name}'), @sidebar_last: $(tmux show -wqv -t =proj-a:w-split @sidebar_last)"
tmux send-keys -K -c $C C-b '|'; sleep 1
tmux list-panes -t =proj-a:w-split -F '#{pane_id} #{pane_width} #{?@sidebar_client,panel,}'
tmux kill-server
```
```
===== two agents, main
▌   ● .1    sh                     4s
agent pane %2, shell pane %1
client window: w-split, @sidebar_last: %2
%3 39 panel
%1 20 
%2 9 
%4 9 
===== two agents, branch
▌   ● .2    sh                     4s
agent pane %2, shell pane %1
client window: w-split, @sidebar_last: %1
%3 39 panel
%1 10 
%4 9 
%2 19 
===== two agents, fixed
▌   ● .2    sh                     3s
agent pane %2, shell pane %1
client window: w-split, @sidebar_last: %2
%3 39 panel
%1 20 
%2 9 
%4 9
```
(main labels %2's row `.1`: the panel took index 1, the backlog entry
on pane numbers.) Test `TestPanelGoesToRowPane`, before the fix:
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run '^TestPanelGoesToRowPane$' 2>&1 | grep -v "^        20" | sed '/^ *$/d'
--- FAIL: TestPanelGoesToRowPane (5.40s)
    sidebar_test.go:888: timed out after 5s waiting for the client on %2
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	5.414s
FAIL
```
Main's `TestPanelTracesMoves` checks the trace names the go's target as
`=proj-a:1`; a first fix that passed the pane's id failed it, so the go
names its target as main did. The test is unchanged.

#### Review 6 of 36393ed, finding 1: a plain window's row took its first pane

With the panel's cursor on the row of a window with two panes and no
agent (in an opened fold), its right pane focused, the go selected the
left pane, so `@sidebar_last` named it and the README's `prefix |`
split it. Such a row was keyed by the window's first pane, and a pane's
key selects that pane since the review-5 fix. The user, asked on
2026-11-17 (it reproduced), chose "Fix it (Recommended)". The fix: a
window's row with no agent and no flag is keyed by its window, so the go
keeps the window's focused pane, as on main. The scripts sit in a
`mktemp -d` folder beside three builds, `main`, `branch` (36393ed) and
`fixed`; `bash f.sh <build>`. `common.sh` (the tmux.conf holds the
README's `bind |` line):
```
set -u
D=$(cd "$(dirname "$0")" && pwd)
export HOME=$D/home XDG_STATE_HOME=$D/state XDG_RUNTIME_DIR=$D/run XDG_CONFIG_HOME=$D/home/.config PATH=$D/bin:/usr/bin:/bin
unset TMUX TMUX_PANE
rm -f $D/bin/tmux-agent; cp $D/tmux-agent-$1 $D/bin/tmux-agent
rm -rf $D/state/* $D/run/*
tmux -S $D/sock -f $D/tmux.conf new-session -d -s proj-a -n w-one -x 120 -y 30
export TMUX=$D/sock,$(tmux -S $D/sock display -p '#{pid}'),0
[ "$(tmux display -p '#{socket_path}')" = "$D/sock" ] || exit 1
(script -qfc "tmux -S $D/sock attach -t proj-a" /dev/null </dev/null >/dev/null 2>&1 &); sleep 1
C=$(tmux list-clients -F '#{client_name}')
```
`f.sh`:
```
. "$(dirname "$0")/common.sh"
p=$(tmux new-window -n agent-one -P -F '#{pane_id}'); tmux-agent flag working --pane $p --quiet
a=$(tmux new-window -n w-two -P -F '#{pane_id}'); b=$(tmux split-window -h -t $a -P -F '#{pane_id}'); tmux select-pane -t $b
echo "w-two: left pane $a, right pane $b (focused before)"
tmux select-window -t =proj-a:agent-one
tmux-agent tmux-init; tmux-agent popup $C; sleep 2
P=$(tmux list-panes -a -F '#{?#{@sidebar_client},#{pane_id},}' | tr -d '\n')
tmux send-keys -t $P Down; sleep 1.5; tmux send-keys -t $P Enter; sleep 2
tmux send-keys -t $P Down Down; sleep 2
tmux capture-pane -p -t $P | grep "▌"
echo "client window: $(tmux list-clients -F '#{window_name}'), @sidebar_last: $(tmux show -wqv -t =proj-a:w-two @sidebar_last)"
tmux send-keys -K -c $C C-b '|'; sleep 1
tmux list-panes -t =proj-a:w-two -F '#{pane_id} #{pane_width} #{?@sidebar_client,panel,}'
tmux kill-server
```
Before (`main`, then `branch`; `idle` lines left out):
```
===== main
w-two: left pane %2, right pane %3 (focused before)
▌      w-two                         sh
client window: w-two, @sidebar_last: %3
%4 39 panel
%2 20 
%3 9 
%5 9 
===== branch
w-two: left pane %2, right pane %3 (focused before)
▌      w-two                         sh
client window: w-two, @sidebar_last: %2
%4 39 panel
%2 10 
%5 9 
%3 19 
```
After:
```
===== fixed
w-two: left pane %2, right pane %3 (focused before)
▌      w-two                         sh
client window: w-two, @sidebar_last: %3
%4 39 panel
%2 20 
%3 9 
%5 9 
```
Same fault elsewhere: the other rows keyed by a pane stand for that
pane (an agent's row, a split window's pane row, a task's row); a split
window's header is keyed by its window. Test
`TestPanelKeepsFocusedPaneOfPlainWindow`, before the fix:
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run '^TestPanelKeepsFocusedPaneOfPlainWindow$' 2>&1 | grep -v "^        20" | sed '/^ *$/d'
--- FAIL: TestPanelKeepsFocusedPaneOfPlainWindow (5.45s)
    sidebar_test.go:914: timed out after 5s waiting for the client on w-two's %3
    sidebar_test.go:129: tmux-agent log (<nil>):
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	5.467s
FAIL
```

#### Review 7 of 7f6e9fe, finding 1: `pick CLIENT` outside the panel lost its moves

Outside the panel, a move onto a session's fold, or onto a pane's row of
a split window, went back to the row of the client's window: the
review-5 fix made place ignore `.want` outside a panel, and the move's
reload, which opens the row, runs place. The user, asked on 2026-11-17
(both cases reproduced), chose "Fix it (Recommended)". The fix: outside
a panel each key goes itself, then writes its row's key to `.want` and
`.gone`; place reads them as in a panel (the review-5 special case is
gone; its test, `TestPickMovesFromFold`, still passes). The scripts sit
in a `mktemp -d` folder beside three builds, `main`, `branch` (7f6e9fe)
and `fixed`; `bash a.sh <build>`, `bash b.sh <build>`. `common.sh`:
```
set -u
D=$(cd "$(dirname "$0")" && pwd)
export HOME=$D/home XDG_STATE_HOME=$D/state XDG_RUNTIME_DIR=$D/run XDG_CONFIG_HOME=$D/home/.config PATH=$D/bin:/usr/bin:/bin
unset TMUX TMUX_PANE
rm -f $D/bin/tmux-agent; cp $D/tmux-agent-$1 $D/bin/tmux-agent
rm -rf $D/state/* $D/run/*
tmux -S $D/sock -f $D/tmux.conf new-session -d -s proj-a -n w-one -x 120 -y 30
export TMUX=$D/sock,$(tmux -S $D/sock display -p '#{pid}'),0
[ "$(tmux display -p '#{socket_path}')" = "$D/sock" ] || exit 1
(script -qfc "tmux -S $D/sock attach -t proj-a" /dev/null </dev/null >/dev/null 2>&1 &); sleep 1
C=$(tmux list-clients -F '#{client_name}')
W=$(tmux display -p -t =proj-a:w-one '#{pane_id}')
key() { tmux send-keys -t $W "$1"; sleep 1.5; echo "== $2"; tmux capture-pane -p -t $W | grep -v '^ *$' | sed 's/  */ /g' | tail -5; }
```
`a.sh`:
```
. "$(dirname "$0")/common.sh"
for n in agent-one agent-two; do p=$(tmux new-window -d -n $n -P -F '#{pane_id}'); tmux-agent flag working --pane $p --quiet; done
tmux new-window -d -n plain-two; tmux-agent tmux-init
tmux send-keys -t $W "tmux-agent pick $C" Enter; sleep 2
key Up up; key Down "down onto the fold"; key Enter enter
tmux kill-server
```
`b.sh`:
```
. "$(dirname "$0")/common.sh"
a=$(tmux new-window -d -n w-split -P -F '#{pane_id}'); b=$(tmux split-window -d -t $a -P -F '#{pane_id}')
tmux-agent flag working --pane $a --quiet; tmux-agent flag needs-you --pane $b --quiet; tmux-agent tmux-init
tmux send-keys -t $W "tmux-agent pick $C" Enter; sleep 2
key Up up; key Up up; tmux send-keys -t $W Enter; sleep 1.5
echo "client: $(tmux list-clients -F '#{window_name} pane #{pane_id}') (a=$a b=$b)"
tmux kill-server
```
Before (`idle` lines left out):
```
######## case a, main
== up
 search
 proj-a
 ● agent-one 0s
▌ ● agent-two 0s
 2 more windows
== down onto the fold
 search
 proj-a
 ● agent-one 0s
 ● agent-two 0s
▌ 2 more windows
== enter
 proj-a
 ● agent-one 5s
▌ ● agent-two 5s
 w-one tmux-agent
 plain-two sh
######## case a, branch
== up
 search
 proj-a
 ● agent-one 2s
▌ ● agent-two 2s
 2 more windows
== down onto the fold
 search
 proj-a
 ● agent-one 3s
▌ ● agent-two 3s
 2 more windows
== enter
-sh-5.3$ tmux-agent pick /dev/pts/17
-sh-5.3$
######## case b, main
== up
 proj-a
 ? w-split
 ● .0 sh 0s
▌ ? .1 sh 0s
 1 more window
== up
 proj-a
 ? w-split
▌ ● .0 sh 0s
 ? .1 sh 0s
 1 more window
client: w-split pane %1 (a=%1 b=%2)
######## case b, branch
== up
 proj-a
▌ ? w-split
 ● .0 sh 2s
 ? .1 sh 2s
 1 more window
== up
 proj-a
▌ ? w-split
 ● .0 sh 2s
 ? .1 sh 2s
 1 more window
client: w-split pane %2 (a=%1 b=%2)
```
After:
```
######## case a, fixed
== up
 search
 proj-a
 ● agent-one 2s
▌ ● agent-two 2s
 2 more windows
== down onto the fold
 search
 proj-a
 ● agent-one 4s
 ● agent-two 4s
▌ 2 more windows
== enter
 proj-a
 ● agent-one 6s
▌ ● agent-two 6s
 w-one tmux-agent
 plain-two sh
######## case b, fixed
== up
 proj-a
 ? w-split
 ● .0 sh 2s
▌ ? .1 sh 2s
 1 more window
== up
 proj-a
 ? w-split
▌ ● .0 sh 4s
 ? .1 sh 4s
 1 more window
client: w-split pane %1 (a=%1 b=%2)
```
Tests `TestPickStaysOnFold` and `TestPickReachesPaneRow`, before the fix:
```
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run '^TestPickStaysOnFold$' 2>&1 | grep -v "^        20" | sed '/^ *$/d'
--- FAIL: TestPickStaysOnFold (3.60s)
    picker_test.go:311: the cursor left more windows:
          [tree] state                                                                                                    tab ⇄
         search
         proj-a
          ●    agent-one                                                                                                    2s
        ▌ ●    agent-two                                                                                                    2s
            3 more windows
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	3.617s
FAIL
$ go test -race -count=1 ./tmux-agent/internal/cli/ -run '^TestPickReachesPaneRow$' 2>&1 | grep -v "^        20" | sed '/^ *$/d'
--- FAIL: TestPickReachesPaneRow (2.53s)
    picker_test.go:329: the cursor left ? .1:
          [tree] state                                                                                                    tab ⇄
         search
         proj-a
        ▌ ?    w-split
            ● .0    sh                                                                                                      2s
            ? .1    sh                                                                                                      2s
            2 more windows
FAIL
FAIL	github.com/influento/tmux-plugins/tmux-agent/internal/cli	2.558s
FAIL
```
