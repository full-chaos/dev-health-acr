#!/usr/bin/env bash
set -euo pipefail

# Fixture test for shard-budget-guard.sh. Run with GUARD=<other script> to
# prove the test goes red against a guard that lacks a rule.

here="$(cd "$(dirname "$0")" && pwd)"
guard="${GUARD:-$here/shard-budget-guard.sh}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0

# CHAOS-10963: the JSON cases below need jq; a missing jq is a failure, not a skip.
if ! command -v jq >/dev/null; then
  echo "FAIL jq-present: jq is required for the JSON cases"; fails=$((fails + 1))
fi

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
  ptexpect slowest-band-printed 0 'example/a TestHuge 5s' RACE_TEST_BAND_SECONDS=1
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

# CHAOS-12667: real span report. A parent whose parallel subtests outlive its own function reports a small Elapsed, so the
# guard names the slowest tests by (last event of T or T/...) - (T's last run/cont). Reporting only: the exit code is the
# one the package and per-test checks give.
if command -v jq >/dev/null; then
  ev() { # time action test [elapsed]; time is seconds after 19:25:00 with a fraction, e.g. 57.5848234
    local t test_field=""
    t="$(printf '2026-11-02T19:%02d:%09.7fZ' $(( ${1%.*} / 60 )) "$(awk -v x="$1" 'BEGIN { printf "%.7f", x - 60 * int(x / 60) }')")"
    [ -n "${3:-}" ] && test_field=",\"Test\":\"$3\""
    printf '{"Time":"%s","Action":"%s","Package":"example/a"%s%s}\n' "$t" "$2" "$test_field" "${4:+,\"Elapsed\":$4}"
  }
  sp="$tmp/span.json"
  spexpect() { # name want_rc want_pattern [env assignment]
    local name="$1" want="$2" pat="$3" rc=0 out
    out="$(env RACE_SLOW_TESTS="$allow" ${4:+"$4"} "$guard" --json 6000 "$sp" 2>&1)" || rc=$?
    if [ "$rc" -ne "$want" ] || ! grep -q -- "$pat" <<<"$out"; then
      echo "FAIL $name: rc=$rc want=$want pattern='$pat'"; printf '    %s\n' "${out//$'\n'/$'\n    '}"
      fails=$((fails + 1))
    else
      echo "ok   $name"
    fi
  }
  # parent passes at 1.0s (Elapsed 1) while its two parallel subtests run until 11.5s after the parent started
  { ev 0.5 run TestP; ev 0.6 run TestP/a; ev 0.7 pause TestP/a; ev 0.8 run TestP/b; ev 0.9 pause TestP/b
    ev 1.0 pass TestP 1; ev 1.1 cont TestP/a; ev 1.2 cont TestP/b; ev 11.4 pass TestP/a 10.3; ev 11.5 pass TestP/b 10.3
    ev 11.6 pass "" 11.6; } >"$sp"
  spexpect span-counts-parallel-subtests 0 'span *11\.0 *elapsed *1\.0 *TestP$'
  spexpect span-header-names-the-package 0 'by real span (s), reported Elapsed beside it \[example/a\]'
  # a paused top-level parallel test starts at its cont: run at 0, pause, cont at 20, pass at 25 -> span 5
  { ev 0 run TestPaused; ev 0.1 pause TestPaused; ev 20 cont TestPaused; ev 25 pass TestPaused 5; ev 26 pass "" 26; } >"$sp"
  spexpect paused-test-starts-at-cont 0 'span *5\.0 *elapsed *5\.0 *TestPaused$'
  # sub-second precision is kept
  { ev 0.0000001 run TestFine; ev 0.5848234 pass TestFine 0.58; ev 1 pass "" 1; } >"$sp"
  spexpect sub-second-precision 0 'span *0\.6 *elapsed *0\.6 *TestFine$'
  # the report never changes the exit code: a failing per-test limit still fails, with the report printed
  { ev 0 run TestGiant; ev 310 pass TestGiant 310; ev 311 pass "" 311; } >"$sp"
  spexpect report-printed-when-guard-fails 1 'span *310\.0 *elapsed *310\.0 *TestGiant$'
  # baseline: package and per-test deltas, and a test absent in the baseline is new
  { ev 0 run TestP; ev 10 pass TestP 1; ev 0 run TestOld; ev 3 pass TestOld 3; ev 11 pass "" 11; } >"$tmp/base.json"
  { ev 0 run TestP; ev 14 pass TestP 1; ev 0 run TestNew; ev 2 pass TestNew 2; ev 0 run TestOld; ev 3 pass TestOld 3; ev 20 pass "" 20; } >"$sp"
  spexpect baseline-delta-per-test 0 'span *14\.0 *elapsed *1\.0 *TestP *| baseline *10\.0 *delta +4\.0' RACE_BASELINE_JSON="$tmp/base.json"
  spexpect baseline-unchanged-test-delta-zero 0 'TestOld *| baseline *3\.0 *delta +0\.0' RACE_BASELINE_JSON="$tmp/base.json"
  spexpect baseline-absent-test-is-new 0 'TestNew .*| new' RACE_BASELINE_JSON="$tmp/base.json"
  spexpect baseline-package-delta 0 'package time: now 20\.0s, baseline 11\.0s, delta +9\.0s' RACE_BASELINE_JSON="$tmp/base.json"
  spexpect baseline-unreadable-file-warns 0 '::warning::shard-budget-guard: no main baseline for /nonexistent' RACE_BASELINE_JSON=/nonexistent
  spexpect no-baseline-says-so 0 'no baseline set (RACE_BASELINE_JSON)'
  # an unreadable baseline file never changes the exit code: a failing input still fails, with the warning
  { ev 0 run TestGiant; ev 310 pass TestGiant 310; ev 311 pass "" 311; } >"$sp"
  spexpect baseline-missing-failing-input-rc1 1 '::warning::shard-budget-guard: no main baseline for /nonexistent' RACE_BASELINE_JSON=/nonexistent
  # Codex #1967 r1: the report's temporary directory failing (mktemp -d) is a warning, never an exit: a passing input
  # stays 0 and a failing input stays 1. The shim fails only `mktemp -d`; the guard's own plain mktemp calls work.
  mkdir -p "$tmp/nodir"
  cat >"$tmp/nodir/mktemp" <<'SHIM'
