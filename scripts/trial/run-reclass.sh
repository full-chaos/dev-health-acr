#!/usr/bin/env bash
# Usage: run-reclass.sh <arm-label> <model> [fallback] <indices>
# Reruns an EXACT subset of corpus positions (comma-separated) against the
# real API, for targeted reclassification -- does not pay for the other
# already-known cases.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ARM="${1:?arm label required}"
MODEL="${2:?model required}"
FALLBACK="${3:-}"
INDICES="${4:?comma-separated indices required}"

trial_wire_common_env
export ACR_TEST_TRIAL_OUT="$ACR_TRIAL_RESULTS_DIR/gen-trial-${ARM}-reclass.json"
export ACR_TEST_TRIAL_ARM="${ARM}-reclass"
export ACR_TEST_TRIAL_INDICES="$INDICES"
export ACR_TEST_TRIAL_MODEL="$MODEL"
unset ACR_TEST_TRIAL_MODEL_FALLBACK
if [[ -n "$FALLBACK" ]]; then
  export ACR_TEST_TRIAL_MODEL_FALLBACK="$FALLBACK"
fi
export ACR_TEST_TRIAL_MODEL_API_KEY="$(trial_secret OPENAI_API_KEY)"

echo "RECLASS ARM=$ARM MODEL=$MODEL FALLBACK=${FALLBACK:-none} indices=$INDICES started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
trial_run_go_test 15m
echo "RECLASS ARM=$ARM finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
