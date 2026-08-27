#!/usr/bin/env bash
# Usage: run-n-turn.sh <oracle-annex-path> <comma-separated-case-indices> [max-turns]
#
# CHAOS-4360 (harness half, "conversations lack memory"): runs
# TestChaos4360NTurnConfirmationCarry, the N-turn window+candidate-carry
# case class, against a SMALL EXPLICIT seed set of case indices -- never the
# full corpus (the go test itself refuses more than 20 indices; see its own
# ACR_TEST_NTURN_CASE_INDICES doc comment). This is the harness's own
# RED-baseline / measurement instrument for the live defect CHAOS-4355's
# 13:40 08-27 walkthrough found (turn 3 arrives with an inferred window --
# nothing carries a confirmed window across turns server-side -- so a
# project/repository-candidate question can never reach a decisive answer
# past two turns). Mirrors run-two-turn.sh's own lifecycle (exchange dir,
# responder subprocess, DONE signal, transport readiness check) exactly --
# see that script's own header for the mechanics this one does not
# re-explain.
#
# example: pass the project-kind seed cases this class was built from
# (idx 57/60, the same "project" positive-arm class the two-turn harness's
# own subject_anchor arm already exercises):
#   scripts/trial/run-n-turn.sh .remember/trial-results/oracle-annex-v2-ext65.json 57,60
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ANNEX_PATH="${1:?usage: run-n-turn.sh <oracle-annex-path> <comma-separated-case-indices> [max-turns]}"
CASE_INDICES="${2:?usage: run-n-turn.sh <oracle-annex-path> <comma-separated-case-indices> [max-turns]}"
MAX_TURNS="${3:-}"

if [[ ! -f "$ANNEX_PATH" ]]; then
  echo "run-n-turn.sh: oracle annex not found at $ANNEX_PATH" >&2
  exit 1
fi

trial_wire_common_env
# CHAOS-4313 red-first fail-closed check -- same discipline run-two-turn.sh
# applies before touching an exchange dir or starting a go test.
trial_require_responder_transport_ready
trial_wire_graph_lifecycle_env
# ACR_TEST_TRIAL_ARM: file-exchange transport uses this as the Model field
# of every persisted ModelExecutionReceipt (model_runtime.go) -- see
# run-two-turn.sh's own comment on this line for the exact failure an unset
# value produces. "nturn" keeps this run's own exchange traffic in a
# clearly-labeled, non-colliding namespace from "twoturn" runs sharing the
# same host.
export ACR_TEST_TRIAL_ARM="nturn"
export ACR_TEST_NTURN_ORACLE_ANNEX="$ANNEX_PATH"
export ACR_TEST_NTURN_CASE_INDICES="$CASE_INDICES"
if [[ -n "$MAX_TURNS" ]]; then
  export ACR_TEST_NTURN_MAX_TURNS="$MAX_TURNS"
fi
: "${ACR_TEST_NTURN_OUT:=$ACR_TRIAL_RESULTS_DIR/gen-trial-chaos4360_nturn-$(date -u +%Y%m%dT%H%M%SZ)-$$.json}"
export ACR_TEST_NTURN_OUT
if [[ -n "${ACR_TRIAL_CORPUS_SHA256:-}" ]]; then
  export ACR_TEST_TRIAL_CORPUS_SHA256="$ACR_TRIAL_CORPUS_SHA256"
fi

# $$ + timestamp (chaos3884/twoturn's own collision lesson, applied
# identically here): two invocations started in the same second still get
# distinct exchange dirs and artifact paths.
exdir="$repo_root/.trial-exchange-nturn-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "$exdir/requests" "$exdir/responses"
export ACR_TEST_TRIAL_EXCHANGE_DIR="$exdir"
export ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}"
export ACR_TEST_TRIAL_RESPONDER_MODEL="$(trial_responder_model)"
export ACR_TEST_TRIAL_RESPONDER_EFFORT="$(trial_responder_effort)"
echo "EXCHANGE_DIR=$exdir"
echo "ORACLE_ANNEX=$ANNEX_PATH"
echo "CASE_INDICES=$CASE_INDICES"

responder_log="$exdir/_responder_driver.log"
"$(trial_responder_script)" "$exdir" >"$responder_log" 2>&1 &
responder_pid=$!

cleanup() {
  touch "$exdir/DONE"
  wait "$responder_pid" 2>/dev/null || true
}
trap cleanup EXIT

# Package -timeout SIZED TO THE CONFIGURED BUDGET (codex review round 3,
# P2, confirmed): a fixed "2h" undercounted the harness's own worst-case
# aggregate -- each Investigate call in chaos4360_nturn_confirmation_test.go
# is budgeted 2*ACR_TEST_TRIAL_EXCHANGE_TIMEOUT+30s (mirrors run-two-turn.sh's
# own caseTimeout formula), and this class can make up to (case count ×
# max turns) such calls. Undercounting means `go test` can kill a slow but
# otherwise VALID run before the report is ever written -- a false timeout
# failure that reads as "the harness is broken" when it is only slow.
# exchange_timeout_to_seconds parses the subset of Go duration syntax this
# repo's trial scripts ever actually pass here (Nh, Nm, Ns, or a bare
# combination like "1h30m") -- not a general parser, just enough to size a
# safety-margin timeout, never to interpret the value semantically.
exchange_timeout_to_seconds() {
  local spec="$1" total=0 num unit
  while [[ -n "$spec" ]]; do
    if [[ "$spec" =~ ^([0-9]+)(h|m|s) ]]; then
      num="${BASH_REMATCH[1]}"; unit="${BASH_REMATCH[2]}"
      case "$unit" in
        h) total=$((total + num * 3600)) ;;
        m) total=$((total + num * 60)) ;;
        s) total=$((total + num)) ;;
      esac
      spec="${spec#"${num}${unit}"}"
    else
      echo "run-n-turn.sh: cannot parse ACR_TRIAL_EXCHANGE_TIMEOUT segment %q" >&2
      total=600
      break
    fi
  done
  echo "$total"
}
exchange_timeout_seconds="$(exchange_timeout_to_seconds "$ACR_TEST_TRIAL_EXCHANGE_TIMEOUT")"
per_call_timeout_seconds=$((2 * exchange_timeout_seconds + 30))
case_count=$(($(tr -cd ',' <<<"$CASE_INDICES" | wc -c) + 1))
turns_budget="${MAX_TURNS:-5}"
# +600s fixed overhead margin: annex/corpus load, the epoch resolver's own
# live read, report marshal+write -- none of these are per-Investigate-call
# work, so they are not covered by the per-call multiplication above.
package_timeout_seconds=$((case_count * turns_budget * per_call_timeout_seconds + 600))
echo "PACKAGE_TIMEOUT=${package_timeout_seconds}s (case_count=$case_count max_turns=$turns_budget per_call_timeout=${per_call_timeout_seconds}s)"

set +e
( cd "$repo_root" && go test -run TestChaos4360NTurnConfirmationCarry -count=1 -v -timeout "${package_timeout_seconds}s" ./internal/runtime/hosted )
status=$?
set -e

echo "N-TURN finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) exit=$status out=$ACR_TEST_NTURN_OUT"
exit "$status"
