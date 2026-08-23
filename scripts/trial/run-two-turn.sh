#!/usr/bin/env bash
# Usage: run-two-turn.sh <oracle-annex-path> [limit]
#
# CHAOS-3742 acceptance debt: runs the two-turn confirmation replay
# instrument (TestChaos3742TwoTurnConfirmationReplay, internal/runtime/hosted)
# against the frozen corpus, answered by run-responder-codex.sh -- the SAME
# subscription `codex exec` subprocess run-replay.sh already uses, never a
# metered OPENAI_API_KEY (standing rule for this epic).
#
# The oracle annex (design brief DP10) is REQUIRED and is NOT defaulted here
# -- it is a chris-ratified artifact, never guessed at by a script. Pass the
# absolute path to the chris-signed annex, e.g.
# .remember/trial-results/oracle-annex-v1.json (provenance.signoff.status
# must be "APPROVED" with a non-empty "by" -- the go test's loader adapts
# that real, on-disk schema directly; an annex whose nested signoff block
# is not APPROVED makes the go test itself refuse to run,
# requireAnnexSignedOff).
#
# Mirrors run-replay.sh's exchange-dir lifecycle exactly (start the responder
# before the go test, wait for the test to finish, signal DONE, wait for the
# responder to notice and wipe its own private CODEX_HOME).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ANNEX_PATH="${1:?usage: run-two-turn.sh <oracle-annex-path> [limit]}"
LIMIT="${2:-}"

if [[ ! -f "$ANNEX_PATH" ]]; then
  echo "run-two-turn.sh: oracle annex not found at $ANNEX_PATH" >&2
  exit 1
fi

trial_wire_common_env
# CHAOS-4100 (post-4108-fix graph rebuild incident): this script's own
# consumer, TestChaos3742TwoTurnConfirmationReplay, is the SECOND trial
# test (after chaos3884_replay_harness_test.go) to record
# ResolvedActiveEpoch/GraphLifecycleEnabled in its provenance -- see
# trial_wire_graph_lifecycle_env's own doc comment (common.sh) for why this
# is called here specifically rather than folded into trial_wire_common_env.
# Without this, the harness silently reads the bare legacy epoch-0 graph
# key even when the org has a live, rebuilt epoch (the exact incident that
# blocked CHAOS-4100's rerun #2 for two attempts).
trial_wire_graph_lifecycle_env
# ACR_TEST_TRIAL_ARM: the file-exchange runtime uses this value as the
# Model field in every persisted ModelExecutionReceipt (model_runtime.go);
# an unset value fails ModelExecutionReceipt.Validate() universally at
# turn 1 for EVERY case ("model receipt model is invalid" -- the live-run
# finding this line fixes, orchestrator ruling 2026-08-20). Every sibling
# script in this directory (run-replay.sh, run-arm5.sh, run-w0.sh, ...)
# already exports this; this script must be correct standalone, not
# correct-with-tribal-knowledge.
export ACR_TEST_TRIAL_ARM="twoturn"
export ACR_TEST_TWOTURN_ORACLE_ANNEX="$ANNEX_PATH"
: "${ACR_TEST_TWOTURN_OUT:=$ACR_TRIAL_RESULTS_DIR/gen-trial-chaos3742_twoturn-$(date -u +%Y%m%dT%H%M%SZ)-$$.json}"
export ACR_TEST_TWOTURN_OUT
if [[ -n "$LIMIT" ]]; then
  export ACR_TEST_TRIAL_LIMIT="$LIMIT"
fi
if [[ -n "${ACR_TRIAL_CORPUS_SHA256:-}" ]]; then
  export ACR_TEST_TRIAL_CORPUS_SHA256="$ACR_TRIAL_CORPUS_SHA256"
fi
# ACR_TEST_TRIAL_BASE_SHA: origin/main's tip this run's branch was rebased
# onto -- required provenance (team-lead ruling 2026-08-17), read directly
# from git rather than trusted to an operator-supplied value.
export ACR_TEST_TRIAL_BASE_SHA="$(cd "$repo_root" && git rev-parse origin/main)"

# $$ + timestamp (chaos3884's own collision lesson, applied identically
# here): two invocations started in the same second still get distinct
# exchange dirs and artifact paths.
exdir="$repo_root/.trial-exchange-twoturn-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "$exdir/requests" "$exdir/responses"
export ACR_TEST_TRIAL_EXCHANGE_DIR="$exdir"
export ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}"
# CHAOS-4113: explicit pass-through, not ambient inheritance -- unset by
# default, which leaves run-responder-codex.sh's own `codex exec` call with
# no `-m` flag (today's behavior, unchanged). See that script's own header
# for what setting this actually does and does not affect.
export ACR_TEST_TRIAL_RESPONDER_MODEL="${ACR_TEST_TRIAL_RESPONDER_MODEL:-}"
echo "EXCHANGE_DIR=$exdir"
echo "ORACLE_ANNEX=$ANNEX_PATH"

responder_log="$exdir/_responder_driver.log"
"$(dirname "${BASH_SOURCE[0]}")/run-responder-codex.sh" "$exdir" >"$responder_log" 2>&1 &
responder_pid=$!

cleanup() {
  touch "$exdir/DONE"
  wait "$responder_pid" 2>/dev/null || true
}
trap cleanup EXIT

set +e
( cd "$repo_root" && go test -run TestChaos3742TwoTurnConfirmationReplay -count=1 -v -timeout 6h ./internal/runtime/hosted )
status=$?
set -e

echo "TWO-TURN finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) exit=$status out=$ACR_TEST_TWOTURN_OUT"
exit "$status"
