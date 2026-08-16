#!/usr/bin/env bash
# Usage: run-smoke.sh [limit]
# Cheap real-API smoke check (default 2 questions, nano alone) proving the
# harness end-to-end before spending a full arm's budget.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

LIMIT="${1:-2}"

trial_wire_common_env
export ACR_TEST_TRIAL_OUT="$ACR_TRIAL_RESULTS_DIR/smoke-nano-alone.json"
export ACR_TEST_TRIAL_ARM=nano_alone_smoke
export ACR_TEST_TRIAL_LIMIT="$LIMIT"
export ACR_TEST_TRIAL_MODEL=gpt-5-nano
export ACR_TEST_TRIAL_MODEL_API_KEY="$(trial_secret OPENAI_API_KEY)"

trial_run_go_test 10m
