#!/usr/bin/env bash
set -euo pipefail

# Usage: check-vendored-skills.sh <context-fabric-agents-root> [vendored-dir]
#
# <context-fabric-agents-root> is a checkout of the commit
# ci/context-fabric-agents.pin names. Every skill vendored under
# internal/mcp/skills must equal skills/<name> there, byte for byte, with no
# file added or missing on either side.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cfa_root="${1:?usage: check-vendored-skills.sh <context-fabric-agents-root>}"
vendored="${2:-$repo_root/internal/mcp/skills}"

status=0
found=0
for dir in "$vendored"/*/; do
  [ -d "$dir" ] || continue
  name="$(basename "$dir")"
  found=1
  if [ ! -d "$cfa_root/skills/$name" ]; then
    printf 'the context-fabric-agents tree has no skills/%s\n' "$name" >&2
    status=1
    continue
  fi
  if ! diff -ru "$cfa_root/skills/$name" "$vendored/$name" >&2; then
    printf 'vendored skill %s differs from the pinned context-fabric-agents copy\n' "$name" >&2
    status=1
  fi
done
if [ "$found" -eq 0 ]; then
  printf 'no vendored skill under %s\n' "$vendored" >&2
  exit 1
fi
if [ "$status" -ne 0 ]; then
  printf 'Re-vendor from the commit in ci/context-fabric-agents.pin, or move the pin, the rev and the digests in internal/mcp/skills_vendored.go with the copy in one change.\n' >&2
fi
exit "$status"
