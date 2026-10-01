#!/usr/bin/env bash
set -euo pipefail

# shard-budget-guard.sh reads `go test` package summary lines and enforces the
# shard's wall budget per package.
#
# Usage: shard-budget-guard.sh <budget_seconds> <go-test-output-file>
#        shard-budget-guard.sh --json <budget_seconds> <gotestsum-jsonfile>
#
# Always prints the five slowest packages. Exits 1 when a package ran over 85%
# of the budget, or when a package printed a FAIL line (a timeout panic prints
# no `ok` line, so the FAIL line is the only place its package name appears).
# It never changes the -timeout it audits.

json=0
if [ "${1:-}" = "--json" ]; then
  json=1
  shift
fi
if [ "$#" -ne 2 ]; then
  echo "usage: $0 [--json] <budget_seconds> <file>" >&2
  exit 2
fi
budget="$1"
file="$2"
case "$budget" in
  '' | *[!0-9]*) echo "shard-budget-guard: budget must be whole seconds, got '$budget'" >&2; exit 2 ;;
esac
if [ ! -s "$file" ]; then
  echo "::error::shard-budget-guard: no go test output at $file; the measurement did not happen" >&2
  exit 1
fi

lines="$(mktemp)"
trap 'rm -f "$lines"' EXIT
if [ "$json" -eq 1 ]; then
  jq -r 'select((.Action == "pass" or .Action == "fail") and (.Test == null) and (.Package != null)) |
    (if .Action == "pass" then "ok" else "FAIL" end) + "\t" + .Package + "\t" + ((.Elapsed // 0) | tostring) + "s"' \
    "$file" >"$lines"
else
  cp "$file" "$lines"
fi

awk -v budget="$budget" '
  /^(ok|FAIL)[ \t]+[^ \t]+[ \t]+[0-9.]+s/ {
    pkg = $2; secs = $3; sub(/s.*$/, "", secs)
    n++; name[n] = pkg; t[n] = secs + 0
    if ($1 == "FAIL") failed[pkg] = 1
    next
  }
  /^FAIL[ \t]+[^ \t]+/ { failed[$2] = 1; next }
  END {
    printf "top 5 packages by time (budget %ss, guard at 85%% = %.1fs):\n", budget, budget * 0.85
    for (k = 1; k <= 5; k++) {
      best = 0
      for (i = 1; i <= n; i++) if (!used[i] && (best == 0 || t[i] > t[best])) best = i
      if (best == 0) break
      used[best] = 1
      printf "  %8.1fs  %5.1f%%  %s\n", t[best], t[best] * 100 / budget, name[best]
    }
    rc = 0
    for (i = 1; i <= n; i++) if (t[i] * 100 > budget * 85) {
      printf "::error::CHAOS-6342 class: %s at %ss is over 85%% of %ss\n", name[i], t[i], budget
      rc = 1
    }
    for (p in failed) {
      printf "::error::package %s printed FAIL (timeout panic or test failure); it has no passing time to compare to the %ss budget\n", p, budget
      rc = 1
    }
    exit rc
  }
' "$lines"
