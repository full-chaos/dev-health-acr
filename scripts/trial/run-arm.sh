#!/usr/bin/env bash
# Usage: run-arm.sh <arm-label> <model> [fallback] [limit]
# Runs a real-API generative arm (nano alone / luna alone / nano+luna
# fallback) over the full corpus (or the first [limit] cases).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ARM="${1:?arm label required}"
MODEL="${2:?model required}"
FALLBACK="${3:-}"
LIMIT="${4:-}"

trial_wire_common_env
export ACR_TEST_TRIAL_OUT="$ACR_TRIAL_RESULTS_DIR/gen-trial-${ARM}.json"
export ACR_TEST_TRIAL_ARM="$ARM"
export ACR_TEST_TRIAL_MODEL="$MODEL"
# sol review F1: explicitly clear before conditionally setting, so this
# script can never inherit a stale ACR_TEST_TRIAL_MODEL_FALLBACK from the
# calling shell when this run's arm has no fallback.
unset ACR_TEST_TRIAL_MODEL_FALLBACK
if [[ -n "$FALLBACK" ]]; then
  export ACR_TEST_TRIAL_MODEL_FALLBACK="$FALLBACK"
fi
export ACR_TEST_TRIAL_MODEL_API_KEY="$(trial_secret OPENAI_API_KEY)"
if [[ -n "$LIMIT" ]]; then
  export ACR_TEST_TRIAL_LIMIT="$LIMIT"
fi

echo "ARM=$ARM MODEL=$MODEL FALLBACK=${FALLBACK:-none} started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
trial_run_go_test 120m
echo "ARM=$ARM finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
