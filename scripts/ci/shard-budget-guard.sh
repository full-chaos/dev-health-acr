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
  # the first line must be exactly the header; every other row is package<TAB>test<TAB>limit_seconds<TAB>reason
  if ! awk -F'\t' -v hdr="package	test	limit_seconds	reason" '
    NR == 1 { if ($0 != hdr) bad = 1; next }
    NF != 4 || $1 == "" || $2 == "" || $3 !~ /^[0-9]+$/ || $4 == "" { bad = 1 }
    END { if (NR == 0) bad = 1; exit bad }' "$allow"; then
    echo "shard-budget-guard: $allow must start with the header package<TAB>test<TAB>limit_seconds<TAB>reason and hold only rows of that shape" >&2
    exit 2
  fi
  size="$(wc -c <"$file")"
  if [ "$size" -gt "$max_bytes" ]; then
    printf '::error::shard-budget-guard: %s is %s bytes, over the %s byte bound; the per-test limit was not measured\n' "$file" "$size" "$max_bytes"
    exit 1
  fi
  tests="$(mktemp)"
  trap 'rm -f "$lines" "$tests"' EXIT
  # A line that starts with "{" is a test2json record and must parse; a top-level test record that ended (pass, fail or skip)
  # must carry a package and a numeric Elapsed. Anything else fails closed. A line that does not start with "{" is
  # the wrapper's own echo (make's recipe line) and is ignored.
  jq -Rr 'if startswith("{") then
      ((try fromjson catch null) as $e
       | if ($e | type) != "object" then "BAD\t\(input_line_number)\tnot a JSON object"
         elif ($e.Test != null) and ($e.Action == "pass" or $e.Action == "fail" or $e.Action == "skip") and (($e.Test | tostring | contains("/")) | not) then
           (if ($e.Package | type) != "string" then "BAD\t\(input_line_number)\tno package"
            elif ($e.Elapsed | type) != "number" or $e.Elapsed < 0 then "BAD\t\(input_line_number)\tElapsed is not a non-negative number"
            else "T\t\($e.Package)\t\($e.Test)\t\($e.Elapsed)" end)
         else empty end)
    else empty end' "$file" >"$tests" || { echo "::error::shard-budget-guard: jq could not read $file; the per-test limit was not measured"; exit 1; }
  if grep -q '^BAD' "$tests"; then
    printf '::error::shard-budget-guard: unreadable record in %s (line %s: %s); the per-test limit was not measured\n' "$file" "$(grep -m1 '^BAD' "$tests" | cut -f2)" "$(grep -m1 '^BAD' "$tests" | cut -f3)"
    exit 1
  fi
  sed -i 's/^T\t//' "$tests"
  if [ ! -s "$tests" ]; then
    echo "::error::shard-budget-guard: no test-level events in $file; the per-test limit was not measured"
    exit 1
  fi
  test_rc=0
  awk -F'\t' -v limit="$limit" -v band="$band" -v allowfile="$allow" '
    BEGIN {
      while ((getline row < allowfile) > 0) {
        n = split(row, f, "\t")
        if (++line > 1 && n == 4 && f[3] ~ /^[0-9]+$/) lift[f[1] "\t" f[2]] = f[3] + 0
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
          printf "::error::%s %s ran %ss under -race, over the %ss per-test limit; mark it with the production-size build tag (plain unit run only) or add a reviewed row to scripts/ci/race-slow-tests.tsv\n", pkg[i], name[i], secs[i], max
          rc = 1
        }
      }
      exit rc
    }
  ' "$tests" || test_rc=$?
