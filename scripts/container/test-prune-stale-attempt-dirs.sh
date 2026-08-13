#!/usr/bin/env bash
# Unit test for prune_stale_attempt_dirs (scripts/container/lib/prune-stale-attempt-dirs.sh).
#
# CHAOS-3772 R2-1: proves a live sibling's evidence survives a concurrent
# invocation's prune, while a dead or unmarked sibling is removed. Pure
# bash against synthetic fixtures, no docker required.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/container/lib/prune-stale-attempt-dirs.sh
source "${repo_root}/scripts/container/lib/prune-stale-attempt-dirs.sh"

fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

pass=0
fail=0

expect_survives() {
  local label="$1" dir="$2"
  if [[ -d "$dir" ]]; then
    printf 'ok: %s survived\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL: %s was removed but should have survived\n' "$label" >&2
    fail=$((fail + 1))
  fi
}

expect_removed() {
  local label="$1" dir="$2"
  if [[ -d "$dir" ]]; then
    printf 'FAIL: %s survived but should have been removed\n' "$label" >&2
    fail=$((fail + 1))
  else
    printf 'ok: %s correctly removed\n' "$label"
    pass=$((pass + 1))
  fi
}

# A dead PID, deterministically: fork, let it exit, then reuse its now-
# reaped PID. No reliance on a "probably unused" hardcoded number.
( exit 0 ) &
dead_pid=$!
wait "$dead_pid" 2>/dev/null || true

keep_dir="${fixture}/attempt.keep"
mkdir -p "$keep_dir"
printf '%s\n' "$$" >"${keep_dir}/.owner.pid"

live_sibling="${fixture}/attempt.live-sibling"
mkdir -p "$live_sibling"
printf '%s\n' "$$" >"${live_sibling}/.owner.pid"

dead_sibling="${fixture}/attempt.dead-sibling"
mkdir -p "$dead_sibling"
printf '%s\n' "$dead_pid" >"${dead_sibling}/.owner.pid"

unmarked_sibling="${fixture}/attempt.unmarked-sibling"
mkdir -p "$unmarked_sibling"

prune_stale_attempt_dirs "$fixture" 'attempt.' "$keep_dir"

expect_survives 'the directory being kept' "$keep_dir"
expect_survives 'a sibling owned by a live PID' "$live_sibling"
expect_removed 'a sibling owned by a dead PID' "$dead_sibling"
expect_removed 'a sibling with no ownership marker' "$unmarked_sibling"

test "$fail" -eq 0 || {
  printf '%s prune assertions failed, %s passed\n' "$fail" "$pass" >&2
  exit 1
}
printf 'all %s prune-stale-attempt-dirs assertions passed\n' "$pass"
