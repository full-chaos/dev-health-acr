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
# run-two-turn.sh itself uses today).
#
# CHAOS-4100: per-shard isolation is now a per-shard DATABASE on the
# standing instance, not a per-shard CONTAINER. One template database is
# migrated once, then each shard gets a `CREATE DATABASE ... TEMPLATE`
# clone of it -- a file copy, seconds for sixty-five, against the tens of
# seconds per shard a container start plus a full `go run ./cmd/acr-migrate`
# used to cost. Removing the containers is also what removes Docker from
# the run, and with it the `mapped port: invalid port` socket-contention
# flake class this harness kept hitting.
#
# ISOLATION IS UNCHANGED, and that is the point rather than a footnote.
# Every shard still gets its OWN database, so the two pieces of shared,
# ORG-KEYED state stay per-shard exactly as before: acr.context_fabric_
# structure_prior_pointer is `org_id TEXT PRIMARY KEY` (migration 0028) --
# one row per org, and every shard runs as the SAME org -- and answer reuse
# keys on (org_id, question_hash, ...), so a shared database would let one
# shard serve another shard's stored answer. A shared INSTANCE has neither
# property; a shared instance with per-shard DATABASES has both.
#
# This script still NEVER writes to, migrates, restarts, or loads the
# standing `acr` database itself: every database it touches is one it
# created under the acr_trial_<RUN_TAG>_ prefix and drops on the way out.
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

# PLAN-ONLY IS ANSWERED BEFORE ANY LIVE SETUP (codex xhigh review round 1,
# P1). common.sh hard-exits at SOURCE time when the sibling dev-health
# checkout or its ops/.env is absent -- correctly, since every other path
# through this script needs those credentials. Plan-only needs none of it:
# it reads the annex, computes a layout and prints it. Sourcing first made
# `make shard-plan` (and therefore `make verify`) depend on a private
# credentials file that no clean checkout or CI runner has, which would
# have failed CI while passing on the one machine that happens to have it.
#
# The layout logic itself is duplicated below rather than hoisted here on
# purpose: this branch computes it from the SAME code the live path uses,
# by re-entering this script with the variables it needs already set.
plan_only_requested() { [[ "${ACR_TRIAL_SHARD_PLAN_ONLY:-0}" == "1" ]]; }
if ! plan_only_requested; then
  source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
else
  # Minimal stand-ins for the two values the layout code reads. Neither
  # reaches a database, a model or a credential.
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
  trial_wire_common_env() { :; }
  trial_secret() { printf 'plan-only'; }
  require_outside_repo_tree() { :; }
  ACR_TRIAL_RESULTS_DIR="${ACR_TRIAL_RESULTS_DIR:-${TMPDIR:-/tmp}}"
  # Unset any ACR_TEST_TRIAL_PG_* left exported by an EARLIER live
  # invocation in this same shell (codex xhigh review, fresh cycle round 1,
  # P2): the stub above never sets these, but PG_HOST/PG_PORT's own
  # fallback chain below checks them first -- without this, a stale
  # ambient value would silently override what THIS plan-only invocation's
  # own ACR_TRIAL_PG_HOST/PORT diagnostic-relay tier was asked to preview.
  unset ACR_TEST_TRIAL_PG_HOST ACR_TEST_TRIAL_PG_PORT ACR_TEST_TRIAL_PG_USER ACR_TEST_TRIAL_PG_PASSWORD
fi

ANNEX_PATH="${1:?usage: run-two-turn-parallel.sh <oracle-annex-path> [limit] [shard-count]}"
LIMIT="${2:-}"
SHARD_COUNT="${3:-4}"

