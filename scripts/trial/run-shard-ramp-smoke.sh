#!/usr/bin/env bash
# Usage: run-shard-ramp-smoke.sh <oracle-annex-path> [steps...]   (default steps: 8 16 32)
#
# CHAOS-4100 ramp smoke. Finds the CONCURRENCY CEILING in a run whose
# failures are free, instead of discovering it during a measurement run.
#
# WHY THIS EXISTS. Per-case sharding makes wall time the slowest single
# case only if the fan-out can actually run that wide, and run-responder-
# codex.sh is one SEQUENTIAL responder per shard -- so N shards means N
# concurrent `codex exec` processes against ONE ChatGPT subscription. That
# number is a property of the subscription, not of this repo, so it has to
# be measured rather than assumed.
#
# NOT A MEASUREMENT RUN. Every artifact it produces is smoke evidence about
# THROUGHPUT. Per-case-sharded results are not verdict-bearing until the
# same-tip A/B against a coarse run has passed; until then the coarse shape
# remains the shape of record.
#
# THE RECORD IS THE POINT. Each step's wall time, shard failures and
# responder failures are written to a JSON record, so the ceiling is
# documented evidence rather than lore about "roughly sixteen".
set -euo pipefail

# count_responder_logs / count_responder_failures (codex xhigh review round
# 1... round 2, P2) count this step's responder driver logs and how many of
# them look unhealthy.
#
# THE BUG THEY REPLACE: the first version was
#   n=$(grep -rl PATTERN $(find ...) | wc -l)
# which fails two ways under `set -euo pipefail`, and BOTH on the healthy
# path. grep exits 1 when nothing matches -- the expected outcome when every
# responder is fine -- and pipefail propagates that through the pipeline into
# the assignment, so `set -e` killed the script before it could record the
# successful step. And when find matched nothing, the unquoted substitution
# left grep with NO file arguments, so it read stdin and blocked forever.
# A smoke whose only failure mode is succeeding is worse than no smoke.
#
# Reading with a while loop instead: no pipeline status to propagate, no
# word-splitting, and an empty set is simply zero.
#
# "Since" is a MARKER FILE, not `-newermt @epoch`. BSD find (the reference
# machine is macOS) cannot parse an @epoch timestamp -- it errors, the
# 2>/dev/null swallows it, and the count silently reads 0 for every step.
# A smoke that reports zero responder failures at every concurrency level
# whatever actually happened is precisely the lore this ticket is supposed
# to replace with evidence. `-newer <file>` works on BSD and GNU alike.
count_responder_logs() {
  local marker="$1" count=0 log
  while IFS= read -r log; do
    [[ -n "$log" ]] && count=$((count + 1))
  done < <(find "${TMPDIR:-/tmp}" -maxdepth 2 -path '*acr-trial-exchange-twoturn-parallel*' \
    -name '_responder_driver.log' -newer "$marker" 2>/dev/null || true)
  printf '%s' "$count"
}

count_responder_failures() {
  local marker="$1" count=0 log
  while IFS= read -r log; do
    [[ -z "$log" ]] && continue
    # A responder that had to be force-killed, or that reported a failure
    # answering a request, is the signal this smoke exists to find: at some
    # concurrency the subscription starts refusing and it shows up here
    # first.
    if grep -qiE 'did not exit within|failed|error' "$log" 2>/dev/null; then
      count=$((count + 1))
    fi
  done < <(find "${TMPDIR:-/tmp}" -maxdepth 2 -path '*acr-trial-exchange-twoturn-parallel*' \
    -name '_responder_driver.log' -newer "$marker" 2>/dev/null || true)
  printf '%s' "$count"
}

