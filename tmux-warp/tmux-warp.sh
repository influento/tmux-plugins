#!/usr/bin/env bash
set -euo pipefail

# Shell wrapper for tmux-warp.
# 1. Clears any stale query option.
# 2. Uses tmux command-prompt to store the search query (char or word) into the
#    @warp_query tmux option. Storing via set-option (not run-shell) keeps query
#    characters like $ ; " ` literal -- they are never executed by a shell.
# 3. Invokes the Go binary, which reads the option and takes over.

tmux set-option -gu @warp_query 2>/dev/null || true
tmux command-prompt -p 'warp:' "set-option -g @warp_query '%1'"

# Resolve binary path relative to this script.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
binary="${script_dir}/tmux-warp"

# Fall back to PATH if not found next to script.
if [[ ! -x "${binary}" ]]; then
  binary="$(command -v tmux-warp 2>/dev/null || true)"
fi

if [[ -z "${binary}" ]]; then
  echo "tmux-warp: binary not found" >&2
  exit 1
fi

"${binary}"
