#!/usr/bin/env bash
# Usage: run-two-turn.sh <oracle-annex-path> [limit]
#
# CHAOS-3742 acceptance debt: runs the two-turn confirmation replay
# instrument (TestChaos3742TwoTurnConfirmationReplay, internal/runtime/hosted)
# against the frozen corpus, answered via ACR_TEST_TRIAL_RESPONDER_TRANSPORT
# (common.sh's trial_responder_script) -- "api" by default (CHAOS-4313,
# chris ruling 2026-08-26: a direct OpenAI API call, cmd/acr-trial-
# responder-api, metered OPENAI_API_KEY spend now expected), or "codex" for
# the SAME subscription `codex exec` subprocess run-replay.sh still uses,
# retained only for replaying historical runs.
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
# responder to notice and exit -- run-responder-api.sh has no CODEX_HOME to
# wipe; run-responder-codex.sh's own private-CODEX_HOME cleanup is unchanged
# under transport=codex).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ANNEX_PATH="${1:?usage: run-two-turn.sh <oracle-annex-path> [limit]}"
LIMIT="${2:-}"

if [[ ! -f "$ANNEX_PATH" ]]; then
  echo "run-two-turn.sh: oracle annex not found at $ANNEX_PATH" >&2
  exit 1
fi

trial_wire_common_env
# CHAOS-4313 red-first fail-closed check: before ANYTHING else touches an
# exchange dir or starts a go test that could publish a request, refuse
# loudly if the selected responder transport cannot actually answer (a
# missing OPENAI_API_KEY for transport=api, a missing codex login for
# transport=codex).
trial_require_responder_transport_ready
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
# ACR_TEST_TRIAL_BASE_SHA (CHAOS-4157 fix-forward, 2026-08-23): DROPPED.
# Used to export `git rev-parse origin/main` here for the report's own
# BaseSHA field -- a genuine provenance defect, caught live: origin/main
# can move mid-run, so the value could name a commit that never actually
# produced the artifact. The report now stamps BaseSHA from
# requireGitSourceIdentity's own `git rev-parse HEAD` (the SAME value
# SourceCommit already used) instead, so this export has no reader left.

# $$ + timestamp (chaos3884's own collision lesson, applied identically
# here): two invocations started in the same second still get distinct
# exchange dirs and artifact paths.
exdir="$repo_root/.trial-exchange-twoturn-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "$exdir/requests" "$exdir/responses"
export ACR_TEST_TRIAL_EXCHANGE_DIR="$exdir"
export ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}"
# CHAOS-4113: explicit pass-through, not ambient inheritance -- unset by
# default under transport=codex, which leaves run-responder-codex.sh's own
# `codex exec` call with no `-m` flag (today's behavior, unchanged). See
# that script's own header for what setting this actually does and does
# not affect. CHAOS-4313, codex xhigh review round 3 (Medium, confirmed):
# under transport=api, trial_responder_model resolves the SAME concrete
# default run-responder-api.sh's own MODEL variable would otherwise apply
# silently -- an unset value used to stay empty here, so the go test's own
# twoTurnResponderModel() recorded the literal string "ambient-default" in
# provenance even though gpt-5.6-luna, a real known value, was what
# actually answered every call.
export ACR_TEST_TRIAL_RESPONDER_MODEL="$(trial_responder_model)"
# CHAOS-4313 follow-up: same explicit-pass-through discipline as
# ACR_TEST_TRIAL_RESPONDER_MODEL immediately above, so the go test's own
# twoTurnResponderEffort() and run-responder-api.sh's own reasoning-effort
# passthrough agree on what was actually requested. Unlike MODEL, this has
# no substituted default -- trial_responder_effort's own empty output for
# an unset var is itself the correct value to export here.
export ACR_TEST_TRIAL_RESPONDER_EFFORT="$(trial_responder_effort)"
echo "EXCHANGE_DIR=$exdir"
echo "ORACLE_ANNEX=$ANNEX_PATH"

responder_log="$exdir/_responder_driver.log"
"$(trial_responder_script)" "$exdir" >"$responder_log" 2>&1 &
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
