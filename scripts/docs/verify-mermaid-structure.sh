#!/usr/bin/env bash
# Fails when any mermaid block under docs/ has a different node or edge count than at BASE_REF.
# For text-only sweeps: a label edit passes, a dropped node or edge fails.
# Usage: scripts/docs/verify-mermaid-structure.sh BASE_REF
set -euo pipefail
cd "$(dirname "$0")/../.."
base="${1:?usage: verify-mermaid-structure.sh BASE_REF}"
tool=scripts/docs/mermaid-structure.py
before="$(python3 "$tool" "$base")"
after="$(python3 "$tool")"
if [ -z "$before" ] || [ -z "$after" ]; then
  printf 'FAIL: no mermaid block found; an empty comparison is not a pass\n' >&2
  exit 1
fi
if ! diff <(printf '%s\n' "$before") <(printf '%s\n' "$after"); then
  printf 'FAIL: mermaid node or edge counts differ from %s\n' "$base" >&2
  exit 1
fi
printf 'ok: %s mermaid block(s) keep their node and edge counts against %s\n' "$(printf '%s\n' "$after" | wc -l)" "$base"
