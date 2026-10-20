#!/usr/bin/env bash
set -euo pipefail

# Rows for the isolated internal/contracts/v1 race job: the package runs alone
# in race-contracts-v1 under its own 440s budget instead of sharing race shard 4.
# Every row is evaluated before the script exits, so one run names every fault.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
pkg="github.com/full-chaos/dev-health-acr/internal/contracts/v1"
fails=0
fail() { printf 'FAIL: %s\n' "$1" >&2; fails=$((fails + 1)); }

got="$("$root/scripts/ci/test-shard.sh" isolated-dedicated v1 2>/dev/null || true)"
[ "$got" = "$pkg" ] || fail "isolated-dedicated v1 prints '$got', want $pkg"

for idx in 1 2 3 4; do
  if "$root/scripts/ci/test-shard.sh" "$idx" 4 | tr ' ' '\n' | grep -qxF "$pkg"; then
    fail "internal/contracts/v1 is still in round-robin race shard $idx"
  fi
done

if grep -qF "internal/contracts/v1=" "$root/scripts/ci/test-shard.sh"; then
  fail "race_spread_pins still pins internal/contracts/v1"
fi

budget="$(make -s -C "$root" isolated-timeout PKG="$pkg")"
[ "$budget" = "440s" ] || fail "isolated-timeout for internal/contracts/v1 is '$budget', want 440s"

workflow="$root/.github/workflows/ci.yml"
grep -qxF '  race-contracts-v1:' "$workflow" || fail "ci.yml has no race-contracts-v1 job"
grep -qxF '      - race-contracts-v1' "$workflow" || fail "the aggregate job does not need race-contracts-v1"
awk '/^  race-contracts-v1:/ { g = 1; next } g && /^  [A-Za-z0-9_-]+:/ { g = 0 } g' "$workflow" |
  grep -qF 'test-shard.sh isolated-dedicated v1' || fail "race-contracts-v1 does not select internal/contracts/v1"

[ "$fails" -eq 0 ] || { printf '%s violation(s)\n' "$fails" >&2; exit 1; }
printf 'PASS: contracts/v1 isolation rows\n'
