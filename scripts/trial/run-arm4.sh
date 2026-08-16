#!/usr/bin/env bash
# Usage: run-arm4.sh <arm-label>
# Runs a file-exchange generative arm (a diagnostic transport an
# out-of-process responder answers) over the full corpus. Creates a FRESH
# timestamped exchange session dir every run (sol review S3) and prints
# its path -- tell the responder before this script's go test starts
# writing requests.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ARM="${1:?arm label required}"

trial_wire_common_env
export ACR_TEST_TRIAL_OUT="$ACR_TRIAL_RESULTS_DIR/gen-trial-${ARM}.json"
export ACR_TEST_TRIAL_ARM="$ARM"

exdir="$dev_health_root/acr-wt-trial/.trial-exchange-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$exdir/requests" "$exdir/responses"
export ACR_TEST_TRIAL_EXCHANGE_DIR="$exdir"
export ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}"
echo "EXCHANGE_DIR=$exdir (tell the responder before launching)"

set +e
trial_run_go_test 6h
status=$?
set -e
echo "ARM=$ARM finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) exit=$status"
touch "$exdir/DONE"
exit "$status"
