# shellcheck shell=bash
# Sourced by each gate's gate.sh, its Stop hook and test-lock.sh, from the
# project root, as <gate>/scripts/lib.sh: the gate's install copies this file
# there. What every gate's bash reads the same way. The workbench and
# workshop CLIs source it from the workshop repository for default_branch,
# so a change here reaches them at once; it names nothing outside itself.

# conf_value <key>: the value of the key in .claude/workshop.conf, trimmed;
# the last line with the key wins. Nothing when the key or the file is
# missing. The format: AGENTS.md in the workshop repo, ".claude/workshop.conf".
conf_value() {
  local line k v=""
  if [ -f .claude/workshop.conf ]; then
    while IFS= read -r line || [ -n "$line" ]; do
      line="${line#"${line%%[![:space:]]*}"}"; line="${line%"${line##*[![:space:]]}"}"
      case "$line" in '#'*) continue ;; *=*) ;; *) continue ;; esac
      k=${line%%=*}; k="${k%"${k##*[![:space:]]}"}"
      [ "$k" = "$1" ] || continue
      v=${line#*=}; v="${v#"${v%%[![:space:]]*}"}"
    done < .claude/workshop.conf
  fi
  printf '%s' "$v"
}

# conf_count <key> [<default>]: the key's value when it is a positive integer,
# else the default, else nothing.
conf_count() {
  local v
  v=$(conf_value "$1")
  if [[ "$v" =~ ^[1-9][0-9]{0,8}$ ]]; then echo "$v"; elif [ -n "${2:-}" ]; then echo "$2"; fi
  return 0
}

# default_branch: the default branch of the repository in the current
# folder, found, never asked: origin's HEAD as a remote ref (origin/main),
# else main, else master; else HEAD.
default_branch() {
  git symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null && return
  local b; for b in main master; do git show-ref -q --verify "refs/heads/$b" && { echo "$b"; return; }; done
  echo HEAD
}

# Where the branch left the default branch: what --local compares against.
# Before the first commit, the empty tree: every file in the index is a change.
merge_base() {
  git rev-parse -q --verify HEAD >/dev/null || { git hash-object -t tree /dev/null; return; }
  git merge-base HEAD "$(default_branch)" 2>/dev/null || echo HEAD
}

# On the default branch, --local diffs from the commit that the gate's last
# passing run in this checkout saw, so a commit made on main is checked too.
# Why and the edge cases: ecosystems/CLAUDE.md in the workshop repo, "What
# --local diffs against".

# green_file <gate>: the record, <gate>-green under the checkout's own git dir:
# each worktree has its own.
green_file() { git rev-parse --git-path "$1-green" 2>/dev/null; }

# on_default_branch: HEAD is the default branch, or the repository has none to
# compare with.
on_default_branch() {
  local d
  d=$(default_branch)
  [ "$d" = HEAD ] || [ "$(git symbolic-ref -q --short HEAD 2>/dev/null)" = "${d#origin/}" ]
}

# green_commit <gate>: the recorded commit, when --local diffs from it: on the
# default branch, and in HEAD's history. Nothing for no record, or one that a
# reset or a rewrite left out of HEAD's history.
green_commit() {
  on_default_branch || return 0
  local f c=""
  f=$(green_file "$1") && [ -f "$f" ] || return 0
  read -r c < "$f" || true
  [[ "$c" =~ ^[0-9a-f]{40}([0-9a-f]{24})?$ ]] || return 0
  if git merge-base --is-ancestor "$c" HEAD 2>/dev/null; then echo "$c"; fi
}

# local_base <gate>: what --local diffs against: the recorded commit, else the
# merge base.
local_base() {
  local c
  c=$(green_commit "$1")
  if [ -n "$c" ]; then echo "$c"; else merge_base; fi
}

# record_green <gate> <commit>: a passing run records the commit it saw, HEAD
# when it started; none before the first commit.
record_green() {
  [ -n "${2:-}" ] || return 0
  local f
  f=$(green_file "$1") || return 0
  { printf '%s\n' "$2" > "$f.$$" && mv -f "$f.$$" "$f"; } 2>/dev/null || rm -f "$f.$$"
}

# seed_green <gate>: on the default branch with no record that --local can
# use, record the merge base, the range --local has without one. The Stop hook
# calls it at each stop: a session that commits before it stops leaves the
# hook nothing to run, and no run would ever pass to write the first record.
seed_green() {
  git rev-parse -q --verify HEAD >/dev/null 2>&1 && on_default_branch || return 0
  [ -z "$(green_commit "$1")" ] || return 0
  record_green "$1" "$(git rev-parse -q --verify "$(merge_base)^{commit}")"
}

# changed_files <range> [<pathspec>...]: the files under the current folder
# that changed since <range>, deleted ones too, and the files git does not
# track, relative to the current folder, each once. git quotes a name with a
# byte above 0x7f by default ("src/caf\303\251.ts"), which no -e test finds:
# core.quotePath=false prints it as it is.
changed_files() {
  local range="$1"
  shift
  {
    git -c core.quotePath=false diff --relative --name-only --diff-filter=ACMRD "$range" -- . "$@"
    git -c core.quotePath=false ls-files --others --exclude-standard -- . "$@"
  } | sort -u
}

# legacy_reminder <gate> [<path>...]: a full run's last line while the gate's
# legacy list holds the paths, in its order; nothing when it holds none.
legacy_reminder() {
  local gate="$1"
  shift
  [ $# -eq 0 ] || echo "$gate-legacy.txt: $# files the gate does not check yet. To bring one in, delete its line, then fix what the gate reports in it. The first line is the one to take next: $1"
}
