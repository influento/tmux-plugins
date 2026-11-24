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

# changed_files <range> [<pathspec>...]: the files under the current folder
# that changed since <range>, deleted ones too, and the files git does not
# track, relative to the current folder, each once.
changed_files() {
  local range="$1"
  shift
  { git diff --relative --name-only --diff-filter=ACMRD "$range" -- . "$@"; git ls-files --others --exclude-standard -- . "$@"; } | sort -u
}

# legacy_reminder <gate> [<path>...]: a full run's last line while the gate's
# legacy list holds the paths, in its order; nothing when it holds none.
legacy_reminder() {
  local gate="$1"
  shift
  [ $# -eq 0 ] || echo "$gate-legacy.txt: $# files the gate does not check yet. To bring one in, delete its line, then fix what the gate reports in it. The first line is the one to take next: $1"
}
