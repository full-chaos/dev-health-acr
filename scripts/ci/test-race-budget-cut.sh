#!/usr/bin/env bash
set -euo pipefail

# Rows for the measured race-budget cut: internal/mcp runs alone in race-mcp
# under its own 480s budget, and the six slowest non-mid devhealthsource tests
# run in the rest job instead of the heavy job.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mcp_pkg="github.com/full-chaos/dev-health-acr/internal/mcp"
fails=0
fail() { printf 'FAIL: %s\n' "$1" >&2; fails=$((fails + 1)); }

got="$("$root/scripts/ci/test-shard.sh" isolated-dedicated mcp 2>/dev/null || true)"
[ "$got" = "$mcp_pkg" ] || fail "isolated-dedicated mcp prints '$got', want $mcp_pkg"

for idx in 1 2 3 4; do
  if "$root/scripts/ci/test-shard.sh" "$idx" 4 | tr ' ' '\n' | grep -qxF "$mcp_pkg"; then
    fail "internal/mcp is still in round-robin race shard $idx"
  fi
done

budget="$(make -s -C "$root" isolated-timeout PKG="$mcp_pkg")"
[ "$budget" = "480s" ] || fail "isolated-timeout for internal/mcp is '$budget', want 480s"

workflow="$root/.github/workflows/ci.yml"
grep -qxF '  race-mcp:' "$workflow" || fail "ci.yml has no race-mcp job"
grep -qxF '      - race-mcp' "$workflow" || fail "the aggregate job does not need race-mcp"
awk '/^  race-mcp:/ { g = 1; next } g && /^  [A-Za-z0-9_-]+:/ { g = 0 } g' "$workflow" |
  grep -qF 'test-shard.sh isolated-dedicated mcp' || fail "race-mcp does not select internal/mcp"

heavy="$root/scripts/ci/devhealthsource-heavy-tests.txt"
for t in \
  TestProjectTeamOwnershipStatementMemoryAgainstTheOwnershipHistory \
  TestEveryBlockerLinkOfTheLargestOrganizationRemovedIsRetracted \
  TestWorkGraphLabelStatementCostIgnoresOtherWorkItemsAndOtherEdges \
  TestIngestTimeCursor \
  TestProjectTeamOwnershipStatementMemoryOnTheProdShape \
  TestMembershipColumnArmDrainsEveryRowWhileASyncRewritesThem; do
  if grep -qxF "$t" "$heavy"; then fail "$t is still in the devhealthsource heavy list"; fi
done

[ "$fails" -eq 0 ] || exit 1
printf 'PASS: race budget cut rows\n'
