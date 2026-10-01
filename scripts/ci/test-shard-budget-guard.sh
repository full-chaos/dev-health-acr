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

[ "$fails" -eq 0 ]
