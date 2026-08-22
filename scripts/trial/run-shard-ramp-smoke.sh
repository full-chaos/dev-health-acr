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
  logs=$(find "${TMPDIR:-/tmp}" -maxdepth 1 -name '_responder_driver.log' -newermt "@$started" 2>/dev/null | wc -l | tr -d ' ')
  responder_attempts=$(find "${TMPDIR:-/tmp}" -maxdepth 2 -path '*acr-trial-exchange-twoturn-parallel*' -name '_responder_driver.log' -newermt "@$started" 2>/dev/null | wc -l | tr -d ' ')
  responder_failures=$(grep -rl "did not exit within\|failed\|error" $(find "${TMPDIR:-/tmp}" -maxdepth 2 -path '*acr-trial-exchange-twoturn-parallel*' -name '_responder_driver.log' -newermt "@$started" 2>/dev/null) 2>/dev/null | wc -l | tr -d ' ')

  [[ "$first" -eq 0 ]] && printf ',' >>"$record"
  first=0
  printf '{"concurrency":%d,"wall_seconds":%d,"launcher_exit":%d,"responder_logs":%d,"responder_attempts":%d,"responder_failures":%d}' \
    "$cap" "$elapsed" "$step_status" "$logs" "$responder_attempts" "$responder_failures" >>"$record"

  echo "step cap=$cap wall=${elapsed}s exit=$step_status responders=$responder_attempts failures=$responder_failures"
  if [[ "$step_status" -ne 0 && "$step_status" -ne 3 ]]; then
    echo "run-shard-ramp-smoke.sh: step cap=$cap failed (exit $step_status) -- stopping the ramp here; the ceiling is at or below this step" >&2
    break
  fi
done

printf ']}\n' >>"$record"
echo "ramp smoke record written to $record"
echo "REMINDER: throughput evidence only. Per-case-sharded results are not verdict-bearing until the same-tip A/B against a coarse run has passed."