# CHAOS-4100 knobs.
#
# ACR_TRIAL_CASES_PER_SHARD (the ticket's C) is shard GRANULARITY: when
# set, the shard count is DERIVED from the annex's own distinct case
# indices rather than taken from $3, so `=1` means one case per shard and
# wall time becomes the slowest single case. Unset leaves $3 in charge and
# every existing invocation byte-identical.
#
# ACR_TRIAL_MAX_CONCURRENT_SHARDS bounds how many shards run AT ONCE, and
# it is independent of how many there are. It exists because the loop below
# used to background every shard unconditionally: fine at four, but at
# sixty-five per-case shards that is sixty-five concurrent `codex exec`
# processes against ONE ChatGPT subscription, since run-responder-codex.sh
# is one sequential responder PER SHARD. The default is deliberately
# conservative -- find the subscription's ceiling in a ramp smoke whose
# failures are free, not in a measurement run.
CASES_PER_SHARD="${ACR_TRIAL_CASES_PER_SHARD:-}"
MAX_CONCURRENT="${ACR_TRIAL_MAX_CONCURRENT_SHARDS:-8}"
# ACR_TEST_TRIAL_RESPONDER_MODEL (CHAOS-4113): explicit pass-through to
# run-responder-codex.sh, not ambient inheritance -- unset by default,
# which leaves every shard's responder with no `-m` flag (today's
# behavior, unchanged; see that script's own header for what setting this
# actually does and does not affect). Exported ONCE here, before the shard
# loop below launches its per-shard responder background jobs, since every
# shard in one run answers with the SAME model -- each backgrounded
# `run-responder-codex.sh` call inherits it like any other exported
# variable, no per-shard threading needed.
export ACR_TEST_TRIAL_RESPONDER_MODEL="${ACR_TEST_TRIAL_RESPONDER_MODEL:-}"
# SHARD_PLAN_ONLY prints the computed layout as JSON and exits without
# provisioning anything. The layout is the one piece of this script with
# real logic in it, so it is made inspectable and testable rather than
# only observable by running a full trial.
PLAN_ONLY="${ACR_TRIAL_SHARD_PLAN_ONLY:-0}"

# PG_HOST/PG_PORT/PG_USER/PG_PASSWORD and trial_pg_dsn are defined further
# down, right after trial_wire_common_env is called -- see that site's own
# comment (CHAOS-4186 round-3 design ruling) for why this moved off of
# ACR_TRIAL_PG_HOST/PORT + its own ops/.env read.

if ! [[ "$MAX_CONCURRENT" =~ ^[0-9]+$ ]] || [[ "$MAX_CONCURRENT" -lt 1 ]]; then
  echo "run-two-turn-parallel.sh: ACR_TRIAL_MAX_CONCURRENT_SHARDS must be a positive integer, got $MAX_CONCURRENT" >&2
  exit 1
fi
if [[ -n "$CASES_PER_SHARD" ]] && { ! [[ "$CASES_PER_SHARD" =~ ^[0-9]+$ ]] || [[ "$CASES_PER_SHARD" -lt 1 ]]; }; then
  echo "run-two-turn-parallel.sh: ACR_TRIAL_CASES_PER_SHARD must be a positive integer, got $CASES_PER_SHARD" >&2
  exit 1
fi

if [[ ! -f "$ANNEX_PATH" ]]; then
  echo "run-two-turn-parallel.sh: oracle annex not found at $ANNEX_PATH" >&2
  exit 1
fi
if ! [[ "$SHARD_COUNT" =~ ^[0-9]+$ ]] || [[ "$SHARD_COUNT" -lt 1 ]]; then
  echo "run-two-turn-parallel.sh: shard-count must be a positive integer, got $SHARD_COUNT" >&2
  exit 1
fi