fi
# CHAOS-12667: real span report, JSON mode only. Reporting only: nothing below changes the exit code. A top-level test
# that starts parallel subtests reports only its own function time as Elapsed, while the subtests keep it running, so
# the span is (last event of T or of any T/...) - (T's last run/cont event). A paused parallel test starts at its cont.
# It never prints a number it did not measure: a parse failure prints ::warning:: and skips the block.
span_rows() { # file -> "P<TAB>pkg<TAB>elapsed" and "T<TAB>pkg<TAB>test<TAB>span<TAB>elapsed" rows, slowest span first
  local f="$1" ev
  ev="$(mktemp)"
  jq -Rr 'fromjson? | select(type == "object" and .Package != null and .Time != null and (.Action | type) == "string")
    | (.Time as $t
       | ($t | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) as $s
       | ((($t | capture("\\.(?<f>[0-9]+)Z$").f) // "0") | ("0." + .) | tonumber) as $f
       | ($s + $f)) as $ts
    | if .Test == null then (select(.Action == "pass" or .Action == "fail") | "P\t\(.Package)\t\(.Elapsed // 0)")
      else "E\t\(.Package)\t\(.Test)\t\(.Action)\t\($ts)\t\(.Elapsed // 0)" end' "$f" >"$ev" || { rm -f "$ev"; return 1; }
  awk -F'\t' '
    $1 == "P" { print; next }
    $1 == "E" {
      split($3, parts, "/"); top = parts[1]; key = $2 "\t" top; ts = $5 + 0
      if ($3 == top && ($4 == "run" || $4 == "cont")) { start[key] = ts; has[key] = 1 }
      if (!(key in last) || ts > last[key]) last[key] = ts
      if ($3 == top && ($4 == "pass" || $4 == "fail" || $4 == "skip")) el[key] = $6 + 0
    }
    END { for (key in has) printf "T\t%s\t%.3f\t%.3f\n", key, last[key] - start[key], el[key] + 0 }
  ' "$ev" | sort -t "$(printf '\t')" -k1,1 -k4,4gr
  rm -f "$ev"
}

fetch_baseline() { # prints the path of a baseline race.json (runs in a subshell); on failure returns 1 with the reason in $tmpd/reason
  local baseline_reason=""
  trap '[ -z "$baseline_reason" ] || printf %s "$baseline_reason" >"$tmpd/reason"' RETURN
  if [ -n "${RACE_BASELINE_JSON:-}" ]; then
    if [ -r "$RACE_BASELINE_JSON" ] && [ -s "$RACE_BASELINE_JSON" ]; then echo "$RACE_BASELINE_JSON"; return 0; fi
    baseline_reason="RACE_BASELINE_JSON $RACE_BASELINE_JSON is not a readable file"; return 1
  fi
  local art="$RACE_BASELINE_ARTIFACT" dir ids id got
  command -v gh >/dev/null 2>&1 || { baseline_reason="gh is not installed"; return 1; }
  [ -n "${GITHUB_REPOSITORY:-}" ] || { baseline_reason="GITHUB_REPOSITORY is not set"; return 1; }
  ids="$(gh run list -R "$GITHUB_REPOSITORY" --workflow ci.yml --branch main --event push --status success --limit 10 --json databaseId -q '.[].databaseId' 2>/dev/null)" \
    || { baseline_reason="gh run list failed"; return 1; }
  [ -n "$ids" ] || { baseline_reason="no successful main run"; return 1; }
  dir="$tmpd/dl"
  for id in $ids; do
    mkdir -p "$dir/$id"
    if gh run download "$id" -R "$GITHUB_REPOSITORY" -n "$art" -D "$dir/$id" >/dev/null 2>&1; then
      got="$(find "$dir/$id" -name race.json -type f -size +0c 2>/dev/null | head -n1)"
      if [ -n "$got" ]; then echo "main run $id" >&2; echo "$got"; return 0; fi
    fi
  done
  baseline_reason="no race.json in artifact $art of the last main runs"; return 1
}

span_report() {
  local now base="" b_rows="" note="" baseline_reason=""
  now="$(span_rows "$file")" || return 1
  grep -q '^T' <<<"$now" || return 1
  if [ -n "${RACE_BASELINE_JSON:-}" ] || [ -n "${RACE_BASELINE_ARTIFACT:-}" ]; then
    local bf
    if bf="$(fetch_baseline 2>"$tmpd/note")"; then
      note="$(cat "$tmpd/note")"
      if ! b_rows="$(span_rows "$bf")" || ! grep -q '^T' <<<"$b_rows"; then
        b_rows=""
        printf %s "baseline $bf has no test timing" >"$tmpd/reason"
      fi
      [ -n "$b_rows" ] && base="$bf"
    fi
    baseline_reason="$(cat "$tmpd/reason" 2>/dev/null || true)"
    if [ -z "$base" ]; then
      printf '::warning::shard-budget-guard: no main baseline for %s: %s\n' "${RACE_BASELINE_ARTIFACT:-${RACE_BASELINE_JSON:-}}" "$baseline_reason"
    fi
  fi
  { printf '%s\n' "$now"; printf 'BASE\n'; [ -n "$b_rows" ] && printf '%s\n' "$b_rows"; true; } | awk -F'\t' -v hasbase="$([ -n "$base" ] && echo 1 || echo 0)" -v note="$note" '
    /^BASE$/ { inbase = 1; next }
    $1 == "P" { if (inbase) bp[$2] += $3; else np[$2] += $3; next }
    $1 == "T" {
      k = $2 "\t" $3
      if (inbase) { bspan[k] = $4 + 0 } else { n++; key[n] = k; pk[n] = $2; nm[n] = $3; sp[n] = $4 + 0; el[n] = $5 + 0 }
    }
    END {
      npk = 0; for (p in np) { npk++; onlypkg = p }
      printf "slowest top-level tests by real span (s), reported Elapsed beside it%s:\n", (npk == 1 ? " [" onlypkg "]" : "")
      for (i = 1; i <= n && i <= 10; i++) {
        label = (npk == 1 ? nm[i] : pk[i] " " nm[i])
        line = sprintf("  span %7.1f  elapsed %7.1f  %s", sp[i], el[i], label)
        if (hasbase) {
          if (key[i] in bspan) line = line sprintf("  | baseline %7.1f  delta %+.1f", bspan[key[i]], sp[i] - bspan[key[i]])
          else line = line "  | new (not in baseline)"
        }
        print line
      }
      if (hasbase) {
        if (note != "") print "baseline: " note
        tn = 0; tb = 0; for (p in np) { tn += np[p]; if (p in bp) tb += bp[p]; else missing = 1 }
        if (missing) print "package time: baseline lacks a package here; no package delta"
        else printf "package time: now %.1fs, baseline %.1fs, delta %+.1fs\n", tn, tb, tn - tb
      }
    }'
}

tmpd="$(mktemp -d)"
trap 'rm -f "$lines" "${tests:-}"; rm -rf "$tmpd"' EXIT
if [ "$json" -eq 1 ]; then
  if ! span_report; then
    echo "::warning::shard-budget-guard: could not compute the real span report from $file; the block is skipped"
  fi
  if [ -z "${RACE_BASELINE_JSON:-}" ] && [ -z "${RACE_BASELINE_ARTIFACT:-}" ]; then
    echo "no baseline set (RACE_BASELINE_ARTIFACT / RACE_BASELINE_JSON)"
  fi
fi

if [ "$pkg_rc" -ne 0 ] || [ "$test_rc" -ne 0 ]; then exit 1; fi
