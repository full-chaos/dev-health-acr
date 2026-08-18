#!/usr/bin/env bash
set -euo pipefail

# test-shard-closure.sh proves, at runtime, that the race matrix in
# .github/workflows/ci.yml actually covers the whole module: it reads the
# shard indices and the shard total straight out of the workflow, invokes
# scripts/ci/test-shard.sh exactly the way CI does for every index, and
# requires the union of those invocations to equal `go list ./...` with no
# package listed twice.
#
# This is deliberately separate from test-workflow-contract.sh. That script
# is a static check that runs in a Go-less job; this one needs a Go toolchain
# because only running the real partition against the real package list can
# show that no package escapes the suite. A static agreement check can prove
# the matrix is well-formed, but not that the partition is total -- and a
# race lane that silently tests fewer packages still reports success.
#
# Usage: test-shard-closure.sh [path-to-workflow]

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="${1:-$repo_root/.github/workflows/ci.yml}"

test -r "$workflow" || {
  printf '%s: cannot read workflow file: %s\n' "${0##*/}" "$workflow" >&2
  exit 2
}

race_block="$(awk '
  /^  race:/ { grab=1; print; next }
  grab && /^  [A-Za-z0-9_-]+:/ { grab=0 }
  grab { print }
' "$workflow")"

shard_line="$(printf '%s\n' "$race_block" | grep -E 'shard: *\[' | head -n1 || true)"
if [ -z "$shard_line" ]; then
  printf '%s: race job has no "shard: [...]" matrix in %s\n' "${0##*/}" "$workflow" >&2
  exit 1
fi

shard_call="$(printf '%s\n' "$race_block" \
  | grep -E '^[[:space:]]*[a-z_]+="?\$\(scripts/ci/test-shard\.sh' | head -n1 || true)"
if [ -z "$shard_call" ]; then
  printf '%s: race job does not invoke scripts/ci/test-shard.sh in %s\n' "${0##*/}" "$workflow" >&2
  exit 1
fi

total="$(printf '%s\n' "$shard_call" | grep -oE '[0-9]+' | tail -n1 || true)"
if [ -z "$total" ]; then
  printf '%s: could not read the shard total from: %s\n' "${0##*/}" "$shard_call" >&2
  exit 1
fi

mapfile -t indices < <(
  printf '%s' "$shard_line" \
    | sed -E 's/.*\[([^]]*)\].*/\1/' \
    | tr ',' '\n' | tr -d '[:blank:]' | grep -v '^$'
)

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

: >"$tmpdir/union"
for index in "${indices[@]}"; do
  "$repo_root/scripts/ci/test-shard.sh" "$index" "$total" | tr ' ' '\n' >>"$tmpdir/union"
done

LC_ALL=C sort "$tmpdir/union" | grep -v '^$' >"$tmpdir/union.sorted"
(cd "$repo_root" && go list ./...) | LC_ALL=C sort >"$tmpdir/all.sorted"

duplicates="$(LC_ALL=C uniq -d "$tmpdir/union.sorted")"
if [ -n "$duplicates" ]; then
  printf '%s: these packages are covered by more than one shard:\n%s\n' \
    "${0##*/}" "$duplicates" >&2
  exit 1
fi

if ! diff -u "$tmpdir/all.sorted" "$tmpdir/union.sorted" >"$tmpdir/diff"; then
  # shellcheck disable=SC2016  # backticked `go list ./...` is prose, not a shell expansion
  printf '%s: the union of shards 1..%s is not exactly `go list ./...`\n' "${0##*/}" "$total" >&2
  printf '  (-) listed by go but covered by no shard; (+) covered but not listed\n' >&2
  cat "$tmpdir/diff" >&2
  exit 1
fi

printf 'PASS: shards %s of %s cover all %s packages exactly once\n' \
  "$(printf '%s,' "${indices[@]}" | sed 's/,$//')" \
  "$total" \
  "$(wc -l <"$tmpdir/all.sorted" | tr -d ' ')"
