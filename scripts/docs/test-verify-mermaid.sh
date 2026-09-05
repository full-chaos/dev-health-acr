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

# Second negative control: a MUST-NOT-EXTRACT case, not a must-fail one. A
# tab-indented ```mermaid fence is not a fence at all under CommonMark (a
# leading tab is 4+ columns of indentation, so the line is an ordinary
# indented code block to any real renderer) -- extracting and validating it
# anyway would be a false-positive fence match. The companion valid.md block
# must still render fine, and the tab file must never be named at all.
tab_fixture="$root/testdata/docs-invalid-mermaid/tab-indented-not-a-fence"
set +e
tab_output="$("$root/scripts/docs/verify-mermaid.sh" --root "$tab_fixture" 2>&1)"
tab_exit_code=$?
set -e

if [ "$tab_exit_code" -ne 0 ]; then
  printf 'expected the tab-indented-not-a-fence fixture to PASS verify-mermaid.sh (the tab block must be ignored, not fail), got exit %s\n%s\n' \
    "$tab_exit_code" "$tab_output" >&2
  exit 1
fi

if grep -qF 'tab-indented.md' <<<"$tab_output"; then
  printf 'verify-mermaid.sh mentioned tab-indented.md -- the tab-indented fence was extracted, which is exactly the false-positive this fixture exists to catch\n%s\n' \
    "$tab_output" >&2
  exit 1
fi

if ! grep -qF 'ok: docs/valid.md block #1' <<<"$tab_output"; then
  printf 'the companion valid.md block did not render in the tab-indented-not-a-fence fixture\n%s\n' \
    "$tab_output" >&2
  exit 1
fi

printf 'PASS: verify-mermaid.sh renders every real block, rejects the broken fixture, and ignores a tab-indented non-fence\n'
