#!/usr/bin/env bash
# Usage: run-replay.sh [limit]
#
# CHAOS-3884: runs the frozen-interpretation replay harness
# (TestChaos3884ReplayHarness, internal/runtime/hosted) over the corpus (or
# the first [limit] cases), answered by run-responder-codex.sh -- the SAME
# `codex exec` subprocess on the operator's ChatGPT SUBSCRIPTION auth
# run-arm5.sh already uses for the scorecard, never a metered
# OPENAI_API_KEY (standing rule for this epic; embeddings alone use a
# metered key, per trial_wire_common_env -- unaffected by this script).
#
# Mirrors run-arm5.sh's exchange-dir lifecycle exactly (start the responder
# before the go test, wait for the test to finish, signal DONE, wait for the
# responder to notice and wipe its own private CODEX_HOME) -- only the go
# test target and output env var differ.
#
# CHAOS-3899: ACR_TEST_REPLAY_OUT is now a default-if-unset (":=" ), not an
# unconditional export -- an operator running the CHAOS-3899 shadow-round
# acceptance pass sets it explicitly (a DIFFERENT artifact path, per
# design brief v5 §6) before invoking this script; every existing caller
# that does not set it gets the exact same default path as before,
# byte-identical. ACR_TEST_TRIAL_SHADOW_CENSUS=false (also optional, read
# directly by the go test) opts back out to a zero-shadow-overhead run of
# the ORIGINAL CHAOS-3884 replay if ever needed again.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

LIMIT="${1:-}"

trial_wire_common_env
: "${ACR_TEST_REPLAY_OUT:=$ACR_TRIAL_RESULTS_DIR/gen-trial-chaos3884_full50_replay.json}"
export ACR_TEST_REPLAY_OUT
export ACR_TEST_TRIAL_ARM="replay"
if [[ -n "$LIMIT" ]]; then
  export ACR_TEST_TRIAL_LIMIT="$LIMIT"
fi
# ACR_TRIAL_CORPUS_SHA256 (optional): the operator's own known-good hash for
# ACR_TRIAL_CORPUS, verified by the harness itself BEFORE any live call --
# see TestChaos3884ReplayHarness's own doc comment.
if [[ -n "${ACR_TRIAL_CORPUS_SHA256:-}" ]]; then
  export ACR_TEST_TRIAL_CORPUS_SHA256="$ACR_TRIAL_CORPUS_SHA256"
fi
# ACR_TEST_TRIAL_BASE_SHA: origin/main's tip THIS run's branch was rebased
# onto -- required provenance (team-lead ruling 2026-08-17), read directly
# from git rather than trusted to an operator-supplied value.
export ACR_TEST_TRIAL_BASE_SHA="$(cd "$repo_root" && git rev-parse origin/main)"

exdir="$repo_root/.trial-exchange-replay-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$exdir/requests" "$exdir/responses"
export ACR_TEST_TRIAL_EXCHANGE_DIR="$exdir"
export ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}"
echo "EXCHANGE_DIR=$exdir"

responder_log="$exdir/_responder_driver.log"
"$(dirname "${BASH_SOURCE[0]}")/run-responder-codex.sh" "$exdir" >"$responder_log" 2>&1 &
responder_pid=$!

cleanup() {
  touch "$exdir/DONE"
  wait "$responder_pid" 2>/dev/null || true
}
trap cleanup EXIT

set +e
( cd "$repo_root" && go test -run TestChaos3884ReplayHarness -count=1 -v -timeout 6h ./internal/runtime/hosted )
status=$?
set -e

echo "REPLAY finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) exit=$status out=$ACR_TEST_REPLAY_OUT"
exit "$status"
