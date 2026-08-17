#!/usr/bin/env bash
# Usage: run-gate-sweep-cell.sh <cell-label> <transport: exchange|real> [model]
#
# CHAOS-3857 commit-gate threshold sweep: runs ONE grid cell. The cell's
# knob values are NOT flags on this script -- they are the CHAOS-3857 env
# vars generative_trial_live_test.go's wireProductionEnv already knows how
# to pass through (ACR_TEST_TRIAL_COMMIT_LONE_FLOOR / _TOP_FLOOR / _TOP_GAP
# / _VECTOR_MARGIN_COMMIT_THRESHOLD, mirroring every other ACR_TEST_TRIAL_*
# input this harness already takes), set by the CALLER before invoking this
# script. Any subset may be set; an unset knob keeps its calibrated
# production default (see CommitGatePolicy's doc comment,
# internal/contextfabric/graphrank/resolution.go). This script adds no
# second env-wiring path to drift from that one -- it only echoes which
# knobs this cell is running with (also recorded, independently, in the
# result JSON's own provenance.commit_gate) and delegates straight through
# to the existing arm runner for the chosen transport:
#   exchange -- run-arm4.sh (file-exchange transport, an out-of-process
#               responder answers -- see .probe-tmp/codex-luna-responder.sh
#               for the CHAOS-3857 luna smoke/sweep responder). Cheap, no
#               per-token API cost -- this is the exploratory 12-cell grid's
#               transport.
#   real     -- run-arm.sh (the real, billable provider API). Reserved for
#               the SINGLE post-merge confirmation run at whatever operating
#               point chris ratifies from the swept curve -- not for the
#               exploratory grid.
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"

CELL="${1:?cell label required}"
TRANSPORT="${2:?transport required: exchange|real}"
MODEL="${3:-gpt-5.6-luna}"

echo "SWEEP_CELL=$CELL transport=$TRANSPORT model=$MODEL" \
	"lone_floor=${ACR_TEST_TRIAL_COMMIT_LONE_FLOOR:-<calibrated default 0.72>}" \
	"top_floor=${ACR_TEST_TRIAL_COMMIT_TOP_FLOOR:-<calibrated default 0.88>}" \
	"top_gap=${ACR_TEST_TRIAL_COMMIT_TOP_GAP:-<calibrated default 0.12>}" \
	"margin_m=${ACR_TEST_TRIAL_VECTOR_MARGIN_COMMIT_THRESHOLD:-<calibrated default 0.03378617763519299>}"

case "$TRANSPORT" in
exchange)
	exec bash "$script_dir/run-arm4.sh" "$CELL"
	;;
real)
	exec bash "$script_dir/run-arm.sh" "$CELL" "$MODEL"
	;;
*)
	echo "unknown transport $TRANSPORT (want exchange|real)" >&2
	exit 1
	;;
esac