#!/bin/sh
for a in "$@"; do [ "$a" = -d ] && { echo "mktemp: no directory" >&2; exit 1; }; done
exec /usr/bin/mktemp "$@"
SHIM
  chmod +x "$tmp/nodir/mktemp"
  { ev 0 run TestP; ev 14 pass TestP 1; ev 20 pass "" 20; } >"$sp"
  rc_nd=0; PATH="$tmp/nodir:$PATH" RACE_SLOW_TESTS="$allow" "$guard" --json 6000 "$sp" >"$tmp/nd.out" 2>&1 || rc_nd=$?
  if [ "$rc_nd" -eq 0 ] && grep -q 'no temporary directory for the real span report' "$tmp/nd.out"; then echo "ok   report-tempdir-failure-passing-input-rc0"
  else echo "FAIL report-tempdir-failure-passing-input-rc0 rc=$rc_nd"; cat "$tmp/nd.out"; fails=$((fails + 1)); fi
  { ev 0 run TestGiant; ev 310 pass TestGiant 310; ev 311 pass "" 311; } >"$sp"
  rc_nd=0; PATH="$tmp/nodir:$PATH" RACE_SLOW_TESTS="$allow" "$guard" --json 6000 "$sp" >"$tmp/nd.out" 2>&1 || rc_nd=$?
  if [ "$rc_nd" -eq 1 ] && grep -q 'no temporary directory for the real span report' "$tmp/nd.out"; then echo "ok   report-tempdir-failure-failing-input-rc1"
  else echo "FAIL report-tempdir-failure-failing-input-rc1 rc=$rc_nd"; cat "$tmp/nd.out"; fails=$((fails + 1)); fi
  # Codex #1967 r2: a failing cleanup never changes the result. The shim fails every `rm`; the guard still exits with
  # the status it reached (0 on a passing input, 1 on a failing one).
  mkdir -p "$tmp/norm"
  printf '#!/bin/sh\necho "rm: denied" >&2\nexit 1\n' >"$tmp/norm/rm"; chmod +x "$tmp/norm/rm"
  { ev 0 run TestP; ev 14 pass TestP 1; ev 20 pass "" 20; } >"$sp"
  rc_rm=0; PATH="$tmp/norm:$PATH" RACE_SLOW_TESTS="$allow" "$guard" --json 6000 "$sp" >"$tmp/rm.out" 2>&1 || rc_rm=$?
  if [ "$rc_rm" -eq 0 ]; then echo "ok   failing-cleanup-passing-input-rc0"
  else echo "FAIL failing-cleanup-passing-input-rc0 rc=$rc_rm"; cat "$tmp/rm.out"; fails=$((fails + 1)); fi
  { ev 0 run TestGiant; ev 310 pass TestGiant 310; ev 311 pass "" 311; } >"$sp"
  rc_rm=0; PATH="$tmp/norm:$PATH" RACE_SLOW_TESTS="$allow" "$guard" --json 6000 "$sp" >"$tmp/rm.out" 2>&1 || rc_rm=$?
  if [ "$rc_rm" -eq 1 ]; then echo "ok   failing-cleanup-failing-input-rc1"
  else echo "FAIL failing-cleanup-failing-input-rc1 rc=$rc_rm"; cat "$tmp/rm.out"; fails=$((fails + 1)); fi
  # Codex #1967 r2: a test with no end event (a cut-off transcript) is a lower bound, and says so.
  { ev 0 run TestDone; ev 1 pass TestDone 1; ev 2 run TestHung; ev 10 output TestHung; } >"$sp"
  spexpect unfinished-test-is-a-lower-bound 1 'TestHung  (no end event: ran at least this long)'
fi

[ "$fails" -eq 0 ]
