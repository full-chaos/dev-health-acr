#!/usr/bin/env bash
set -euo pipefail

# unit-leg-timeout.sh prints the plain (non-race) -timeout one `unit` leg needs
# for the packages it runs, the value ci.yml's unit and unit-dedicated jobs
# pass as GOTEST_PLAIN_TIMEOUT and to the leg's budget guard.
#
# A leg's default is 600s (GOTEST_PLAIN_TIMEOUT 10m in the Makefile). An
# isolated package whose own budget (`make isolated-timeout`) is LARGER raises
# the leg to that budget, because go test's -timeout panics per test binary.
# A smaller budget never lowers the leg (CHAOS-12717): internal/api's 420s,
# mcp's 480s and contracts/v1's 440s are the budgets of their own race jobs,
# and taking one of them as a whole unit leg's budget cut leg 3, which holds
# internal/api and falkorgraph, from 600s to 420s (#1978, main 34d372e2:
# falkorgraph 378-415s over the 357s guard).
#
# Prints nothing when no isolated package raises the leg, so the caller keeps
# its default. Names each isolated package it read on stderr.
# Usage: unit-leg-timeout.sh "<space-separated import paths>"

if [ "$#" -ne 1 ]; then
  echo "usage: $0 \"<space-separated import paths>\"" >&2
  exit 2
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
packages="$(printf '%s' "$1" | tr '\n\t' '  ')"
floor_secs=600
best=""
best_secs="$floor_secs"
for isolated_pkg in $("$root/scripts/ci/test-shard.sh" --print-isolated-packages); do
  case " $packages " in
    *" $isolated_pkg "*)
      pkg_timeout="$(make -s -C "$root" isolated-timeout PKG="$isolated_pkg")"
      pkg_secs="${pkg_timeout%s}"
      case "$pkg_secs" in
        '' | *[!0-9]*)
          echo "unit-leg-timeout: $isolated_pkg has budget '$pkg_timeout', not whole seconds" >&2
          exit 2
          ;;
      esac
      printf 'unit leg carries isolated package %s (its own budget %s)\n' "$isolated_pkg" "$pkg_timeout" >&2
      if [ "$pkg_secs" -gt "$best_secs" ]; then
        best="$pkg_timeout"
        best_secs="$pkg_secs"
      fi
      ;;
  esac
done
printf '%s\n' "$best"
