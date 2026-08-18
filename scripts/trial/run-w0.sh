#!/usr/bin/env bash
# Usage: run-w0.sh [limit]
#
# CHAOS-3900 W0: runs the window-inference shadow measurement harness
# (TestChaos3900W0WindowShadow, internal/runtime/hosted) over the corpus
# (or the first [limit] cases), answered by run-responder-codex.sh -- the
# SAME `codex exec` subprocess on the operator's ChatGPT SUBSCRIPTION auth
# run-arm5.sh/run-replay.sh already use, never a metered OPENAI_API_KEY
# (standing rule for this epic; embeddings alone use a metered key, per
# trial_wire_common_env -- unaffected by this script).
#
# Mirrors run-replay.sh's exchange-dir lifecycle exactly (start the
# responder before the go test, wait for the test to finish, signal DONE,
# wait for the responder to notice and wipe its own private CODEX_HOME) --
# only the go test target and output env var differ. This harness runs
# THREE interpretations per corpus case (the N=3 divergence measurement,
# design brief ../.remember/chaos3900-design-brief.md (relative to the dev-health/acr repo root) v5.2 §7), so it takes
# roughly 3x as long per case as run-replay.sh's single-interpretation
# pass -- the -timeout below and the default exchange timeout account for
# that.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

LIMIT="${1:-}"

trial_wire_common_env
: "${ACR_TEST_W0_OUT:=$ACR_TRIAL_RESULTS_DIR/gen-trial-chaos3900_w0_window_shadow.json}"
export ACR_TEST_W0_OUT
export ACR_TEST_TRIAL_ARM="w0"
if [[ -n "$LIMIT" ]]; then
  export ACR_TEST_TRIAL_LIMIT="$LIMIT"
fi
if [[ -n "${ACR_TRIAL_CORPUS_SHA256:-}" ]]; then
  export ACR_TEST_TRIAL_CORPUS_SHA256="$ACR_TRIAL_CORPUS_SHA256"
fi
export ACR_TEST_TRIAL_BASE_SHA="$(cd "$repo_root" && git rev-parse origin/main)"

exdir="$repo_root/.trial-exchange-w0-$(date -u +%Y%m%dT%H%M%SZ)"
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
( cd "$repo_root" && go test -run TestChaos3900W0WindowShadow -count=1 -v -timeout 12h ./internal/runtime/hosted )
status=$?
set -e

echo "W0 finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) exit=$status out=$ACR_TEST_W0_OUT"
exit "$status"
