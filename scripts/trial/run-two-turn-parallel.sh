#!/usr/bin/env bash
# Usage: run-two-turn-parallel.sh <oracle-annex-path> [limit] [shard-count]
#
# CHAOS-4033: fans TestChaos3742TwoTurnConfirmationReplay out across N
# isolated environments, one per corpus SHARD (see the shard-selection
# knob run-two-turn.sh's sequential sibling does not use, and that test's
# own SCOPE NOTE doc comment). Only Postgres needs per-shard isolation:
# Engine.Investigate (internal/contextfabric/engine.go) never writes to
# the graph -- ResolveInvestigationBinding/ResolveSubjects/DiscoverContext
# are all read paths, and the harness's only .Save() call targets the
# Postgres results store -- so FalkorDB and ClickHouse stay pointed at the
# STANDING stack (read-only, shared across shards, exactly like
# run-two-turn.sh itself uses today). This script NEVER writes to,
# migrates, restarts, or loads the standing Postgres: every Postgres
# instance it touches is a fresh, ephemeral, per-shard scratch container
# this script creates and tears down itself.
#
# SCOPE NOTE: a single shard's own artifact is never standalone valid
# evidence (see chaos3742_two_turn_confirmation_test.go's own comment on
# this) -- this script's real pass/fail signal is the merge tool's exit
# code, not any individual shard's `go test` exit code alone (though a
# non-zero shard exit still aborts the merge -- a genuine per-case
# correctness violation is real regardless of shard size and must never
# be silently merged away).
#
# METHODOLOGY GUARD (ratified, non-negotiable): parallel execution is a
# NEW execution shape. The first parallel run against a real corpus MUST
# be validated against a sequential control on the same corpus/SHA before
# its results are trusted for measurement -- this script cannot know
# whether a given invocation IS that validation run, so it does not
# enforce that itself, but the merged artifact self-labels
# provenance.execution_shape="parallel" so a reader can never mistake it
# for a ratified sequential pair.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

ANNEX_PATH="${1:?usage: run-two-turn-parallel.sh <oracle-annex-path> [limit] [shard-count]}"
LIMIT="${2:-}"
SHARD_COUNT="${3:-4}"

if [[ ! -f "$ANNEX_PATH" ]]; then
  echo "run-two-turn-parallel.sh: oracle annex not found at $ANNEX_PATH" >&2
  exit 1
fi
if ! [[ "$SHARD_COUNT" =~ ^[0-9]+$ ]] || [[ "$SHARD_COUNT" -lt 1 ]]; then
  echo "run-two-turn-parallel.sh: shard-count must be a positive integer, got $SHARD_COUNT" >&2
  exit 1
fi

trial_wire_common_env
export ACR_TEST_TWOTURN_ORACLE_ANNEX="$ANNEX_PATH"
if [[ -n "$LIMIT" ]]; then
  export ACR_TEST_TRIAL_LIMIT="$LIMIT"
fi
if [[ -n "${ACR_TRIAL_CORPUS_SHA256:-}" ]]; then
  export ACR_TEST_TRIAL_CORPUS_SHA256="$ACR_TRIAL_CORPUS_SHA256"
fi
# ACR_TEST_TRIAL_BASE_SHA/ACR_TEST_TRIAL_ARM: see run-two-turn.sh's own
# comments for why these are required exactly this way.
export ACR_TEST_TRIAL_BASE_SHA="$(cd "$repo_root" && git rev-parse origin/main)"
export ACR_TEST_TRIAL_ARM="twoturn"

# $$ + timestamp (chaos3884's own collision lesson, applied identically
# here, and to every per-shard container/dir name below): two invocations
# started in the same second still get distinct names.
RUN_TAG="$(date -u +%Y%m%dT%H%M%SZ)-$$"
PG_USER="$(trial_secret POSTGRES_USER)"
PG_PASSWORD="$(trial_secret POSTGRES_PASSWORD)"
POSTGRES_IMAGE="postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44"

