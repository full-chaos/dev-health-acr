#!/usr/bin/env bash
set -euo pipefail

# Rows for the isolated internal/api race jobs (CHAOS-12717): the package runs
# alone, in two halves split by scripts/ci/api-heavy-tests.txt (race-split.sh
# api), each under the same 420s budget it had in race shard 3, instead of
# sharing that shard. Every row is evaluated before the script exits, so one
# run names every fault.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
pkg="github.com/full-chaos/dev-health-acr/internal/api"
fails=0
fail() { printf 'FAIL: %s\n' "$1" >&2; fails=$((fails + 1)); }

got="$("$root/scripts/ci/test-shard.sh" isolated-dedicated api 2>/dev/null || true)"
[ "$got" = "$pkg" ] || fail "isolated-dedicated api prints '$got', want $pkg"

for idx in 1 2 3 4; do
  if "$root/scripts/ci/test-shard.sh" "$idx" 4 | tr ' ' '\n' | grep -qxF "$pkg"; then
    fail "internal/api is still in round-robin race shard $idx"
  fi
done

if grep -qF "internal/api=" "$root/scripts/ci/test-shard.sh"; then
  fail "race_spread_pins still pins internal/api"
fi

# No budget raise: each half keeps the 420s the shard gave the whole package.
budget="$(make -s -C "$root" isolated-timeout PKG="$pkg")"
[ "$budget" = "420s" ] || fail "isolated-timeout for internal/api is '$budget', want 420s"

workflow="$root/.github/workflows/ci.yml"
job_block() { awk -v job="$1" '$0 == "  " job ":" { g = 1; next } g && /^  [A-Za-z0-9_-]+:/ { g = 0 } g' "$workflow"; }
for job in race-api race-api-heavy; do
  grep -qxF "  $job:" "$workflow" || fail "ci.yml has no $job job"
  grep -qxF "      - $job" "$workflow" || fail "the aggregate job does not need $job"
  block="$(job_block "$job")"
  grep -qF 'test-shard.sh isolated-dedicated api' <<<"$block" || fail "$job does not select internal/api"
  grep -qF 'race-split.sh api verify' <<<"$block" || fail "$job does not verify the api heavy list"
  grep -qF "artifact: race-json-$job" <<<"$block" || fail "$job does not fetch main's baseline of its own artifact"
  grep -qF 'RACE_BASELINE_JSON: .tmp/race-baseline/race.json' <<<"$block" || fail "$job does not hand the baseline to the guard"
  grep -qF "name: race-json-$job" <<<"$block" || fail "$job does not keep its test2json events"
done
# shellcheck disable=SC2016 # the selector text itself, not an expansion
grep -qF -- '-skip=$(scripts/ci/race-split.sh api rest)' <<<"$(job_block race-api)" || fail "race-api does not -skip the heavy list"
# shellcheck disable=SC2016 # the selector text itself, not an expansion
grep -qF -- '-run=$(scripts/ci/race-split.sh api heavy)' <<<"$(job_block race-api-heavy)" || fail "race-api-heavy does not -run the heavy list"
grep -qF 'race-split.sh api partition' <<<"$(job_block race-api-heavy)" || fail "race-api-heavy does not check the split is a partition"

[ "$fails" -eq 0 ] || { printf '%s violation(s)\n' "$fails" >&2; exit 1; }
printf 'PASS: internal/api isolation rows\n'
