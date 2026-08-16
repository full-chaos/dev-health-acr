#!/usr/bin/env bash
# Usage: run-arm4-reclass.sh <exchange-dir> <indices> [arm-label] [out-suffix]
# Reruns an EXACT subset of corpus positions against a file-exchange
# responder. exchange-dir must already exist (requests/, responses/) and
# have a responder watching it (sol review F6: never hard-coded).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

EXDIR="${1:?exchange dir required}"
INDICES="${2:?comma-separated indices required}"
ARM="${3:-generative-reclass}"
SUFFIX="${4:-reclass}"

trial_wire_common_env
export ACR_TEST_TRIAL_OUT="$ACR_TRIAL_RESULTS_DIR/gen-trial-${ARM}-${SUFFIX}.json"
export ACR_TEST_TRIAL_ARM="${ARM}-${SUFFIX}"
export ACR_TEST_TRIAL_INDICES="$INDICES"
export ACR_TEST_TRIAL_EXCHANGE_DIR="$EXDIR"
export ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}"

set +e
trial_run_go_test 30m
status=$?
set -e
touch "$EXDIR/DONE"
exit "$status"