PG_CONTAINERS=()
EXCHANGE_DIRS=()
RESPONDER_PIDS=()
TEST_PIDS=()
SHARD_OUT=()
# cleanup_had_issues (codex round-3 finding, LOW): set when a responder had
# to be force-killed or a scratch container failed to remove -- the
# measurement can still be fully valid (the artifact was already written
# to disk before any of this teardown runs), so this never overrides
# merge_status, but it DOES change a would-be exit 0 into a distinct exit
# 3 at the very end, so an automated caller can tell "valid, clean" apart
# from "valid, but needs manual cleanup follow-up" instead of both reading
# as a silent, identical success.
cleanup_had_issues=0

# kill_descendants (codex round-2 finding, MEDIUM): a bare `kill "$pid"`
# only signals that one process. Backgrounded jobs in this NON-interactive
# script (no `set -m`) do NOT get their own process group -- confirmed
# empirically (a `&`-launched subshell's pgid equals the SCRIPT's own
# pgid, not a fresh one), so a negative-PID/process-group kill cannot be
# used to reach a tracked PID's children. run-responder-codex.sh's `codex
# exec` runs as a direct foreground child of the responder script (not
# backgrounded), and `go test` execs a separate compiled test binary as
# its own child -- terminating only the tracked wrapper PID would leave
# either running. Walks the process tree via `pgrep -P` (standard on both
# macOS and Linux) and signals bottom-up (children before parents), so a
# parent dying mid-walk can never orphan/reparent a still-undiscovered
# child before this reaches it.
kill_descendants() {
  local pid="$1" sig="$2" child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    kill_descendants "$child" "$sig"
  done
  kill "-$sig" "$pid" 2>/dev/null || true
}

# wait_with_timeout (codex round-1 finding, HIGH): a plain `wait "$pid"`
# blocks forever if that process never exits -- a responder whose `codex
# exec` hangs (network stall, an unexpected auth prompt) never writes a
# response, so run-responder-codex.sh's own poll loop (DONE && pending==0)
# never sees pending==0 and never exits, even after DONE is touched. That
# would leave teardown blocked indefinitely and every scratch container
# running until something kills this script by hand. Bounded instead: wait
# up to timeout_s, then TERM and (if still alive) KILL the WHOLE process
# tree via kill_descendants.
#
# ACCEPTED RESIDUAL (codex round-3 finding, MEDIUM, deliberately not chased
# further): kill_descendants snapshots the tree once per signal, so a
# descendant forking in the ~2s window between the TERM pass and the KILL
# pass -- or an intermediate process exiting on TERM and reparenting a
# TERM-ignoring child before that KILL pass -- can theoretically slip
# through. A fully race-free kill needs OS support this script cannot rely
# on portably (Linux cgroups, or `setsid`, which macOS does not ship by
# default) -- out of proportion to the actual risk: this path only runs
# after an already-exceptional timeout, the trial artifact was already
# written to disk before any of this runs (see the caller), and the worst
# case is a leaked subprocess/temp dir, never corrupted measurement data.
# A forced KILL (as opposed to TERM) also bypasses run-responder-codex.sh's
# own EXIT/INT/TERM trap, which can leave its private CODEX_HOME scratch
# dir (under $TMPDIR, never the shared ~/.codex) behind uncleaned.
#
# Returns 1 if a forced kill was needed (0 if the process exited on its
# own) -- callers use this to distinguish a clean run from one that needed
# manual follow-up, without treating it as a measurement failure.
wait_with_timeout() {
  local pid="$1" timeout_s="$2" waited=0 killed=0
  while kill -0 "$pid" 2>/dev/null; do
    if ((waited >= timeout_s)); then
      echo "run-two-turn-parallel.sh: pid $pid did not exit within ${timeout_s}s -- killing its process tree" >&2
      kill_descendants "$pid" TERM
      sleep 2
      kill_descendants "$pid" KILL
      killed=1
      break
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid" 2>/dev/null || true
  return "$killed"
}

