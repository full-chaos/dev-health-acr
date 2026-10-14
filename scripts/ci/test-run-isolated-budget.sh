#!/usr/bin/env bash
set -euo pipefail

# Fixture test for run-isolated-budget.sh. RUNNER is a stub that prints a
# canned go test transcript, so no Go toolchain or container is needed.
# Run with GUARD=<other script> to prove the test goes red against a guard
# that lacks a rule.

here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0

stub() { # name rc body...
  local f="$tmp/$1.sh" rc="$2"; shift 2
  printf '#!/usr/bin/env bash\n' >"$f"
  for l in "$@"; do printf 'printf "%%s\\n" %q\n' "$l" >>"$f"; done
  printf 'exit %s\n' "$rc" >>"$f"
  chmod +x "$f"
}
expect() { # name want_rc want_pattern budget stub
  local name="$1" want="$2" pat="$3" budget="$4" r="$5" rc=0 out
  out="$(RUNNER="$r" GOTEST_TIMEOUT="${budget}s" "$here/run-isolated-budget.sh" t example/pkg 2>&1)" || rc=$?
  if [ "$rc" -ne "$want" ] || ! grep -q -- "$pat" <<<"$out"; then
    echo "FAIL $name: rc=$rc want=$want pattern='$pat'"; printf '    %s\n' "${out//$'\n'/$'\n    '}"
    fails=$((fails + 1))
  else
    echo "ok   $name"
  fi
}

# job 115633489526: wall 588s, package 505.6s, budget 600s: passes
stub real 0 $'ok  \texample/pkg\t505.6s'
expect real-figures-pass 0 'top 5 packages' 600 "$tmp/real.sh"
# 511s package time is over 85% of 600s (510s): fails
stub over 0 $'ok  \texample/pkg\t511.0s'
expect over-line-fails 1 'example/pkg at 511s is over 85% of 600s' 600 "$tmp/over.sh"
# no package time line: the measurement did not happen
stub absent 0 $'?   \texample/pkg\t[no test files]'
expect absent-time-fails 1 'measurement did not happen' 600 "$tmp/absent.sh"
stub empty 0
expect empty-output-fails 1 'measurement did not happen' 600 "$tmp/empty.sh"
# a test failure with an under-line time still fails
stub testfail 1 $'FAIL\texample/pkg\t100.0s'
expect test-failure-fails 1 'printed FAIL' 600 "$tmp/testfail.sh"
# runner exit non-zero but a passing-looking ok line: runner status wins
stub badrc 3 $'ok  \texample/pkg\t10.0s'
expect runner-status-wins 3 'top 5 packages' 600 "$tmp/badrc.sh"

rc=0
GOTEST_TIMEOUT=20m RUNNER="$tmp/real.sh" "$here/run-isolated-budget.sh" t example/pkg >"$tmp/m.out" 2>&1 || rc=$?
if [ "$rc" -eq 0 ] && grep -q 'budget 1200s' "$tmp/m.out"; then echo "ok   minutes-budget"; else echo "FAIL minutes-budget rc=$rc"; fails=$((fails + 1)); fi
rc=0
GOTEST_TIMEOUT='' RUNNER="$tmp/real.sh" "$here/run-isolated-budget.sh" t example/pkg >/dev/null 2>&1 || rc=$?
if [ "$rc" -eq 2 ]; then echo "ok   missing-budget-fails"; else echo "FAIL missing-budget rc=$rc"; fails=$((fails + 1)); fi

[ "$fails" -eq 0 ]
