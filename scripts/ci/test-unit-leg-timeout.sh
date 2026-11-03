#!/usr/bin/env bash
set -euo pipefail

# Rows for scripts/ci/unit-leg-timeout.sh (CHAOS-12717): an isolated package
# only RAISES a unit leg above the 600s default, never lowers it, and both
# unit jobs in ci.yml take the leg budget from that script. Every row is
# evaluated before the script exits, so one run names every fault.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$root/scripts/ci/unit-leg-timeout.sh"
mod="github.com/full-chaos/dev-health-acr"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0
fail() { printf 'FAIL: %s\n' "$1" >&2; fails=$((fails + 1)); }

row() { # label want packages...
  local label="$1" want="$2" got
  shift 2
  got="$("$script" "$*" 2>/dev/null)" || { fail "$label: the script exited non-zero"; return; }
  if [ "$got" = "$want" ]; then
    printf 'ok   %s -> %s\n' "$label" "${got:-default}"
  else
    fail "$label: got '${got:-default}', want '${want:-default}'"
  fi
}

# A smaller race budget never lowers the leg below its 600s default.
row "internal/api (420s) keeps the default" "" "$mod/internal/api" "$mod/internal/contextfabric/falkorgraph"
row "internal/mcp (480s) keeps the default" "" "$mod/internal/mcp"
row "contracts/v1 (440s) keeps the default" "" "$mod/internal/contracts/v1"
row "no isolated package keeps the default" "" "$mod/internal/contextfabric/falkorgraph" "$mod/cmd/acr-migrate"
# A larger budget still raises the leg, the largest one wins.
row "internal/contextfabric (750s) raises it" "750s" "$mod/internal/contextfabric"
row "devhealthfacts (1800s) raises it" "1800s" "$mod/internal/contextfabric/devhealthfacts"
row "the largest of several wins" "1800s" "$mod/internal/api" "$mod/internal/contextfabric" "$mod/internal/contextfabric/devhealthfacts"
# A prefix of an isolated path is not that package.
row "a package that only shares a prefix is not isolated" "" "$mod/internal/apikeys"

# The real unit legs: none falls below the default because of a race budget.
api_legs=0
for leg in 1 2 3 4; do
  pkgs="$("$root/scripts/ci/test-shard.sh" --with-isolated "$leg" 4)"
  got="$("$script" "$pkgs" 2>"$tmp/stderr")" || { fail "unit leg $leg: the script exited non-zero"; continue; }
  # The leg that holds internal/api must report reading it, or this row
  # measured nothing (a package list it cannot parse matches no package).
  if tr -s '[:space:]' '\n' <<<"$pkgs" | grep -qxF "$mod/internal/api"; then
    api_legs=$((api_legs + 1))
    grep -qF "carries isolated package $mod/internal/api " "$tmp/stderr" || fail "unit leg $leg holds internal/api but the script did not read it"
  fi
  secs="${got%s}"
  if [ -n "$got" ] && [ "$secs" -lt 600 ]; then
    fail "unit leg $leg gets $got, below the 600s default"
  else
    printf 'ok   unit leg %s -> %s\n' "$leg" "${got:-default}"
  fi
done

[ "$api_legs" = 1 ] || fail "internal/api is in $api_legs unit legs, want exactly 1"

# Both unit jobs take the leg budget from the script, and neither keeps an
# inline isolated-timeout loop that could lower it again.
workflow="$root/.github/workflows/ci.yml"
# shellcheck disable=SC2016 # the workflow's own text, not an expansion
calls="$(grep -cF 'isolated_timeout="$(scripts/ci/unit-leg-timeout.sh "$packages")"' "$workflow" || true)"
[ "$calls" = 2 ] || fail "ci.yml calls unit-leg-timeout.sh $calls times, want 2 (unit and unit-dedicated)"
# shellcheck disable=SC2016 # the workflow's own text, not an expansion
loops="$(grep -cF 'for isolated_pkg in $(scripts/ci/test-shard.sh --print-isolated-packages)' "$workflow" || true)"
[ "$loops" = 0 ] || fail "ci.yml still has $loops inline isolated-timeout loop(s)"

[ "$fails" -eq 0 ] || { printf '%s violation(s)\n' "$fails" >&2; exit 1; }
printf 'PASS: unit leg timeouts\n'
