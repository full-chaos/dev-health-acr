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
# In --json mode it also fails a top-level test that ran longer than RACE_TEST_LIMIT_SECONDS (default 300) under -race
# unless scripts/ci/race-slow-tests.tsv lifts it (CHAOS-10333). It never changes the -timeout it audits.

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
  jq -Rr 'fromjson? | select((.Action == "pass" or .Action == "fail") and (.Test == null) and (.Package != null)) |
    (if .Action == "pass" then "ok" else "FAIL" end) + "\t" + .Package + "\t" + ((.Elapsed // 0) | tostring) + "s"' \
    "$file" >"$lines"
else
  cp "$file" "$lines"
fi

pkg_rc=0
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
    if (n == 0) {
      print "::error::no ok/FAIL package timing rows were parsed; the measurement did not happen"
      rc = 1
    }
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
' "$lines" || pkg_rc=$?

# CHAOS-10333: per-test limit, JSON mode only (the race jobs). A top-level test (no "/" in its name; a subtest rolls up
# into its parent) that ran longer than the limit under -race fails the job unless scripts/ci/race-slow-tests.tsv lifts
# that one test. It measures what a test costs, so it does not matter how the size is reached (a local constant, a
# helper, a table). The input is the file this very job wrote (run-isolated-budget.sh truncates it before the run), never
# a downloaded artifact of an earlier attempt.
test_rc=0
if [ "$json" -eq 1 ]; then
  limit="${RACE_TEST_LIMIT_SECONDS:-300}"
  band="${RACE_TEST_BAND_SECONDS:-30}"
  max_bytes="${RACE_JSON_MAX_BYTES:-536870912}"
  allow="${RACE_SLOW_TESTS:-$(cd "$(dirname "$0")" && pwd)/race-slow-tests.tsv}"
  case "$limit$band$max_bytes" in *[!0-9]*) echo "shard-budget-guard: RACE_TEST_LIMIT_SECONDS, RACE_TEST_BAND_SECONDS and RACE_JSON_MAX_BYTES must be whole numbers" >&2; exit 2 ;; esac
  [ -r "$allow" ] || { echo "shard-budget-guard: allowlist $allow is not readable" >&2; exit 2; }
  # a row is package<TAB>test<TAB>limit_seconds<TAB>reason; the first line is the header
  if ! awk -F'\t' 'NR == 1 { next } NF != 4 || $1 == "" || $2 == "" || $3 !~ /^[0-9]+$/ || $4 == "" { bad = 1 } END { exit bad }' "$allow"; then
    echo "shard-budget-guard: $allow has a row that is not package<TAB>test<TAB>limit_seconds<TAB>reason" >&2
    exit 2
  fi
  size="$(wc -c <"$file")"
  if [ "$size" -gt "$max_bytes" ]; then
    printf '::error::shard-budget-guard: %s is %s bytes, over the %s byte bound; the per-test limit was not measured\n' "$file" "$size" "$max_bytes"
    exit 1
  fi
  tests="$(mktemp)"
  trap 'rm -f "$lines" "$tests"' EXIT
  jq -Rr 'fromjson? | select((.Action == "pass" or .Action == "fail") and (.Test != null) and ((.Test | contains("/")) | not) and (.Package != null))
    | "\(.Package)\t\(.Test)\t\(.Elapsed // 0)"' "$file" >"$tests"
  if [ ! -s "$tests" ]; then
    echo "::error::shard-budget-guard: no test-level events in $file; the per-test limit was not measured"
    exit 1
  fi
  test_rc=0
  awk -F'\t' -v limit="$limit" -v band="$band" -v allowfile="$allow" '
    BEGIN {
      while ((getline row < allowfile) > 0) {
        n = split(row, f, "\t")
        if (n == 4 && f[3] ~ /^[0-9]+$/) lift[f[1] "\t" f[2]] = f[3] + 0
      }
    }
    { pkg[NR] = $1; name[NR] = $2; secs[NR] = $3 + 0 }
    END {
      printf "slowest top-level tests at or over %ss (limit %ss):\n", band, limit
      shown = 0
      for (k = 1; k <= 10; k++) {
        best = 0
        for (i = 1; i <= NR; i++) if (!used[i] && (best == 0 || secs[i] > secs[best])) best = i
        if (best == 0 || secs[best] < band) break
        used[best] = 1; shown++
        printf "  %s %s %ss\n", pkg[best], name[best], secs[best]
      }
      rc = 0
      for (i = 1; i <= NR; i++) {
        max = limit
        key = pkg[i] "\t" name[i]
        if (key in lift) max = lift[key]
        if (secs[i] > max) {
          printf "::error::%s %s ran %ss under -race, over the %ss per-test limit; tag it sizeproof (plain unit run only) or add a reviewed row to scripts/ci/race-slow-tests.tsv\n", pkg[i], name[i], secs[i], max
          rc = 1
        }
      }
      exit rc
    }
  ' "$tests" || test_rc=$?
fi
if [ "$pkg_rc" -ne 0 ] || [ "$test_rc" -ne 0 ]; then exit 1; fi
