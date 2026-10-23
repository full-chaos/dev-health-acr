#!/usr/bin/env bash
set -euo pipefail

# Fixture test for shard-budget-guard.sh. Run with GUARD=<other script> to
# prove the test goes red against a guard that lacks a rule.

here="$(cd "$(dirname "$0")" && pwd)"
guard="${GUARD:-$here/shard-budget-guard.sh}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0

expect() { # name want_rc want_pattern budget file
  local name="$1" want="$2" pat="$3" budget="$4" f="$5" rc=0 out
  out="$("$guard" "$budget" "$f" 2>&1)" || rc=$?
  if [ "$rc" -ne "$want" ] || ! grep -q -- "$pat" <<<"$out"; then
    echo "FAIL $name: rc=$rc want=$want pattern='$pat'"; printf '    %s\n' "${out//$'\n'/$'\n    '}"
    fails=$((fails + 1))
  else
    echo "ok   $name"
  fi
}

printf 'ok  \texample/a\t10.5s\nok  \texample/b\t200.0s\n?   \texample/c\t[no test files]\n' >"$tmp/under"
expect under 0 'example/b' 420 "$tmp/under"
expect under-top5-always 0 'top 5 packages' 420 "$tmp/under"

printf 'ok  \texample/a\t10.5s\nok  \texample/slow\t357.1s\n' >"$tmp/edge"
expect at-85-percent-edge 1 'example/slow at 357.1s is over 85% of 420s' 420 "$tmp/edge"
printf 'ok  \texample/slow\t357.0s\n' >"$tmp/edge2"
expect just-under-edge 0 'top 5' 420 "$tmp/edge2"

printf 'ok  \texample/a\t1.0s\nFAIL\texample/hung\t420.012s\npanic: test timed out after 7m0s\n' >"$tmp/panic"
expect panic 1 'package example/hung printed FAIL' 420 "$tmp/panic"

printf '?   \texample/c\t[no test files]\n' >"$tmp/nofiles"
expect no-test-files-only 1 'measurement did not happen' 420 "$tmp/nofiles"

printf 'FAIL\texample/broken [build failed]\n' >"$tmp/build"
expect build-failed 1 'package example/broken' 420 "$tmp/build"

: >"$tmp/empty"
expect empty-output 1 'measurement did not happen' 420 "$tmp/empty"

printf 'ok  \texample/a\t1.0s\n' >"$tmp/one"
for n in 1 2 3 4 5 6 7; do printf 'ok  \texample/p%s\t%s.0s\n' "$n" "$n" >>"$tmp/one"; done
expect top5-truncated 0 'example/p7' 420 "$tmp/one"
rows="$("$guard" 420 "$tmp/one" 2>&1 | grep -c 'example/p' || true)"
if [ "$rows" -ne 5 ] || "$guard" 420 "$tmp/one" 2>&1 | grep -qE 'example/p[12]$'; then
  echo "FAIL top5-limit: printed $rows rows"; fails=$((fails + 1))
else
  echo "ok   top5-limit"
fi

if command -v jq >/dev/null; then
  printf '%s\n' \
    '{"Action":"pass","Package":"example/a","Test":"TestX","Elapsed":1}' \
    '{"Action":"pass","Package":"example/a","Elapsed":3.5}' \
    '{"Action":"pass","Package":"example/big","Elapsed":520}' >"$tmp/j.json"
  rc=0
  out="$("$guard" --json 600 "$tmp/j.json" 2>&1)" || rc=$?
  if [ "$rc" -eq 1 ] && grep -q 'example/big at 520s is over 85% of 600s' <<<"$out" && ! grep -q TestX <<<"$out"; then
    echo "ok   json"
  else
    echo "FAIL json rc=$rc"; echo "$out"; fails=$((fails + 1))
  fi
fi