# Safety-net cleanup: reached on ANY exit path (success falls through the
# manual teardown below first, waits out TEST_PIDS/RESPONDER_PIDS itself,
# and clears every array, so this becomes a no-op there; an early failure
# mid-loop reaches this trap with whatever was actually created still
# populated).
cleanup() {
  local status=$?
  # codex round-1 finding (MEDIUM) + round-2 follow-up: an early failure
  # (e.g. a later shard's docker/migration step) used to leave an earlier
  # shard's already-launched go test running orphaned, potentially for its
  # full 6h timeout. wait_with_timeout (bounded wait, then TERM/KILL the
  # whole process tree, not just the go test wrapper) closes this the same
  # way it closes the responder case -- a 30s bound is generous for a test
  # binary to notice TERM and exit.
  for pid in "${TEST_PIDS[@]:-}"; do
    [[ -n "$pid" ]] && wait_with_timeout "$pid" 30
  done
  for exdir in "${EXCHANGE_DIRS[@]:-}"; do
    [[ -n "$exdir" ]] && touch "$exdir/DONE" 2>/dev/null || true
  done
  for pid in "${RESPONDER_PIDS[@]:-}"; do
    [[ -n "$pid" ]] && wait_with_timeout "$pid" 300
  done
  # codex round-2 finding, MEDIUM: a failed `docker rm -f` used to be
  # silently swallowed -- surfaced now so a leaked scratch container is
  # visible and actionable, never silently dropped.
  local failed_containers=()
  for c in "${PG_CONTAINERS[@]:-}"; do
    if [[ -n "$c" ]] && ! docker rm -f "$c" >/dev/null 2>&1; then
      failed_containers+=("$c")
    fi
  done
  if [[ "${#failed_containers[@]}" -gt 0 ]]; then
    echo "run-two-turn-parallel.sh: failed to remove ${#failed_containers[@]} scratch container(s) -- clean up manually: ${failed_containers[*]/#/docker rm -f }" >&2
  fi
  exit "$status"
}
trap cleanup EXIT

echo "RUN_TAG=$RUN_TAG SHARD_COUNT=$SHARD_COUNT ORACLE_ANNEX=$ANNEX_PATH"

for ((i = 0; i < SHARD_COUNT; i++)); do
  pg_name="acr-trial-pg-${RUN_TAG}-${i}"
  docker run -d --name "$pg_name" \
    -e POSTGRES_USER="$PG_USER" -e POSTGRES_PASSWORD="$PG_PASSWORD" -e POSTGRES_DB=acr \
    -p 127.0.0.1::5432 \
    "$POSTGRES_IMAGE" >/dev/null
  PG_CONTAINERS+=("$pg_name")

  pg_port=""
  for _ in $(seq 1 60); do
    if docker exec "$pg_name" pg_isready -U "$PG_USER" >/dev/null 2>&1; then
      pg_port="$(docker port "$pg_name" 5432/tcp | head -1 | cut -d: -f2)"
      [[ -n "$pg_port" ]] && break
    fi
    sleep 1
  done
  if [[ -z "$pg_port" ]]; then
    echo "run-two-turn-parallel.sh: shard $i postgres ($pg_name) never became ready" >&2
    exit 1
  fi
  echo "shard $i: postgres ready at 127.0.0.1:$pg_port ($pg_name)"

  shard_dsn="postgres://$PG_USER:$PG_PASSWORD@127.0.0.1:$pg_port/acr?sslmode=disable"
  ( cd "$repo_root" && ACR_POSTGRES_MIGRATION_DSN="$shard_dsn" ACR_POSTGRES_CONNECTION_KIND=direct \
      go run ./cmd/acr-migrate up )

  # ${TMPDIR:-/tmp}, not $repo_root (codex-round-4 dry-run finding): every
  # process this exchange dir/log path is visible to runs
  # requireGitSourceIdentity (generative_trial_live_test.go), which hashes
  # `git status --porcelain` PLUS every untracked file's own content. An
  # exchange dir living inside the repo tree is itself untracked, so N
  # shards launched moments apart each see a DIFFERENT set of
  # already-created exchange dirs/logs from earlier shards in the same
  # loop -- producing a genuinely different source_diff_digest per shard
  # and tripping the merge tool's (correct) cross-shard consistency check.
  # Mirrors run-responder-codex.sh's own existing precedent (its private
  # CODEX_HOME already lives under ${TMPDIR:-/tmp}, never the repo tree)
  # for exactly this class of scratch operational state.
  exdir="${TMPDIR:-/tmp}/acr-trial-exchange-twoturn-parallel-${RUN_TAG}-${i}"
  mkdir -p "$exdir/requests" "$exdir/responses"
  EXCHANGE_DIRS+=("$exdir")
  "$(dirname "${BASH_SOURCE[0]}")/run-responder-codex.sh" "$exdir" >"$exdir/_responder_driver.log" 2>&1 &
  RESPONDER_PIDS+=("$!")

  shard_out="$ACR_TRIAL_RESULTS_DIR/gen-trial-chaos3742_twoturn-parallel-${RUN_TAG}-shard${i}.json"
  SHARD_OUT+=("$shard_out")

  # ACR_TEST_TRIAL_POSTGRES_DSN overrides trial_wire_common_env's own
  # (standing-stack) default for THIS subshell only -- FalkorDB/ClickHouse
  # deliberately inherit the shared, standing-stack values exported above
  # (see this script's own header for why that is safe).
  (
    cd "$repo_root" &&
      ACR_TEST_TRIAL_POSTGRES_DSN="$shard_dsn" \
      ACR_TEST_TWOTURN_OUT="$shard_out" \
      ACR_TEST_TRIAL_SHARD_INDEX="$i" \
      ACR_TEST_TRIAL_SHARD_COUNT="$SHARD_COUNT" \
      ACR_TEST_TRIAL_EXCHANGE_DIR="$exdir" \
      ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}" \
      go test -run TestChaos3742TwoTurnConfirmationReplay -count=1 -v -timeout 6h ./internal/runtime/hosted \
      >"$exdir.gotest.log" 2>&1
  ) &
  TEST_PIDS+=("$!")
