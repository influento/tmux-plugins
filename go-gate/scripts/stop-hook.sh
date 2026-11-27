#!/usr/bin/env bash
# The Stop hook of every gate: its install copies this file in as
# <gate>/scripts/stop-hook.sh, and the gate is the folder it sits in. What it
# does and why: ecosystems/CLAUDE.md in the workshop repo, "Stop hook".
set -uo pipefail
GATE=$(basename "$(dirname "$(dirname "$0")")")
# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "$0")/lib.sh"
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0
# Per gate: FINDING, the lines a failure prints, one of which the last block
# quotes; KEEP, the lines the repeat count compares; NORMALIZE, a sed -E script
# that drops from them what changes between two runs of the same failure; ALL,
# the command that prints all of the output.
case "$GATE" in
  ts-gate)
    FINDING='error|unused|FAIL'
    # A line that names a file, with or without :line:col: knip's items under its
    # section header, biome's unformatted files, a cycle's files. The hash keeps
    # them, so another unused export is another failure. Not a bare :line:col:
    # vitest's "Start at" clock is HH:MM:SS.
    KEEP="$FINDING|"'\.[[:alpha:]]+(:[0-9]+)*([[:space:]]|$)'
    # The tools print their run times (biome's "Checked … in", vitest's "Start at"
    # and "Duration") in lines that KEEP leaves out. A failed npm ci names its log
    # file by the time it started (_logs/<time>-debug-0.log).
    NORMALIZE='s#_logs/[^[:space:]]*-debug(-[0-9]+)?\.log#_logs/N-debug.log#'
    ALL='npm run ts-gate:local'
    ;;
  py-gate)
    FINDING='error|^[^ ]+:[0-9]+:|FAILED|DEP0|BROKEN'
    KEEP=$FINDING
    # pytest's run time at the end of its summary, the number of its temp folder
    # (pytest-N), an object's address.
    NORMALIZE='s/ in [0-9.]+s( \([0-9:]+\))?$//; s/pytest-[0-9]+/pytest-N/g; s/0x[0-9a-fA-F]+/0x/g'
    ALL='bash py-gate/scripts/gate.sh --local'
    ;;
  go-gate)
    FINDING='error|^[^ ]+:[0-9]+:|FAIL|no-test-ran|^diff |does not match|DATA RACE'
    KEEP=$FINDING
    # A run time, the shuffle seed, a goroutine's number, an address,
    # t.TempDir's numbered folder.
    NORMALIZE='s/ ?\([0-9.]+m?s\)//g; s/[[:space:]]+[0-9.]+m?s$//; s/-test\.shuffle [0-9]+/-test.shuffle N/; s/goroutine [0-9]+/goroutine N/g; s/0x[0-9a-fA-F]+/0x/g; s#(/Test[^/ ]*)[0-9]+/[0-9]+#\1N/N#g'
    ALL='bash go-gate/scripts/gate.sh --local'
    ;;
  *) echo "stop-hook.sh: no gate named $GATE" >&2; exit 1 ;;
esac

IN=$(cat)
# While the file that pause_file names exists, the project runs something that
# the load of a gate run would disturb: every stop passes, and the next stop
# after the file goes checks what changed meanwhile.
PAUSE=$(conf_value pause_file)
# shellcheck disable=SC2016,SC2088  # the conf's literal ~/ and $XDG_RUNTIME_DIR/
case "$PAUSE" in
  '~/'*) PAUSE=$HOME/${PAUSE#'~/'} ;;
  '$XDG_RUNTIME_DIR/'*) PAUSE=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/${PAUSE#'$XDG_RUNTIME_DIR/'} ;;
esac
[ -z "$PAUSE" ] || [ ! -e "$PAUSE" ] || exit 0
# On the default branch with no record of a passing run, the range --local has
# without one, so that a commit the session makes from now on is in it.
seed_green "$GATE"
# The checkouts this session worked in, each checked by its own copy of the
# gate: another session's work in this project is not this one's to fix.
# When worked-in.py cannot tell: this checkout.
HERE=$(pwd -P)
mapfile -t TOPS < <(printf '%s' "$IN" | python3 -I "$GATE/scripts/worked-in.py" "$GATE" 2>/dev/null || echo "$HERE")
OUT="" RED=0
for TOP in "${TOPS[@]}"; do
  # A folder whose files are as they were at its last passing run passes
  # again: the key is taken before the run, so a file that changes during it
  # makes the next stop run.
  KEY=$(cd "$TOP" && tree_key)
  [ -z "$KEY" ] || [ "$KEY" != "$(cd "$TOP" && tree_green "$GATE")" ] || continue
  RUN=$(cd "$TOP" && bash "$GATE/scripts/gate.sh" --local 2>&1) && { (cd "$TOP" && record_tree "$GATE" "$KEY"); continue; }
  RED=1
  [ "$TOP" = "$HERE" ] || RUN="== in $TOP =="$'\n'"$RUN"
  OUT+="${OUT:+$'\n'}$RUN"
done
[ "$RED" -eq 1 ] || exit 0
CAP=$(conf_count "$GATE.repeat_cap" 3)
MAX_LINES=$(conf_count "$GATE.output_lines" 80)

SID=$(printf '%s' "$IN" | sed -n 's/.*"session_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
SAME=1
if [ -n "$SID" ]; then
  MARK="${TMPDIR:-/tmp}/$GATE-stop-$SID"
  # Another order, or what NORMALIZE drops, must not make the same failures look new.
  # In the C locale each byte is a character: a path that is not UTF-8 matches
  # [^ ], and grep does not take the output for binary.
  FINDINGS=$(printf '%s\n' "$OUT" | LC_ALL=C grep -iE "$KEEP" | LC_ALL=C sed -E "$NORMALIZE" | LC_ALL=C sort || true)
  HASH=$(printf '%s' "${FINDINGS:-$OUT}" | sha256sum | cut -c1-16)
  read -r LAST N 2>/dev/null < "$MARK" || { LAST=""; N=0; }
  [ "$HASH" = "$LAST" ] && SAME=$((N + 1))
  printf '%s %s\n' "$HASH" "$SAME" > "$MARK"
fi
[ "$SAME" -le "$CAP" ] || exit 0

LINES=$(printf '%s\n' "$OUT" | wc -l)
if [ "$LINES" -gt "$MAX_LINES" ]; then
  OUT=$(printf '%s\n' "$OUT" | head -n "$MAX_LINES"; echo "… $((LINES - MAX_LINES)) more lines; run $ALL for all of it")
fi
if [ "$SAME" -eq "$CAP" ]; then
  # A message no FINDING names (the dependency check's) gets the output's first
  # line, not an empty quote.
  QUOTE=$(printf '%s\n' "$OUT" | LC_ALL=C grep -m1 -iE "$FINDING" || printf '%s\n' "$OUT" | LC_ALL=C grep -m1 -v '^== in .* ==$')
  printf 'The gate failed the same way %s times. Do not try again. Ask the user what to do, through the question prompt of your environment. Quote this line to the user:\n%s\nThe next stop is allowed.\n' \
    "$CAP" "$(printf '%s\n' "$QUOTE" | cut -c1-120)" >&2
fi
printf '%s\n' "$OUT" >&2
exit 2
