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