# CHAOS-10333: a per-test limit under -race, measured from the race.json events. Top-level tests only; a subtest rolls
# up into its parent. The allowlist scripts/ci/race-slow-tests.tsv (package, test, limit_seconds, reason) lifts one test.
if command -v jq >/dev/null; then
  hdr='{"Action":"pass","Package":"example/a","Elapsed":3.5}'
  slow() { printf '%s\n' "$hdr" "{\"Action\":\"pass\",\"Package\":\"example/a\",\"Test\":\"$1\",\"Elapsed\":$2}" >"$tmp/pt.json"; }
  allow="$tmp/allow.tsv"
  printf 'package\ttest\tlimit_seconds\treason\n' >"$allow"
  ptexpect() { # name want_rc want_pattern [env assignment]
    local name="$1" want="$2" pat="$3" rc=0 out
    out="$(env RACE_SLOW_TESTS="$allow" ${4:+"$4"} "$guard" --json 6000 "$tmp/pt.json" 2>&1)" || rc=$?
    if [ "$rc" -ne "$want" ] || ! grep -q -- "$pat" <<<"$out"; then
      echo "FAIL $name: rc=$rc want=$want pattern='$pat'"; printf '    %s\n' "${out//$'\n'/$'\n    '}"
      fails=$((fails + 1))
    else
      echo "ok   $name"
    fi
  }
  slow TestHuge 310
  ptexpect per-test-over-limit-fails 1 'example/a TestHuge ran 310s under -race, over the 300s per-test limit'
  slow TestUnder 290
  ptexpect per-test-under-limit-passes 0 'example/a TestUnder 290s'
  slow TestHuge 310
  ptexpect per-test-limit-env 0 'TestHuge' RACE_TEST_LIMIT_SECONDS=400
  printf '%s\n' "$hdr" '{"Action":"pass","Package":"example/a","Test":"TestParent","Elapsed":100}' \
    '{"Action":"pass","Package":"example/a","Test":"TestParent/sub","Elapsed":900}' >"$tmp/pt.json"
  ptexpect subtest-rolls-into-parent 0 'TestParent 100s'
  slow TestHuge 310
  printf 'example/a\tTestHuge\t400\tmeasured and reviewed\n' >>"$allow"
  ptexpect allowlisted-test-passes 0 'TestHuge'
  slow TestHuge 410
  ptexpect allowlisted-test-over-its-own-limit-fails 1 'over the 400s per-test limit'
  : >"$tmp/pt.json"
  printf '%s\n' "$hdr" >"$tmp/pt.json"
  ptexpect no-test-level-events-fails 1 'no test-level events'
  slow TestHuge 5
  printf 'package\ttest\tlimit_seconds\treason\nexample/a\tTestHuge\tnotanumber\tx\n' >"$allow.bad"
  rc=0; RACE_SLOW_TESTS="$allow.bad" "$guard" --json 6000 "$tmp/pt.json" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 2 ]; then echo "ok   malformed-allowlist-rc2"; else echo "FAIL malformed-allowlist-rc2 rc=$rc"; fails=$((fails + 1)); fi
  printf 'package\ttest\tlimit_seconds\treason\nexample/a\tTestHuge\t400\t\n' >"$allow.bad2"
  rc=0; RACE_SLOW_TESTS="$allow.bad2" "$guard" --json 6000 "$tmp/pt.json" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 2 ]; then echo "ok   allowlist-empty-reason-rc2"; else echo "FAIL allowlist-empty-reason-rc2 rc=$rc"; fails=$((fails + 1)); fi
  slow TestHuge 5
  ptexpect byte-bound-fails 1 'over the .* byte bound' RACE_JSON_MAX_BYTES=10
  slow TestHuge 5
  ptexpect slowest-band-printed 0 'slowest top-level tests' RACE_TEST_BAND_SECONDS=1
  # the two shapes the ticket names: an untagged proof with local constants or helpers is a duration, not a name
  slow TestSomeLocalConstantProof 600
  ptexpect local-constant-proof-caught-by-duration 1 'TestSomeLocalConstantProof'
  slow TestViaUntaggedHelperProof 900
  ptexpect untagged-helper-proof-caught-by-duration 1 'TestViaUntaggedHelperProof'
  # r1 findings: a slow SKIPPED test counts; an unreadable duration record fails closed; the allowlist needs its header
  : >"$tmp/pt.json"
  printf '%s\n' "$hdr" '{"Action":"pass","Package":"example/a","Test":"TestFast","Elapsed":1}' \
    '{"Action":"skip","Package":"example/a","Test":"TestSlowSkip","Elapsed":900}' >"$tmp/pt.json"
  ptexpect slow-skipped-test-fails 1 'example/a TestSlowSkip ran 900s under -race'
  printf '%s\n' "$hdr" '{"Action":"pass","Package":"example/a","Test":"TestFast","Elapsed":1}' \
    '{"Action":"pass","Package":"example/a","Test":"TestSlow","Elapsed":900' >"$tmp/pt.json"
  ptexpect truncated-record-fails-closed 1 'unreadable record'
  printf '%s\n' "$hdr" '{"Action":"pass","Package":"example/a","Test":"TestFast","Elapsed":1}' \
    '{"Action":"pass","Package":"example/a","Test":"TestSlow","Elapsed":"not-a-number"}' >"$tmp/pt.json"
  ptexpect non-numeric-elapsed-fails-closed 1 'unreadable record'
  printf '%s\n' "$hdr" '{"Action":"pass","Package":"example/a","Test":"TestFast","Elapsed":1}' \
    '{"Action":"fail","Package":"example/a","Test":"TestSlow"}' >"$tmp/pt.json"
  ptexpect missing-elapsed-fails-closed 1 'unreadable record'
  printf '%s\n' 'go test -race -json example/a' "$hdr" '{"Action":"pass","Package":"example/a","Test":"TestFast","Elapsed":1}' >"$tmp/pt.json"
  ptexpect leading-non-json-line-is-allowed 0 'slowest top-level tests'
  slow TestA 450
  printf 'example/a\tTestA\t500\tmissing header\n' >"$allow.nohdr"
  rc=0; RACE_SLOW_TESTS="$allow.nohdr" "$guard" --json 6000 "$tmp/pt.json" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 2 ]; then echo "ok   headerless-allowlist-rc2"; else echo "FAIL headerless-allowlist-rc2 rc=$rc"; fails=$((fails + 1)); fi
  : >"$allow.empty"
  rc=0; RACE_SLOW_TESTS="$allow.empty" "$guard" --json 6000 "$tmp/pt.json" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -eq 2 ]; then echo "ok   empty-allowlist-rc2"; else echo "FAIL empty-allowlist-rc2 rc=$rc"; fails=$((fails + 1)); fi
fi

[ "$fails" -eq 0 ]
