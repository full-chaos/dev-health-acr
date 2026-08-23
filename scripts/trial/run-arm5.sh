#!/usr/bin/env bash
# Usage: run-arm5.sh <arm-label> [limit]
#
# CHAOS-3884: runs the file-exchange generative arm over the corpus (or the
# first [limit] cases) answered by run-responder-codex.sh -- a `codex exec`
# subprocess on the operator's ChatGPT SUBSCRIPTION auth, never a metered
# OPENAI_API_KEY (standing rule for this epic). This is arm 5 in
# file_exchange_runtime_test.go's own doc comment ("a separate agent, or a
# codex-exec subprocess for arm 5").
#
# Starts the responder BEFORE the go test so it is already watching when the
# first request is published, waits for the go test to finish, then signals
# DONE and waits for the responder to notice and exit cleanly (it wipes its
# own private CODEX_HOME on exit). Mirrors run-arm4.sh's exchange-dir
# lifecycle exactly; only the responder differs.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ARM="${1:?arm label required}"
LIMIT="${2:-}"

trial_wire_common_env
export ACR_TEST_TRIAL_OUT="$ACR_TRIAL_RESULTS_DIR/gen-trial-${ARM}.json"
export ACR_TEST_TRIAL_ARM="$ARM"
if [[ -n "$LIMIT" ]]; then
  export ACR_TEST_TRIAL_LIMIT="$LIMIT"
fi

exdir="$repo_root/.trial-exchange-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$exdir/requests" "$exdir/responses"
export ACR_TEST_TRIAL_EXCHANGE_DIR="$exdir"
export ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}"
# CHAOS-4113: explicit pass-through, not ambient inheritance -- unset by
# default, which leaves run-responder-codex.sh's own `codex exec` call with
# no `-m` flag (today's behavior, unchanged). See that script's own header
# for what setting this actually does and does not affect.
export ACR_TEST_TRIAL_RESPONDER_MODEL="${ACR_TEST_TRIAL_RESPONDER_MODEL:-}"
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
trial_run_go_test 6h
status=$?
set -e

echo "ARM=$ARM finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) exit=$status"
exit "$status"
