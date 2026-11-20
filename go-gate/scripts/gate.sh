#!/usr/bin/env bash
# shellcheck disable=SC2329  # tidy, lint, fmt, only_tests and tests are called through run
# go-gate: go.mod against the imports, the approved modules, golangci-lint (lint
# and format), the gate's own rules, the code no program reaches (deadcode), the
# tests with -race. Modes: none (the whole project), --local (what the Stop hook
# runs: format on the branch's changed files, the tests from Go's test cache),
# --fix (the linters' fixes and the formatters, nothing else), --legacy (the
# files that fail lint, format or the gate's own rules, for install's
# go-gate-legacy.txt). Every check runs even when another fails.
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
FAIL=0
MODE="${1:-}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# A go.work above the worktree would join the main checkout's modules into the
# build: off, unless git tracks one here.
if git ls-files --error-unmatch go.work >/dev/null 2>&1; then export GOWORK="$PWD/go.work"; else export GOWORK=off; fi
# A non-empty GOFLAGS replaces the one `go env -w` wrote for every project, and
# -mod=readonly stops a build from writing go.mod.
if [ -f vendor/modules.txt ]; then GOFLAGS=-mod=vendor; else GOFLAGS=-mod=readonly; fi
export GOFLAGS GO_GATE_GOFLAGS="$GOFLAGS"
# When go.mod asks for a newer Go than the installed one, go switches for the
# project's commands: the gate's tools build with the same Go.
SELECTED=$(go env GOVERSION 2>/dev/null) || SELECTED=""
[ -z "$SELECTED" ] || [ "$SELECTED" = "$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null)" ] || export GOTOOLCHAIN="$SELECTED"

# The gate's tools, each in its own module under go-gate/ (outside ./...): the
# path of the cached build, which then runs with the project's settings.
tool() {
  local bin
  bin=$(GOWORK=off GOFLAGS=-mod=readonly go -C "$1" tool -n "$2" 2>"$TMP/build.err") && { echo "$bin"; return; }
  cat "$TMP/build.err" >&2
  echo "go-gate could not build $2 from $1 (a first build downloads its modules)" >&2
  return 1
}
LINT_BIN=$(tool go-gate/golangci golangci-lint) || exit 1
CHECK_BIN=$(tool go-gate check) || exit 1
DEADCODE_BIN=$(tool go-gate deadcode) || exit 1
check() { "$CHECK_BIN" "$PWD" "$@"; }
# shellcheck source-path=SCRIPTDIR/../../../shared source=test-lock.sh
. go-gate/scripts/test-lock.sh
run() { "$@" || FAIL=1; }
# A tool that prints a summary when it passes: its output only when it fails.
quiet() { local out; out=$("$@" 2>&1) && return; printf '%s\n' "$out"; return 1; }
LINT_FLAGS=(--output.text.print-issued-lines=false --show-stats=false)
# only-tests-use: deadcode from each main package, tests not counted; check names
# its arguments and reads what it reports, and adds the packages only tests use.
# Without a program, deadcode does not run.
only_tests() {
  local args out=""
  args=$(check only-tests-args) || { printf '%s\n' "$args"; return 1; }
  if [ -n "$args" ]; then
    mapfile -t args <<<"$args"
    out=$("$DEADCODE_BIN" "${args[@]}" 2>&1) || { printf '%s\n' "$out"; echo "deadcode failed"; return 1; }
  fi
  check only-tests <<<"$out"
}

default_branch() {
  git symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null && return
  local b; for b in main master; do git show-ref -q --verify "refs/heads/$b" && { echo "$b"; return; }; done
  echo HEAD
}

CONFIG="$TMP/golangci.yml"
check config > "$CONFIG" || exit 1
# A go list that fails must not leave lint and tests with nothing to check.
PKGS=()
if PKG_LIST=$(check pkgs 2>&1); then
  [ -z "$PKG_LIST" ] || mapfile -t PKGS <<<"$PKG_LIST"
else
  printf '%s\n' "$PKG_LIST"; echo "go-gate could not list the packages: fix go.mod or the error above."; FAIL=1
fi

