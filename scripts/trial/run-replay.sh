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
# CHAOS-3899: ACR_TEST_REPLAY_OUT is a default-if-unset (":=" ), not an
# unconditional export -- an operator running the CHAOS-3899 shadow-round
# acceptance pass (or any other run) can still set it explicitly to a
# specific, deliberately chosen artifact path.
# ACR_TEST_TRIAL_SHADOW_CENSUS=false (also optional, read directly by the
# go test) opts back out to a zero-shadow-overhead run of the ORIGINAL
# CHAOS-3884 replay if ever needed again.
#
# CHAOS-3896 Slice C rider (team-lead, real incident): the default used to
# be a single FIXED filename shared by every unlabeled invocation
# (gen-trial-chaos3884_full50_replay.json) -- a later run silently
# overwrote an earlier one's artifact, destroying the evidence behind an
# already-cited acceptance number. The default now embeds this run's own
# UTC start timestamp PLUS this shell's own PID (independent codex xhigh
# review finding, confirmed and fixed: a timestamp alone still collides for
# two invocations started in the SAME second -- the go test itself
# (chaos3884_replay_harness_test.go) refuses outright to overwrite ANY
# existing file at ACR_TEST_REPLAY_OUT, so a same-second collision failed
# loud rather than silently overwriting, but the exchange-dir collision
# below (RunID/request-file collision, not this artifact) still made one of
# the two runs time out instead), so two ordinary invocations -- even
# started in the same second -- can never collide by accident.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

LIMIT="${1:-}"

trial_wire_common_env
# CHAOS-3916 (local/trial slice): this script's own consumer,
# TestChaos3884ReplayHarness, is the ONLY trial test that records
# ResolvedActiveEpoch/GraphLifecycleEnabled in its provenance -- see
# trial_wire_graph_lifecycle_env's own doc comment (common.sh) for why this
# is called here specifically rather than folded into trial_wire_common_env.
trial_wire_graph_lifecycle_env
: "${ACR_TEST_REPLAY_OUT:=$ACR_TRIAL_RESULTS_DIR/gen-trial-chaos3884_full50_replay-$(date -u +%Y%m%dT%H%M%SZ)-$$.json}"
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

# $$ (independent codex xhigh review finding, confirmed and fixed): a
# timestamp alone still collides for two invocations started in the SAME
# second -- both would share this exchange dir, both start their own
# request sequence numbering at 000001, and the responder's session-nonce
# matching would pair a request from one run with a stale/foreign response
# from the other, timing one of the two runs out rather than failing loud.
exdir="$repo_root/.trial-exchange-replay-$(date -u +%Y%m%dT%H%M%SZ)-$$"
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
( cd "$repo_root" && go test -run TestChaos3884ReplayHarness -count=1 -v -timeout 6h ./internal/runtime/hosted )
status=$?
set -e

echo "REPLAY finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) exit=$status out=$ACR_TEST_REPLAY_OUT"
exit "$status"
