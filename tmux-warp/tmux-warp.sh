#!/usr/bin/env bash
set -euo pipefail

# Shell wrapper for tmux-warp.
# 1. Creates a temp file
# 2. Uses tmux command-prompt to capture search query (char or word) into it
# 3. Invokes the Go binary which polls the file and takes over

tmp_file="$(mktemp)"
tmux command-prompt -p 'warp:' "run-shell \"printf '%1' >> ${tmp_file}\""

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

"${binary}" "${tmp_file}"
