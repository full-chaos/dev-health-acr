#!/usr/bin/env bash
# Unit test for prune_stale_attempt_dirs (scripts/container/lib/prune-stale-attempt-dirs.sh).
#
# CHAOS-3772 R3: age-based pruning. Proves a young sibling always
# survives, an old one is removed, and the boundary is exact (age equal
# to the threshold survives; one second past it is removed). Pure bash
# against synthetic fixtures with a fixed clock passed to the function --
# no docker, no real sleeping required.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/container/lib/prune-stale-attempt-dirs.sh
source "${repo_root}/scripts/container/lib/prune-stale-attempt-dirs.sh"

fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

pass=0
fail=0
now=1755000000
max_age=86400 # 24 hours, matches the production default (CHAOS-3772 R4-2)

set_mtime() {
  # touch -t interprets its timestamp argument in LOCAL time (unlike the
  # UTC formatting used elsewhere in this repo for display strings), so
  # the conversion here must deliberately omit -u to land on the intended
  # epoch regardless of the host's timezone.
  local path="$1" epoch="$2" stamp
  stamp="$(date -d "@${epoch}" +%Y%m%d%H%M.%S 2>/dev/null || date -r "${epoch}" +%Y%m%d%H%M.%S)"
  touch -t "$stamp" "$path"
}

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

keep_dir="${fixture}/attempt.keep"
mkdir -p "$keep_dir"
set_mtime "$keep_dir" $((now - max_age - 999999)) # ancient, but must never be touched

young_sibling="${fixture}/attempt.young"
mkdir -p "$young_sibling"
set_mtime "$young_sibling" $((now - 60)) # one minute old

old_sibling="${fixture}/attempt.old"
mkdir -p "$old_sibling"
set_mtime "$old_sibling" $((now - max_age - 3600)) # well past the threshold

boundary_exact="${fixture}/attempt.boundary-exact"
mkdir -p "$boundary_exact"
set_mtime "$boundary_exact" $((now - max_age)) # age == max_age: not yet stale

boundary_over="${fixture}/attempt.boundary-over"
mkdir -p "$boundary_over"
set_mtime "$boundary_over" $((now - max_age - 1)) # age == max_age + 1: stale

prune_stale_attempt_dirs "$fixture" 'attempt.' "$keep_dir" "$max_age" "$now"

expect_survives 'the directory being kept, regardless of its own age' "$keep_dir"
expect_survives 'a young sibling' "$young_sibling"
expect_removed 'an old sibling well past the threshold' "$old_sibling"
expect_survives 'a sibling exactly at the threshold (age == max_age)' "$boundary_exact"
expect_removed 'a sibling one second past the threshold' "$boundary_over"


# CHAOS-3772 R4-2: garbage max_age_seconds must fail loudly, not silently
# misbehave (e.g. a negative or non-numeric value making every directory
# "too old" or making the comparison never trigger). The ancient kept dir
# must still be untouched, and nothing else should be pruned either --
# the function must fail closed before it ever reaches the loop.
garbage_probe="${fixture}/attempt.garbage-probe"
mkdir -p "$garbage_probe"
set_mtime "$garbage_probe" $((now - max_age - 3600))
for garbage_max_age in 'not-a-number' -1 0; do
  if prune_stale_attempt_dirs "$fixture" 'attempt.' "$keep_dir" "$garbage_max_age" "$now" 2>/dev/null; then
    printf 'FAIL: prune_stale_attempt_dirs accepted garbage max_age_seconds=%s instead of rejecting it\n' \
      "$garbage_max_age" >&2
    fail=$((fail + 1))
  else
    printf 'ok: garbage max_age_seconds=%s was rejected\n' "$garbage_max_age"
    pass=$((pass + 1))
  fi
done
expect_survives 'a directory left alone because max_age validation failed first' "$garbage_probe"

test "$fail" -eq 0 || {
  printf '%s prune assertions failed, %s passed\n' "$fail" "$pass" >&2
  exit 1
}
printf 'all %s prune-stale-attempt-dirs assertions passed\n' "$pass"
