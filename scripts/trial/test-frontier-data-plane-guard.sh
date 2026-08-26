#!/usr/bin/env bash
# CHAOS-4220: pins run-frontier-arm.sh's ACR_TRIAL_DATA_PLANE guard --
# see that script's own doc comment for the incident class this closes
# (the STANDING DEFAULT is kiac, chris's order, but this harness's
# ClickHouse access is docker-exec-shaped and cannot reach kiac; before
# this fix the script silently ignored the switch and always read the
# compose container regardless of what the operator expected).
#
# Offline only, no live infra: exercises just the guard itself, which
# fires before ANY live requirement (ARM/MODEL args, codex/jq on PATH,
# a real ClickHouse container) -- the guard's OWN job is proven here, not
# anything past it. Needs ops/.env (same requirement every other
# non-plan-only script in this directory has, since common.sh's own
# dev_health_root resolution hard-exits without it) -- not a NEW
# dependency this test introduces.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
launcher="$script_dir/run-frontier-arm.sh"

failures=0
check() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $label" >&2
    echo "  want: $want" >&2
    echo "  got:  $got" >&2
    failures=$((failures + 1))
  else
    echo "ok: $label"
  fi
}

# 1. The STANDING DEFAULT (ACR_TRIAL_DATA_PLANE unset -> common.sh's own
# `: "${ACR_TRIAL_DATA_PLANE:=kiac}"` resolves it to kiac) must be
# refused, not silently read compose -- this IS the incident class the
# guard exists to close. `env -u` explicitly drops any ACR_TRIAL_DATA_PLANE
# this test's OWN caller might have exported ambiently (codex R2, real
# Low: an earlier version of this test relied on bash's own subshell not
# inheriting one, which is true for a bare interactive shell but not
# guaranteed for every caller) -- "unset" here means unset, not "whatever
# happened to already be in the environment".
out="$(env -u ACR_TRIAL_DATA_PLANE bash "$launcher" 2>&1)" && rc=0 || rc=$?
check "unset ACR_TRIAL_DATA_PLANE (kiac default) is refused: exit status" "1" "$rc"
case "$out" in
*"ACR_TRIAL_DATA_PLANE=kiac"*"only supports 'compose'"*) check "message names kiac and the compose-only limitation" "yes" "yes" ;;
*) check "message names kiac and the compose-only limitation" "yes" "no (got: $out)" ;;
esac

# 2. Explicit ACR_TRIAL_DATA_PLANE=kiac -- same refusal, not just the
# unset/default path (an operator who explicitly asked for kiac gets the
# same clear answer as one who never set anything).
rc2=0
ACR_TRIAL_DATA_PLANE=kiac bash "$launcher" >/dev/null 2>&1 || rc2=$?
check "explicit ACR_TRIAL_DATA_PLANE=kiac is refused: exit status" "1" "$rc2"

# 3. Explicit ACR_TRIAL_DATA_PLANE=compose must NOT trip the guard -- the
# script must reach PAST it, to the (unmet, in this offline test) ARM
# argument requirement, whose distinct error text proves the guard itself
# never fired (a false positive here would block every real compose run).
out3="$(ACR_TRIAL_DATA_PLANE=compose bash "$launcher" 2>&1)" || true
case "$out3" in
*"only supports 'compose'"*) check "ACR_TRIAL_DATA_PLANE=compose never trips the guard" "no false positive" "FALSE POSITIVE: $out3" ;;
*"arm label required"*) check "ACR_TRIAL_DATA_PLANE=compose never trips the guard" "no false positive" "no false positive" ;;
*) check "ACR_TRIAL_DATA_PLANE=compose never trips the guard" "no false positive" "unexpected output: $out3" ;;
esac

if [[ "$failures" -gt 0 ]]; then
  echo "frontier-data-plane-guard checks FAILED ($failures)" >&2
  exit 1
fi
echo "frontier-data-plane-guard checks passed"