if [ "$MODE" = "--fix" ]; then
  [ ${#PKGS[@]} -gt 0 ] || { echo "no Go packages"; exit 0; }
  "$LINT_BIN" run -c "$CONFIG" "${LINT_FLAGS[@]}" --fix ./...; l=$?
  "$LINT_BIN" fmt -c "$CONFIG" ./...; f=$?
  exit $((l > f ? l : f))
fi

if [ "$MODE" = "--legacy" ]; then
  { [ ${#PKGS[@]} -eq 0 ] || "$LINT_BIN" run -c "$CONFIG" "${LINT_FLAGS[@]}" ./...; check rules; only_tests; } 2>&1 | check legacy
  exit 0
fi

FILES=()
if [ "$MODE" = "--local" ]; then
  RANGE=$(git merge-base HEAD "$(default_branch)" 2>/dev/null || echo HEAD)
  mapfile -t CHANGED < <({ git diff --relative --name-only --diff-filter=ACMRD "$RANGE" -- .; git ls-files --others --exclude-standard -- .; } | sort -u | check excluded)
  printf '%s\n' "${CHANGED[@]}" \
    | grep -qE '\.go$|(^|/)testdata/|^(go\.mod|go\.sum|go\.work|go\.work\.sum|vendor/.*|go-gate/.*|go-gate-legacy\.txt|\.claude/workshop\.conf)$' \
    || { echo "no Go or config changes"; exit 0; }
  mapfile -t LEGACY < <(check legacy-files)
  for f in "${CHANGED[@]}"; do
    [[ "$f" == *.go && -e "$f" ]] && ! printf '%s\n' "${LEGACY[@]}" | grep -qxF -- "$f" && FILES+=("$f")
  done
  # The format check runs on the changed files alone, so the lint runs without the formatters.
  check config --local > "$TMP/golangci-local.yml" || exit 1
fi

tidy() {
  local out
  out=$(go mod tidy -diff 2>&1) && return
  printf '%s\n' "$out"
  echo "go.mod or go.sum does not match the imports: run go mod tidy."
  return 1
}
lint() {
  [ ${#PKGS[@]} -gt 0 ] || return 0
  local cfg="$CONFIG" out
  [ "$MODE" != "--local" ] || cfg="$TMP/golangci-local.yml"
  out=$("$LINT_BIN" run -c "$cfg" "${LINT_FLAGS[@]}" ./... 2>&1) && return
  printf '%s\n' "$out"
  grep -qE '\((gofumpt|goimports)\)$' <<<"$out" && echo "Run bash go-gate/scripts/gate.sh --fix to format these files."
  return 1
}
fmt() {
  [ ${#FILES[@]} -gt 0 ] || return 0
  local out
  out=$("$LINT_BIN" fmt -c "$CONFIG" --diff "${FILES[@]}" 2>&1)
  [ -n "$out" ] || return 0
  printf '%s\n' "$out"
  echo "Run bash go-gate/scripts/gate.sh --fix to format these files."
  return 1
}
tests() {
  [ ${#PKGS[@]} -gt 0 ] || { echo "no Go packages: no tests ran"; return 0; }
  local args=(-race) rc
  # -race needs cgo, which is off without a C compiler.
  if [ "$(go env CGO_ENABLED)" != 1 ]; then
    args=(); echo "go-gate: cgo is off, so the tests run without -race. Install a C compiler (gcc) to turn it on."
  fi
  # The full run shuffles the order; a shuffled run is never cached, so --local
  # runs from Go's test cache: only the packages a change reaches run again.
  [ "$MODE" = "--local" ] || args+=(-shuffle=on)
  # -p: test binaries at once; -parallel: the t.Parallel tests of one binary.
  local workers trc
  workers=$(test_workers)
  [ -z "$workers" ] || args+=(-p "$workers" -parallel "$workers")
  # shellcheck disable=SC2016  # the inner shell's $0 and $@: go test's output into the file
  test_locked go-gate "$MODE" sh -c 'exec "$@" > "$0" 2>&1' "$TMP/test.json" go test "${args[@]}" -json "${PKGS[@]}"; trc=$?
  [ "$trc" -ne "$TEST_LOCK_TIMED_OUT" ] || return 1
  check tests < "$TMP/test.json"; rc=$?
  # go test can fail before any test event: a go.mod it cannot use, a bad flag.
  [ "$rc" -ne 0 ] || [ "$trc" -eq 0 ] || { echo "go test failed (exit $trc)"; rc=1; }
  # Workbench asks for each test's run time when it runs the project's checks.
  if [ -n "${WORKSHOP_TEST_REPORTS:-}" ]; then
    local sum
    sum=$(tool go-gate gotestsum) && "$sum" --format none --junitfile "$WORKSHOP_TEST_REPORTS/go-gate.xml" --raw-command -- cat "$TMP/test.json" >/dev/null 2>&1
  fi
  return "$rc"
}

run tidy
run quiet check deps
run lint
run fmt
run check rules
run only_tests
run tests
if [ -z "$MODE" ]; then
  mapfile -t LEGACY < <(check legacy-files)
  [ ${#LEGACY[@]} -eq 0 ] || echo "go-gate-legacy.txt: ${#LEGACY[@]} files the gate does not check yet. To bring one in, delete its line, then fix what the gate reports in it. The first line is the one to take next: ${LEGACY[0]}"
fi
exit $FAIL