done

echo "all $SHARD_COUNT shards launched, waiting..."
overall_status=0
for pid in "${TEST_PIDS[@]}"; do
  if ! wait "$pid"; then
    overall_status=1
  fi
done
TEST_PIDS=() # every shard already reaped above; trap becomes a no-op for these

for exdir in "${EXCHANGE_DIRS[@]}"; do
  touch "$exdir/DONE"
done
for pid in "${RESPONDER_PIDS[@]}"; do
  wait_with_timeout "$pid" 300 || cleanup_had_issues=1
done
RESPONDER_PIDS=() # waited above; trap becomes a no-op for these

# codex round-2 finding, MEDIUM: a failed `docker rm -f` used to be
# silently swallowed here too -- surfaced now, same as cleanup()'s own fix.
failed_containers=()
for c in "${PG_CONTAINERS[@]}"; do
  if ! docker rm -f "$c" >/dev/null 2>&1; then
    failed_containers+=("$c")
  fi
done
if [[ "${#failed_containers[@]}" -gt 0 ]]; then
  echo "run-two-turn-parallel.sh: failed to remove ${#failed_containers[@]} scratch container(s) -- clean up manually: ${failed_containers[*]/#/docker rm -f }" >&2
  cleanup_had_issues=1
fi
PG_CONTAINERS=() # torn down above; trap becomes a no-op for these

if [[ "$overall_status" -ne 0 ]]; then
  echo "run-two-turn-parallel.sh: one or more shards failed (see each shard's own .gotest.log) -- refusing to merge a run with a failed shard" >&2
  exit 1
fi

merged_out="$ACR_TRIAL_RESULTS_DIR/gen-trial-chaos3742_twoturn-parallel-${RUN_TAG}-merged.json"
set +e
( cd "$repo_root" && go run ./cmd/acr-trial-merge-two-turn -out "$merged_out" "${SHARD_OUT[@]}" )
merge_status=$?
set -e

echo "PARALLEL run finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) merge_exit=$merge_status merged_out=$merged_out"

# codex round-3 finding, LOW: merge_status alone used to be the ENTIRE
# exit status, so a forced responder kill or a leaked scratch container
# read identically to a fully clean run -- exit 3 distinguishes "valid
# measurement, but see the warnings above for manual cleanup follow-up"
# from a genuine measurement failure (merge_status itself, never
# overridden here) or a fully clean success.
if [[ "$merge_status" -ne 0 ]]; then
  exit "$merge_status"
fi
if [[ "$cleanup_had_issues" -eq 1 ]]; then
  echo "run-two-turn-parallel.sh: measurement VALID but cleanup needed a forced kill or left a container behind -- see warnings above; exiting 3 to flag this distinctly from a clean run" >&2
  exit 3
fi
exit 0