# require_outside_repo_tree (codex round-1 finding on the TMPDIR fix
# below): validates a directory an operator-controlled env var points at
# is NOT inside this repo's own working tree -- an operator setting
# TMPDIR (or ACR_TRIAL_RESULTS_DIR) to a relative or in-repo path would
# silently reopen the exact untracked-file source-identity-digest race
# this fix exists to close (requireGitSourceIdentity hashes every
# untracked file under repo_root). Validates the RESOLVED absolute path
# (the RESULT), not the mechanics that produced it -- same philosophy as
# common.sh's own resolve_dev_health_root.
require_outside_repo_tree() {
  local label="$1" dir="$2" resolved
  if ! resolved="$(cd "$dir" 2>/dev/null && pwd -P)"; then
    echo "run-two-turn-parallel.sh: $label=$dir does not exist or is not a directory" >&2
    exit 1
  fi
  case "$resolved" in
  "$repo_root" | "$repo_root"/*)
    echo "run-two-turn-parallel.sh: $label resolves to $resolved, INSIDE this repo's working tree ($repo_root) -- refusing; this would corrupt every shard's own source-identity digest (requireGitSourceIdentity)" >&2
    exit 1
    ;;
  esac
}
plan_only_requested || require_outside_repo_tree "TMPDIR" "${TMPDIR:-/tmp}"

trial_wire_common_env

# PG_HOST/PG_PORT/PG_USER/PG_PASSWORD (CHAOS-4116, re-anchored CHAOS-4186
# round-3 design ruling): this script derives NOTHING of its own postgres
# connection anymore -- it reads exactly what trial_wire_common_env just
# resolved for ACR_TRIAL_DATA_PLANE, the SAME single switch every other
# trial store (Falkor/ClickHouse) moves with. This used to independently
# read ACR_TRIAL_PG_HOST/PORT plus its own ops/.env credential lookup,
# which let an operator move ONLY Postgres to kiac while Falkor/ClickHouse
# silently stayed on compose (codex xhigh review round 3, P1/High) -- a
# hybrid measurement with no error. The middle-tier ACR_TRIAL_PG_HOST/PORT
# fallback below is PLAN-ONLY-ONLY: ACR_TRIAL_SHARD_PLAN_ONLY=1 stubs
# trial_wire_common_env to a no-op (see the top of this file) specifically
# so the offline layout preview needs no live stack or ops/.env -- its own
# CHAOS-4100 A/B diagnostic-relay override still works exactly as before,
# entirely offline. This tier is inert for a live run: the all-or-none
# guard above refuses a lone ACR_TRIAL_PG_HOST before trial_wire_common_env
# is ever called, so live mode always falls through to what
# trial_wire_common_env actually resolved (or its own 127.0.0.1 default).
PG_HOST="${ACR_TEST_TRIAL_PG_HOST:-${ACR_TRIAL_PG_HOST:-127.0.0.1}}"
PG_PORT="${ACR_TEST_TRIAL_PG_PORT:-${ACR_TRIAL_PG_PORT:-5432}}"
PG_USER="${ACR_TEST_TRIAL_PG_USER:-$(trial_secret POSTGRES_USER)}"
PG_PASSWORD="${ACR_TEST_TRIAL_PG_PASSWORD:-$(trial_secret POSTGRES_PASSWORD)}"

# trial_pg_dsn (CHAOS-4116) is the ONE place a postgres:// DSN is built --
# template_dsn and SHARD_DSN below both call this rather than each carrying
# its own copy of the string template, so a host/port override is
# structurally incapable of reaching one and missing the other.
trial_pg_dsn() {
  printf 'postgres://%s:%s@%s:%s/%s?sslmode=disable' "$PG_USER" "$PG_PASSWORD" "$PG_HOST" "$PG_PORT" "$1"
}
# CHAOS-4100 (post-4108-fix graph rebuild incident): trial_wire_graph_lifecycle_env
# is only DEFINED by the real common.sh sourced above -- plan-only mode never
# sources it (its trial_wire_common_env stub reaches no database and needs
# none of this), so this call is guarded exactly like require_outside_repo_tree
# on the next line. Exported here, at the parallel launcher's own top level,
# it is inherited by every shard subshell below -- the SAME mechanism
# ACR_TEST_TRIAL_RESPONDER_MODEL already relies on. See run-two-turn.sh's own
# comment on trial_wire_graph_lifecycle_env for why this harness needs it at
# all: without it, every shard silently reads the bare legacy epoch-0 graph
# key even when the org has a live, rebuilt epoch.
plan_only_requested || trial_wire_graph_lifecycle_env
plan_only_requested || require_outside_repo_tree "ACR_TRIAL_RESULTS_DIR" "$ACR_TRIAL_RESULTS_DIR"
export ACR_TEST_TWOTURN_ORACLE_ANNEX="$ANNEX_PATH"
if [[ -n "$LIMIT" ]]; then
  export ACR_TEST_TRIAL_LIMIT="$LIMIT"
fi
if [[ -n "${ACR_TRIAL_CORPUS_SHA256:-}" ]]; then
  export ACR_TEST_TRIAL_CORPUS_SHA256="$ACR_TRIAL_CORPUS_SHA256"
fi
# ACR_TEST_TRIAL_BASE_SHA (CHAOS-4157 fix-forward, 2026-08-23): DROPPED --
# see run-two-turn.sh's own comment. This export has no reader left.
export ACR_TEST_TRIAL_ARM="twoturn"

# annex_case_indices reads the annex's own DISTINCT case indices, ascending.
#
# THE ANNEX IS AN OBJECT, NOT AN ARRAY (codex xhigh review round 3, P1).
# The signed oracle annex loadTwoTurnOracleAnnex consumes is
#   {"provenance": {...}, "cases": {"50": {...}, "51": {...}}}
# -- case indices are the DECIMAL-STRING KEYS of `.cases`. The first
# version of this function read `.[].index` as though the annex were an
# array of per-entry objects, which emits nulls against the real file and
# would have broken every live invocation, including the default modulo
# path this ticket promised to leave byte-identical. It passed the offline
# test only because that test's fixture was written to match the same wrong
# assumption -- the fixture confirmed the belief instead of the format.
#
# Non-numeric keys are SKIPPED rather than fatal, mirroring
# adaptSignedOracleAnnex's own `continue` on a non-numeric case key: the
# layout must agree with what the harness will actually run, so where the
# harness is lenient this has to be lenient identically, or a shard would
# be assigned a case the harness then ignores.
annex_case_indices() {
  jq -r '[(.cases // {}) | keys[] | select(test("^[0-9]+$")) | tonumber] | sort | .[]' "$ANNEX_PATH"
}

# $$ + timestamp (chaos3884's own collision lesson, applied identically
# here, and to every per-shard database/dir name below): two invocations
# started in the same second still get distinct names.
RUN_TAG="$(date -u +%Y%m%dT%H%M%SZ)-$$"
# RUN_SLUG is RUN_TAG reduced to what a postgres identifier accepts
# (lowercase alphanumerics and underscore). The database name has to carry
# RUN_TAG for the same collision reason every other per-shard resource
# does, but `-` and `T` in a bare identifier would force quoting through
# every psql call site.
RUN_SLUG="$(printf '%s' "$RUN_TAG" | tr '[:upper:]-' '[:lower:]_' | tr -cd 'a-z0-9_')"

# --- shard layout -----------------------------------------------------
# Computed BEFORE anything is provisioned, so a bad layout costs nothing.
ALL_INDICES=()
while IFS= read -r idx; do
  [[ -n "$idx" ]] && ALL_INDICES+=("$idx")
done < <(annex_case_indices)
if [[ "${#ALL_INDICES[@]}" -eq 0 ]]; then
  echo "run-two-turn-parallel.sh: oracle annex $ANNEX_PATH carries no case indices" >&2
  exit 1
fi

if [[ -n "$CASES_PER_SHARD" ]]; then
  # Contiguous chunks of the annex's OWN index set, which is what modulo
  # cannot express over a sparse annex: for indices 50..64 a modulo split
  # at shard-count 65 would spin fifty empty shards.
  SHARD_COUNT=$(((${#ALL_INDICES[@]} + CASES_PER_SHARD - 1) / CASES_PER_SHARD))
fi

# SHARD_CASES[i] is shard i's comma-separated case list. Round-robin, not
# contiguous chunks, when granularity is unset -- that reproduces the
# modulo rule the harness has always used, so an existing invocation
# selects byte-identical cases.
SHARD_CASES=()
for ((i = 0; i < SHARD_COUNT; i++)); do SHARD_CASES+=(""); done
if [[ -n "$CASES_PER_SHARD" ]]; then
  for ((n = 0; n < ${#ALL_INDICES[@]}; n++)); do
    target=$((n / CASES_PER_SHARD))
    if [[ -z "${SHARD_CASES[$target]}" ]]; then
      SHARD_CASES[$target]="${ALL_INDICES[$n]}"
    else
      SHARD_CASES[$target]="${SHARD_CASES[$target]},${ALL_INDICES[$n]}"
    fi
  done
else
  for idx in "${ALL_INDICES[@]}"; do
    target=$((idx % SHARD_COUNT))
    if [[ -z "${SHARD_CASES[$target]}" ]]; then
      SHARD_CASES[$target]="$idx"
    else
      SHARD_CASES[$target]="${SHARD_CASES[$target]},$idx"
    fi
  done
fi

if [[ "$PLAN_ONLY" == "1" ]]; then
  # pg_host/pg_port/pg_dsn_example (CHAOS-4116): proves the override reaches
  # the SAME construction every live psql_admin/template_dsn/SHARD_DSN call
  # uses, offline -- trial_pg_dsn is the ONE function template_dsn and
  # SHARD_DSN both call below (never a second copy of the DSN template), so
  # exercising it here is exercising their real code, not a restatement of
  # it. "EXAMPLE_DB" stands in for the RUN_TAG-derived database name neither
  # of those has yet at plan time; the DSN shape and the host/port it
  # carries are identical regardless of which database name fills that slot.
  #
  # codex review round 1 (P2, confirmed): PG_HOST/PG_PORT are OPERATOR
  # input (ACR_TRIAL_PG_HOST/PORT), not a closed set of digits like every
  # other value in this JSON -- a value carrying a `"` or a newline would
  # otherwise emit invalid JSON no consumer could parse. `jq -Rn --arg`
  # produces a properly quoted-and-escaped JSON string; this script
  # already depends on jq for annex parsing, so this adds no new tool.
  json_string() { jq -Rn --arg v "$1" '$v'; }
  printf '{"shard_count":%d,"granularity":%s,"concurrency_cap":%d,"case_count":%d,"pg_host":%s,"pg_port":%s,"pg_dsn_example":%s,"shards":[' \
    "$SHARD_COUNT" "${CASES_PER_SHARD:-0}" "$MAX_CONCURRENT" "${#ALL_INDICES[@]}" "$(json_string "$PG_HOST")" "$(json_string "$PG_PORT")" "$(json_string "$(trial_pg_dsn EXAMPLE_DB)")"
  for ((i = 0; i < SHARD_COUNT; i++)); do
    [[ "$i" -gt 0 ]] && printf ','
    printf '{"index":%d,"cases":"%s"}' "$i" "${SHARD_CASES[$i]}"
  done
  printf ']}\n'
  exit 0
fi

# PG_DATABASES replaces the pre-CHAOS-4100 PG_CONTAINERS: the resources
# this script now creates and must drop are DATABASES on the standing
# instance, not containers.
PG_DATABASES=()
PG_TEMPLATE=""
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
  # Same discipline the container teardown had (codex round-2 finding,
  # MEDIUM): a failed drop is SURFACED, never silently swallowed. A leaked
  # scratch database is cheap but it is still state on the standing
  # instance, and an operator has to be told it is there.
  local failed_drops=()
  for db in "${PG_DATABASES[@]:-}"; do
    [[ -n "$db" ]] && ! drop_trial_database "$db" && failed_drops+=("$db")
  done
  if [[ -n "$PG_TEMPLATE" ]] && ! drop_trial_database "$PG_TEMPLATE"; then
    failed_drops+=("$PG_TEMPLATE")
  fi
  if [[ "${#failed_drops[@]}" -gt 0 ]]; then
    echo "run-two-turn-parallel.sh: failed to drop ${#failed_drops[@]} scratch database(s) -- clean up manually: ${failed_drops[*]/#/DROP DATABASE }" >&2
  fi
  exit "$status"
}
# psql_admin runs one statement against the STANDING instance's own `acr`
# database -- the connection this script uses purely to CREATE/DROP other
# databases. It never touches `acr`'s contents.
#
# ON_ERROR_STOP=1 so a failed CREATE is an error rather than a warning
# followed by a shard that quietly runs against the wrong database.
#
# CHAOS-4116 (2026-08-22 A/B incident, see the scoped-kill skill): retries
# up to PSQL_ADMIN_MAX_ATTEMPTS times, PGCONNECT_TIMEOUT-bounded, ONLY on
# psql's own exit code 2 ("could not connect to server") -- the exact
# handshake-hang signature the incident's ad hoc clone-path.log wrapper was
# built to survive (a stuck Docker Desktop host port-forward proxy, psql
# alive with zero corresponding pg_stat_activity row). A real SQL error
# (ON_ERROR_STOP tripping on a genuine statement failure, exit 1) is
# deterministic -- retrying it would just prove the same failure again more
# slowly, so only exit 2 is retried. ACR_TRIAL_CLONE_PATH_LOG, when set,
# records every attempt (ok/failed, attempt number, the statement) -- the
# incident's own clone-path.log proved this retry-count history IS the
# diagnostic evidence a proxy flake needs, not merely a courtesy.
PSQL_ADMIN_MAX_ATTEMPTS="${ACR_TRIAL_PG_CONNECT_RETRIES:-3}"
PSQL_ADMIN_CONNECT_TIMEOUT="${ACR_TRIAL_PG_CONNECT_TIMEOUT:-15}"
PSQL_ADMIN_RETRY_BACKOFF_SECONDS="${ACR_TRIAL_PG_CONNECT_RETRY_BACKOFF:-2}"
psql_admin() {
  local attempt=1 rc
  while true; do
    # rc is captured INSIDE the else branch, never via "$?" after the
    # if/fi construct -- the classic bash trap this exact bug hit in
    # development (codex round 1, confirmed by direct reproduction): when
    # an `if cmd; then ...; fi` with no `else` takes neither branch's
    # `return`/exit, "$?" immediately after `fi` is the IF STATEMENT's
    # own exit status (0, per POSIX -- "no branch taken" is success), NOT
    # the tested command's real exit code. That silently turned every
    # psql failure into a reported success and a permanent 0-retry no-op.
    # Capturing "$?" as the FIRST statement of the else branch is the
    # only place it still reflects the command that actually ran.
    if PGPASSWORD="$PG_PASSWORD" PGCONNECT_TIMEOUT="$PSQL_ADMIN_CONNECT_TIMEOUT" psql --no-psqlrc -v ON_ERROR_STOP=1 -qtA \
      -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d acr -c "$1"; then
      rc=0
    else
      rc=$?
    fi
    if [[ "$rc" -eq 0 ]]; then
      [[ -n "${ACR_TRIAL_CLONE_PATH_LOG:-}" ]] && printf '%s ok attempt=%d/%d stmt=%s\n' "$(date -u +%H:%M:%S)" "$attempt" "$PSQL_ADMIN_MAX_ATTEMPTS" "$1" >>"$ACR_TRIAL_CLONE_PATH_LOG"
      return 0
    fi
    [[ -n "${ACR_TRIAL_CLONE_PATH_LOG:-}" ]] && printf '%s FAILED attempt=%d/%d rc=%d stmt=%s\n' "$(date -u +%H:%M:%S)" "$attempt" "$PSQL_ADMIN_MAX_ATTEMPTS" "$rc" "$1" >>"$ACR_TRIAL_CLONE_PATH_LOG"
    if [[ "$rc" -ne 2 ]] || ((attempt >= PSQL_ADMIN_MAX_ATTEMPTS)); then
      return "$rc"
    fi
    attempt=$((attempt + 1))
    sleep "$PSQL_ADMIN_RETRY_BACKOFF_SECONDS"
  done
}

# ACR_TRIAL_PSQL_ADMIN_SELFTEST (CHAOS-4116, test-only hook): runs psql_admin
# once against a real database with a harmless statement, then exits --
# never reaches template/shard provisioning. Lets an offline test exercise
# the REAL psql_admin (real retry gating, real -h/-p wiring to PG_HOST/
# PG_PORT) against a FAKE psql placed first on PATH, without provisioning
# anything or requiring a live corpus/annex beyond the one this script
# already validates above. See scripts/trial/test-connect-retry.sh.
if [[ "${ACR_TRIAL_PSQL_ADMIN_SELFTEST:-0}" == "1" ]]; then
  psql_admin "SELECT 1"
  exit $?
fi

drop_trial_database() {
  local db="$1"
  # WITH (FORCE) terminates any session still attached -- a shard whose go
  # test was killed mid-run would otherwise hold the database open and
  # leak it. Postgres 13+; the standing instance is 18.
  psql_admin "DROP DATABASE IF EXISTS \"$db\" WITH (FORCE)" >/dev/null 2>&1
}

trap cleanup EXIT

echo "RUN_TAG=$RUN_TAG SHARD_COUNT=$SHARD_COUNT GRANULARITY=${CASES_PER_SHARD:-none} CONCURRENCY=$MAX_CONCURRENT ORACLE_ANNEX=$ANNEX_PATH"

# --- template database, migrated ONCE ---------------------------------
# The whole point of CHAOS-4100's provisioning change: one migration for
# the run instead of one `go run ./cmd/acr-migrate` per shard. Cloning is a
# file copy, so sixty-five shards cost one migration plus sixty-five copies.
PG_TEMPLATE="acr_trial_tmpl_${RUN_SLUG}"
echo "provisioning template database $PG_TEMPLATE (migrating once for the whole run)..."
template_started="$(date +%s%3N 2>/dev/null || echo 0)"
psql_admin "CREATE DATABASE \"$PG_TEMPLATE\"" >/dev/null
template_dsn="$(trial_pg_dsn "$PG_TEMPLATE")"
( cd "$repo_root" && ACR_POSTGRES_MIGRATION_DSN="$template_dsn" ACR_POSTGRES_CONNECTION_KIND=direct \
    go run ./cmd/acr-migrate up )
template_ms=$(($(date +%s%3N 2>/dev/null || echo 0) - template_started))
echo "template database migrated in ${template_ms}ms"

# CHAOS-4100 (2026-08-23 finding, discovered live via a 2-shard smoke while
# wiring trial_wire_graph_lifecycle_env below): `go run ./cmd/acr-migrate up`
# above only CREATES acr.context_fabric_graph_lifecycle -- it never seeds a
# row into it, so absent this copy every shard clone reads an EMPTY
# lifecycle table for every org regardless of the flag, and
# ResolveActiveEpoch(org) fatally refuses (buildReplayEpochResolver's own
# "refusing to silently measure epoch 0 under a flag that claims otherwise"
# guard) instead of the run-two-turn.sh single-process path's correct
# resolved_active_epoch=2 (that path reads the STANDING acr database
# directly, never a shard clone). pg_dump/psql --data-only, not a hand-built
# INSERT, so jsonb/timestamptz/NULL columns are never re-escaped by hand.
# Copies the WHOLE table (this trial harness has exactly one org of
# interest; unrelated orgs' rows are harmless, inert extras -- filtering by
# org_id would need a second, narrower pg_dump invocation for no real
# benefit). Unconditional, not gated behind the flag: cheap, and a row's
# mere presence is dormant/inert without EpochResolver separately wired --
# see this file's own trial_wire_graph_lifecycle_env comment for that
# byte-identical-when-unused convention.
echo "copying acr.context_fabric_graph_lifecycle from the standing database into the template..."
pg_dump --data-only --table=acr.context_fabric_graph_lifecycle --no-owner "$(trial_pg_dsn acr)" |
  psql --no-psqlrc -v ON_ERROR_STOP=1 -q "$template_dsn"

# --- clone one database per shard, SERIALLY ---------------------------
# Serial deliberately: CREATE DATABASE ... TEMPLATE requires that NOTHING
# else is connected to the template, and two concurrent clones of the same
# template contend on exactly that. Serial is also fast -- the copy is the
# cost, and it is milliseconds for a freshly-migrated schema.
SHARD_DSN=()
SHARD_CLONE_MS=()
for ((i = 0; i < SHARD_COUNT; i++)); do
  shard_db="acr_trial_${RUN_SLUG}_${i}"
  clone_started="$(date +%s%3N 2>/dev/null || echo 0)"
  psql_admin "CREATE DATABASE \"$shard_db\" TEMPLATE \"$PG_TEMPLATE\"" >/dev/null
  clone_ms=$(($(date +%s%3N 2>/dev/null || echo 0) - clone_started))
  PG_DATABASES+=("$shard_db")
  SHARD_DSN+=("$(trial_pg_dsn "$shard_db")")
  SHARD_CLONE_MS+=("$clone_ms")
done
echo "cloned $SHARD_COUNT shard database(s) from $PG_TEMPLATE"

# launch_shard runs ONE shard in the background. Extracted from the loop so
# the bounded-concurrency scheduler below has something to call.
launch_shard() {
  local i="$1"
  local shard_dsn="${SHARD_DSN[$i]}"
  # "none", not "" -- an empty string reads to the harness as "the
  # launcher said nothing", which makes it select by modulo instead of
  # running nothing (codex xhigh review round 4, P2). The two agree today
  # by construction, so nothing duplicates; the sentinel removes the
  # coincidence rather than relying on it.
  local shard_cases="${SHARD_CASES[$i]:-}"
  [[ -z "$shard_cases" ]] && shard_cases="none"

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
      ACR_TEST_TRIAL_SHARD_CASE_INDICES="$shard_cases" \
      ACR_TEST_TRIAL_SHARD_GRANULARITY="${CASES_PER_SHARD:-0}" \
      ACR_TEST_TRIAL_SHARD_CONCURRENCY_CAP="$MAX_CONCURRENT" \
      ACR_TEST_TRIAL_SHARD_PROVISIONING_MODE="template_clone" \
      ACR_TEST_TRIAL_SHARD_DB_PROVISION_MS="${SHARD_CLONE_MS[$i]}" \
      ACR_TEST_TRIAL_EXCHANGE_DIR="$exdir" \
      ACR_TEST_TRIAL_EXCHANGE_TIMEOUT="${ACR_TRIAL_EXCHANGE_TIMEOUT:-10m}" \
      go test -run TestChaos3742TwoTurnConfirmationReplay -count=1 -v -timeout 6h ./internal/runtime/hosted \
      >"$exdir.gotest.log" 2>&1
  ) &
  TEST_PIDS+=("$!")
}

# --- bounded-concurrency scheduler ------------------------------------
# Launch at most MAX_CONCURRENT shards at a time. The pre-CHAOS-4100 loop
# backgrounded every shard unconditionally, which at per-case granularity
# is sixty-five concurrent `codex exec` processes on one subscription (one
# sequential responder per shard).
#
# `wait -n` blocks until ANY child exits, so a finished shard's slot is
# reused immediately rather than waiting for a whole batch -- with a
# long-tail case distribution, batching would cost most of the speedup
# this ticket exists to get.
#
# overall_status accumulates rather than short-circuits: a failing shard
# must not abandon the shards already running, and its non-zero exit still
# refuses the merge below.
overall_status=0

# INFLIGHT holds the TEST pids currently running -- deliberately not "any
# child" (codex xhigh review round 1, P1).
#
# A bare `wait -n` returns when ANY child exits, and launch_shard starts
# TWO: a responder and a test. A responder finishing first -- which is
# exactly what happens when its shard's exchange work is done before the
# test finishes writing its artifact -- would free a slot while that test
# is still running, so the advertised cap could be exceeded and the
# subscription contention this cap exists to bound would recur silently.
# Only test pids are counted, so the cap means what it says.
INFLIGHT=()

# wait_for_one_test blocks until at least one IN-FLIGHT TEST finishes, then
# prunes it from INFLIGHT.
#
# `wait -n` with explicit pid arguments needs bash 5.1+. Where it is
# available a slot is freed by whichever test finishes FIRST, which matters
# because case durations are long-tailed and waiting on the oldest would
# idle a slot behind one slow case. Where it is not, the fallback waits on
# the oldest -- slower in the tail, identical in correctness. The cap is a
# correctness property; the scheduling order is an optimization.
wait_for_one_test() {
  if ((BASH_VERSINFO[0] > 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] >= 1))); then
    wait -n "${INFLIGHT[@]}" || overall_status=1
    local still=()
    for pid in "${INFLIGHT[@]}"; do
      kill -0 "$pid" 2>/dev/null && still+=("$pid")
    done
    INFLIGHT=("${still[@]}")
  else
    wait "${INFLIGHT[0]}" || overall_status=1
    INFLIGHT=("${INFLIGHT[@]:1}")
  fi
}

for ((i = 0; i < SHARD_COUNT; i++)); do
  if [[ -z "${SHARD_CASES[$i]}" ]]; then
    # An empty shard runs no case. It still needs an artifact, because the
    # merge tool requires every shard_index in 0..N-1 to be present and
    # refuses a partial merge -- so it is launched like any other and
    # writes a zero-case report. Only reachable on the modulo path over a
    # sparse annex; granularity-derived layouts never produce one.
    echo "shard $i: no cases assigned (modulo split over a sparse annex)"
  fi
  launch_shard "$i"
  INFLIGHT+=("${TEST_PIDS[${#TEST_PIDS[@]} - 1]}")
  while [[ "${#INFLIGHT[@]}" -ge "$MAX_CONCURRENT" ]]; do
    wait_for_one_test
  done
done

echo "all $SHARD_COUNT shards launched (cap $MAX_CONCURRENT), waiting..."
for pid in "${INFLIGHT[@]}"; do
  if ! wait "$pid"; then
    overall_status=1
  fi
done
INFLIGHT=()
TEST_PIDS=() # every shard already reaped above; trap becomes a no-op for these

for exdir in "${EXCHANGE_DIRS[@]}"; do
  touch "$exdir/DONE"
done
for pid in "${RESPONDER_PIDS[@]}"; do
  wait_with_timeout "$pid" 300 || cleanup_had_issues=1
done
RESPONDER_PIDS=() # waited above; trap becomes a no-op for these

# Same surfacing discipline the container teardown had: a failed drop is
# reported and downgrades a clean exit to 3, never silently swallowed.
failed_drops=()
for db in "${PG_DATABASES[@]}"; do
  drop_trial_database "$db" || failed_drops+=("$db")
done
if [[ -n "$PG_TEMPLATE" ]]; then
  drop_trial_database "$PG_TEMPLATE" || failed_drops+=("$PG_TEMPLATE")
  PG_TEMPLATE=""
fi
if [[ "${#failed_drops[@]}" -gt 0 ]]; then
  echo "run-two-turn-parallel.sh: failed to drop ${#failed_drops[@]} scratch database(s) -- clean up manually: ${failed_drops[*]/#/DROP DATABASE }" >&2
  cleanup_had_issues=1
fi
PG_DATABASES=() # dropped above; trap becomes a no-op for these

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
