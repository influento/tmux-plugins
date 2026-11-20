# shellcheck shell=bash
# Sourced by each gate's gate.sh, from the project root, as
# <gate>/scripts/test-lock.sh: the gate's install copies this file there.
# One gate test run at a time on the machine, and the joint key test_workers.
# What and why: ecosystems/CLAUDE.md in the workshop repo, "Test load".

# How long --local, the Stop hook's run, waits for the lock: the hook's own
# timeout is 600 s, and a hook that the timeout kills cannot block the stop.
TEST_LOCK_WAIT_LOCAL=240
# flock's exit code when the wait runs out.
TEST_LOCK_TIMED_OUT=75

# The value of test_workers in .claude/workshop.conf when it is a positive
# integer; nothing otherwise, and the runner keeps its own default.
test_workers() {
  local line k v=""
  if [ -f .claude/workshop.conf ]; then
    while IFS= read -r line || [ -n "$line" ]; do
      line="${line#"${line%%[![:space:]]*}"}"; line="${line%"${line##*[![:space:]]}"}"
      case "$line" in '#'*) continue ;; *=*) ;; *) continue ;; esac
      k=${line%%=*}; k="${k%"${k##*[![:space:]]}"}"
      [ "$k" = test_workers ] || continue
      v=${line#*=}; v="${v#"${v%%[![:space:]]*}"}"
    done < .claude/workshop.conf
  fi
  if [[ "$v" =~ ^[1-9][0-9]{0,8}$ ]]; then echo "$v"; fi
  return 0
}

# test_locked <gate> <mode> <command> [<arg>...]: runs the command while it
# holds the machine's test lock, and returns its exit code. --local waits
# TEST_LOCK_WAIT_LOCAL seconds at most, then prints why and returns
# TEST_LOCK_TIMED_OUT; every other mode waits until the lock is free.
test_locked() {
  local gate="$1" mode="$2" lock holder rc
  shift 2
  # A test that runs a gate inside a gate's test run: the outer run holds the lock.
  [ -z "${WORKSHOP_TEST_LOCK_HELD:-}" ] || { "$@"; return; }
  lock=${WORKSHOP_TEST_LOCK:-${XDG_RUNTIME_DIR:-${TMPDIR:-/tmp}}/workshop-tests-$(id -u).lock}
  if ! command -v flock >/dev/null; then
    echo "$gate: flock is not installed, so this test run can overlap with another session's."
    "$@"; return
  fi
  if ! { : >> "$lock"; } 2>/dev/null; then
    echo "$gate: cannot write the test lock $lock, so this test run can overlap with another session's."
    "$@"; return
  fi
  local wait=()
  [ "$mode" != --local ] || wait=(-w "${WORKSHOP_TEST_LOCK_WAIT:-$TEST_LOCK_WAIT_LOCAL}")
  # --local names no holder: the Stop hook counts a repeat by the output, and
  # the holder changes while the same tests wait.
  holder=""
  [ "$mode" = --local ] || holder=$(head -n1 "$lock" 2>/dev/null)
  if ! flock -n "$lock" true; then
    echo "$gate: waiting for the test run in ${holder:-another session} to end: one test run at a time on this machine."
  fi
  # -o: the command and what it starts do not inherit the lock, so a process
  # that a test leaks cannot hold it after the run ends.
  # shellcheck disable=SC2016  # the inner shell's $0 and $@
  WORKSHOP_TEST_LOCK_HELD=1 flock -o "${wait[@]}" -E "$TEST_LOCK_TIMED_OUT" "$lock" \
    sh -c 'printf "%s\n" "$PWD" > "$0"; exec "$@"' "$lock" "$@"
  rc=$?
  if [ "$rc" -eq "$TEST_LOCK_TIMED_OUT" ] && [ "$mode" = --local ]; then
    echo "$gate: the tests did not run: another session's test run held this machine's test lock for ${wait[1]} s. Run the gate again when that run ends."
  fi
  return "$rc"
}
