#!/usr/bin/env bash
set -euo pipefail

# run-isolated-budget.sh runs one isolated package's `make test-race` and
# applies the 85% budget rule to the PACKAGE test time that go test prints,
# the time `go test -timeout` governs, not to the step's wall time (which adds
# go build and cache restore, neither of which the timeout kills).
#
# Usage: GOTEST_TIMEOUT=<Ns|Nm> run-isolated-budget.sh <label> <packages>
# GOTEST_TIMEOUT is the go test -timeout and the budget (required, so the job
# block still names its explicit timeout). GOFLAGS (-run/-skip selection) is
# inherited from the caller.
#
# Fails when the tests fail, when go test printed no package time (the
# measurement did not happen), or when the package time passes 85% of budget.
# The wall time is printed for reference only: these jobs set no
# timeout-minutes (GitHub default 360 min), far above build + budget.

if [ "$#" -ne 2 ]; then
  echo "usage: GOTEST_TIMEOUT=<Ns|Nm> $0 <label> <packages>" >&2
  exit 2
fi
label="$1"
packages="$2"
case "${GOTEST_TIMEOUT:-}" in
  [0-9]*s) budget="${GOTEST_TIMEOUT%s}" ;;
  [0-9]*m) budget=$((${GOTEST_TIMEOUT%m} * 60)) ;;
  *) echo "::error::GOTEST_TIMEOUT must be Ns or Nm, got '${GOTEST_TIMEOUT:-}'" >&2; exit 2 ;;
esac
here="$(cd "$(dirname "$0")" && pwd)"
guard="${GUARD:-$here/shard-budget-guard.sh}"
runner="${RUNNER:-make test-race}"

out="$(mktemp)"
trap 'rm -f "$out"' EXIT
start=$SECONDS
set +e
# shellcheck disable=SC2086
$runner GOTEST_PKGS="$packages" GOTEST_TIMEOUT="$GOTEST_TIMEOUT" 2>&1 | tee "$out"
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
