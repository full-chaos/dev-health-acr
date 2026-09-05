#!/usr/bin/env bash
# Proves scripts/docs/verify-mermaid.sh actually renders every real fenced
# mermaid block under docs/**, AND that it fails on a diagram mermaid
# itself rejects -- not just that the current tree happens to pass it.
#
# Needs a real mmdc (and a real Chromium) available: run this after the
# same `npm install` (or pinned npx) the render check itself uses. Not
# part of the offline-only scripts/docs/verify.sh suite for that reason.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"

# Positive: every real fenced mermaid block under docs/** must render.
"$root/scripts/docs/verify-mermaid.sh"

# Negative control: a deliberately broken block (an unquoted flowchart edge
# label containing a literal paren -- the same shape fixed in
# context-fabric-architecture-diagrams.md) must fail the check, naming the
# fixture file and block.
fixture="$root/testdata/docs-invalid-mermaid/broken-edge-label"
set +e
output="$("$root/scripts/docs/verify-mermaid.sh" --root "$fixture" 2>&1)"
exit_code=$?
set -e

if [ "$exit_code" -eq 0 ]; then
  printf 'expected the broken-edge-label fixture to fail verify-mermaid.sh, but it exited 0\n%s\n' \
    "$output" >&2
  exit 1
fi

if ! grep -qF 'FAIL: docs/broken.md block #1' <<<"$output"; then
  printf 'verify-mermaid.sh failed on the broken fixture but not with the expected FAIL line\n%s\n' \
    "$output" >&2
  exit 1
fi

printf 'PASS: verify-mermaid.sh renders every real block and rejects the broken fixture\n'
