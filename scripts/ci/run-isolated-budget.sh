#!/usr/bin/env bash
set -euo pipefail

# run-isolated-budget.sh runs one isolated package's `make test-race` and
# applies the 85% budget rule to the PACKAGE test time that go test prints,
# the time `go test -timeout` governs, not to the step's wall time (which adds
# go build and cache restore, neither of which the timeout kills).
#
# Usage: run-isolated-budget.sh <label> <budget_seconds> <packages>
# GOFLAGS (-run/-skip selection) is inherited from the caller.
#
# Fails when the tests fail, when go test printed no package time (the
# measurement did not happen), or when the package time passes 85% of budget.
# The wall time is printed for reference only: these jobs set no
# timeout-minutes (GitHub default 360 min), far above build + budget.

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <label> <budget_seconds> <packages>" >&2
  exit 2
fi
label="$1"
budget="$2"
packages="$3"
here="$(cd "$(dirname "$0")" && pwd)"
guard="${GUARD:-$here/shard-budget-guard.sh}"
runner="${RUNNER:-make test-race}"

out="$(mktemp)"
trap 'rm -f "$out"' EXIT
start=$SECONDS
set +e
# shellcheck disable=SC2086
$runner GOTEST_PKGS="$packages" GOTEST_TIMEOUT="${budget}s" 2>&1 | tee "$out"
pipe=("${PIPESTATUS[@]}")
set -e
status=${pipe[0]}
if [ "$status" -eq 0 ] && [ "${pipe[1]}" -ne 0 ]; then
  printf '::error::tee failed (exit %s); the captured go test output is incomplete\n' "${pipe[1]}"
  status=1
fi
printf '%s race wall time (build + tests, informational): %ss; budget %ss\n' "$label" "$((SECONDS - start))" "$budget"

guard_rc=0
"$guard" "$budget" "$out" || guard_rc=$?
if [ "$guard_rc" -ne 0 ]; then
  printf '::error::%s: package test time failed the 85%% guard of its %ss budget; rebalance the heavy-test list or retune the budget\n' "$label" "$budget"
fi
if [ "$status" -ne 0 ]; then exit "$status"; fi
exit "$guard_rc"