# --self-test exercises the counters against a fixture and exits, WITHOUT
# sourcing common.sh -- the counting is pure filesystem work and needs no
# credentials, and the bug above was invisible to every check that did not
# run the healthy path. Same self-test precedent as
# scripts/clients/test-real-clients.sh.
if [[ "${1:-}" == "--self-test" ]]; then
  fixture="$(mktemp -d)"
  trap 'rm -rf "$fixture"' EXIT
  export TMPDIR="$fixture"
  marker="$fixture/.marker"
  touch -t 200001010000 "$marker"
  failures=0
  expect() {
    if [[ "$2" != "$3" ]]; then
      echo "FAIL: $1 = $3, want $2" >&2
      failures=$((failures + 1))
    else
      echo "ok: $1 = $3"
    fi
  }
  # (a) no logs at all -- the empty-find case that used to BLOCK on stdin.
  expect "no logs -> 0 logs" 0 "$(count_responder_logs "$marker")"
  expect "no logs -> 0 failures" 0 "$(count_responder_failures "$marker")"
  # (b) healthy logs -- the no-match case that used to KILL the script.
  mkdir -p "$fixture/acr-trial-exchange-twoturn-parallel-x-0"
  echo "responder: DONE, every published request answered" >"$fixture/acr-trial-exchange-twoturn-parallel-x-0/_responder_driver.log"
  expect "healthy -> 1 log" 1 "$(count_responder_logs "$marker")"
  expect "healthy -> 0 failures" 0 "$(count_responder_failures "$marker")"
  # (c) an unhealthy log is actually counted.
  mkdir -p "$fixture/acr-trial-exchange-twoturn-parallel-x-1"
  echo "responder: pid 123 did not exit within 300s" >"$fixture/acr-trial-exchange-twoturn-parallel-x-1/_responder_driver.log"
  expect "one bad -> 2 logs" 2 "$(count_responder_logs "$marker")"
  expect "one bad -> 1 failure" 1 "$(count_responder_failures "$marker")"
  if [[ "$failures" -gt 0 ]]; then
    echo "ramp-smoke self-test FAILED ($failures)" >&2
    exit 1
  fi
  echo "ramp-smoke self-test passed"
  exit 0
fi

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ANNEX_PATH="${1:?usage: run-shard-ramp-smoke.sh <oracle-annex-path> [steps...]}"
shift || true
STEPS=("$@")
[[ "${#STEPS[@]}" -eq 0 ]] && STEPS=(8 16 32)

launcher="$(dirname "${BASH_SOURCE[0]}")/run-two-turn-parallel.sh"
stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
record="${ACR_TRIAL_RESULTS_DIR:-/tmp}/shard-ramp-smoke-${stamp}.json"

echo "ramp smoke: steps=${STEPS[*]} annex=$ANNEX_PATH record=$record"
printf '{"stamp":"%s","annex":"%s","steps":[' "$stamp" "$ANNEX_PATH" >"$record"

first=1
for cap in "${STEPS[@]}"; do
  echo "=== concurrency step: $cap ==="
  started="$(date +%s)"
  step_marker="$(mktemp)"
  set +e
  ACR_TRIAL_CASES_PER_SHARD=1 ACR_TRIAL_MAX_CONCURRENT_SHARDS="$cap" \
    "$launcher" "$ANNEX_PATH"
  step_status=$?
  set -e
  elapsed=$(($(date +%s) - started))

  # Responder health is counted from the driver logs this step produced.
  # A responder that force-exited or failed to answer every published
  # request is the SIGNAL this smoke exists to find -- at some concurrency
  # the subscription starts refusing, and that shows up here first.
  responder_attempts="$(count_responder_logs "$step_marker")"
  responder_failures="$(count_responder_failures "$step_marker")"
  rm -f "$step_marker"

  [[ "$first" -eq 0 ]] && printf ',' >>"$record"
  first=0
  printf '{"concurrency":%d,"wall_seconds":%d,"launcher_exit":%d,"responder_attempts":%d,"responder_failures":%d}' \
    "$cap" "$elapsed" "$step_status" "$responder_attempts" "$responder_failures" >>"$record"

  echo "step cap=$cap wall=${elapsed}s exit=$step_status responders=$responder_attempts failures=$responder_failures"
  if [[ "$step_status" -ne 0 && "$step_status" -ne 3 ]]; then
    echo "run-shard-ramp-smoke.sh: step cap=$cap failed (exit $step_status) -- stopping the ramp here; the ceiling is at or below this step" >&2
    break
  fi
done

printf ']}\n' >>"$record"
echo "ramp smoke record written to $record"
echo "REMINDER: throughput evidence only. Per-case-sharded results are not verdict-bearing until the same-tip A/B against a coarse run has passed."
