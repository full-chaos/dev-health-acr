#!/usr/bin/env bash
set -euo pipefail

# Asserts every workflow that runs Go sets GOFLAGS=-trimpath at workflow level,
# no job/step overrides GOFLAGS without -trimpath, and every Dockerfile
# `go build` passes -trimpath. Negative controls mutate temp copies and require
# the same check to fail.
#
# Usage: test-trimpath-guard.sh [workflows-dir] [Dockerfile]

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflows="${1:-$repo_root/.github/workflows}"
dockerfile="${2:-$repo_root/Dockerfile}"

go_cmd='(^|[^[:alnum:]_.-])go (build|test|run|vet|install)([[:space:]]|$)|setup-go@'

check_workflow() {
  local file=$1 code
  code="$(grep -vE '^[[:space:]]*#' "$file")"
  grep -qE "$go_cmd" <<<"$code" || return 0
  awk '
    /^env:/ { in_env=1; next }
    in_env && /^[^[:space:]]/ { in_env=0 }
    in_env && /^  GOFLAGS:.*-trimpath/ { found=1 }
    END { exit found ? 0 : 1 }
  ' <<<"$code" || { printf '%s: no workflow-level GOFLAGS with -trimpath\n' "$file" >&2; return 1; }
  if grep -E '^[[:space:]]+GOFLAGS:' <<<"$code" | grep -vq -- '-trimpath'; then
    printf '%s: nested GOFLAGS override without -trimpath\n' "$file" >&2
    return 1
  fi
  if grep -E 'GOFLAGS=' <<<"$code" | grep -vq -- '-trimpath'; then
    printf '%s: inline GOFLAGS= without -trimpath\n' "$file" >&2
    return 1
  fi
}

check_dockerfile() {
  local file=$1 bad
  bad="$(grep -E 'go build( |$)' "$file" | grep -v -- '-trimpath' || true)"
  if [ -n "$bad" ]; then
    printf '%s: go build without -trimpath: %s\n' "$file" "$bad" >&2
    return 1
  fi
}

check_all() {
  local dir=$1 df=$2 f rc=0 n=0
  for f in "$dir"/*.yml; do
    n=$((n + 1))
    check_workflow "$f" || rc=1
  done
  [ "$n" -gt 0 ] || { printf 'no workflows under %s\n' "$dir" >&2; return 1; }
  check_dockerfile "$df" || rc=1
  return $rc
}

check_all "$workflows" "$dockerfile"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

expect_fail() {
  local name=$1
  if check_all "$tmp/wf" "$tmp/Dockerfile" 2>/dev/null; then
    printf 'negative control did not fail: %s\n' "$name" >&2
    exit 1
  fi
}
reset() { rm -rf "$tmp/wf"; cp -r "$workflows" "$tmp/wf"; cp "$dockerfile" "$tmp/Dockerfile"; }

for f in "$workflows"/*.yml; do
  grep -qE '^  GOFLAGS:.*-trimpath' "$f" || continue
  reset
  sed -i -E 's/^(  GOFLAGS:).*/\1 -mod=readonly/' "$tmp/wf/$(basename "$f")"
  expect_fail "workflow GOFLAGS lacks -trimpath: $(basename "$f")"
done

reset
sed -i -E '0,/-trimpath/s// /' "$tmp/Dockerfile"
expect_fail "Dockerfile go build lacks -trimpath"

reset
f="$(grep -lE '^  GOFLAGS:.*-trimpath' "$workflows"/*.yml | head -1)"
printf '\n      - run: GOFLAGS=-mod=readonly go test ./...\n' >>"$tmp/wf/$(basename "$f")"
expect_fail "inline GOFLAGS override"

printf 'trimpath guard ok\n'
