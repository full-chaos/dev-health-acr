#!/usr/bin/env bash
# CHAOS-3853 frontier-baseline-arm runner.
# Usage: run-frontier-arm.sh <arm-label> <model> [effort] [limit]
#
# Runs the frontier-model baseline (codex CLI, subscription-billed --
# team-lead ruling: "harnesses not API keys", see
# .remember/feedback_harnesses_not_api_keys.md) over the withheld trial
# corpus. Requires `codex` on PATH and `codex login` already completed
# (this script never touches codex auth itself).
#
# CHAOS-4313 scoping (team-lead ruled 2026-08-26): the ACR_TEST_TRIAL_
# RESPONDER_TRANSPORT=api|codex cutover does NOT apply here -- this arm
# gives codex its own read-only gh/linear-cli/ClickHouse tool-use loop per
# case, never the file-exchange session_nonce/output_schema envelope that
# switch selects between, so it stays codex-subscription-only.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

# CHAOS-4220: this harness does NOT participate in the
# ACR_TRIAL_DATA_PLANE=compose|kiac switch every other trial launcher
# shares via common.sh (trial_wire_common_env, never called here) -- nor
# does the six-var per-store escape hatch (ACR_TRIAL_{PG,CH,FALKOR}_
# {HOST,PORT}) apply here, for the same reason: those vars, even fully
# set (common.sh's own all-or-none check above only validates their
# COMPLETENESS, not that anything downstream reads them), are simply
# never consulted below -- this harness's own
# ACR_TEST_TRIAL_FRONTIER_CLICKHOUSE_CONTAINER is the only lever on its
# ClickHouse target.
# ClickHouse access below is docker-exec-shaped (frontier_trial_live_test.go's
# verifyClickHouseReadOnlyCredential, PLUS the SAME `docker exec %s
# clickhouse-client ...` command embedded verbatim in the case prompt text
# the frontier model itself executes via its own shell tool), never a
# DSN/client-library connection like every other trial script's
# ClickHouse access. The kiac data plane's ClickHouse runs as a
# Kubernetes pod inside a kiac-managed VM, reachable only via `kubectl
# exec`/`container`, never `docker exec` -- there is no container by this
# name for `docker exec` to find.
#
# Before this fix, this script silently ignored ACR_TRIAL_DATA_PLANE
# entirely: common.sh's own `: "${ACR_TRIAL_DATA_PLANE:=kiac}"` (the
# STANDING DEFAULT, chris's order) sets it regardless, but this script's
# own ACR_TEST_TRIAL_FRONTIER_CLICKHOUSE_CONTAINER default
# ("dev-health-clickhouse-1") always pointed at compose -- exactly the
# silent-hybrid-plane trap CHAOS-4100/4186 exist to structurally close
# everywhere else: an operator expecting kiac (today's ambient default)
# would silently get a compose read instead, with no error. Fail loud.
#
# A real kiac-exec redesign is NOT attempted here (CHAOS-4220's own
# scope note permits documenting why instead of fixing): it would need an
# exec-mechanism switch (docker exec vs kubectl exec) threaded through
# both this script AND the embedded case-prompt text the frontier model
# reads, PLUS live re-verification of the read-only credential
# enforcement against the kiac plane -- CHAOS-3853 explicitly verified
# today's docker-exec path "live against the real container" (see
# frontier_trial_live_test.go's own doc comments), and an unverified
# security control is not a control. This lane is also explicitly
# forbidden from touching the kiac cluster (another lane is its sole
# driver), so a kiac-exec path could not be live-verified here even if
# written.
if [[ "$ACR_TRIAL_DATA_PLANE" != "compose" ]]; then
  echo "run-frontier-arm.sh: ACR_TRIAL_DATA_PLANE=$ACR_TRIAL_DATA_PLANE, but this harness only supports 'compose' -- its ClickHouse access is docker-exec-shaped and cannot reach the kiac data plane (CHAOS-4220). Set ACR_TRIAL_DATA_PLANE=compose explicitly to run the frontier baseline arm." >&2
  exit 1
fi

ARM="${1:?arm label required}"
MODEL="${2:?model required, e.g. gpt-5.6-sol}"
EFFORT="${3:-medium}"
LIMIT="${4:-}"

# CHAOS-3853 review P2 (ARM path hygiene): ARM is used raw below to build
# the output report path -- reject anything that is not a plain path
# component LOUDLY here, rather than trusting it downstream. Mirrored on
# the Go side by armLabelPattern in frontier_trial_live_test.go, since
# either entry point can receive an unsanitized value.
[[ "$ARM" =~ ^[A-Za-z0-9_-]+$ ]] || { echo "run-frontier-arm.sh: ARM=\"$ARM\" is not a safe path component (must match ^[A-Za-z0-9_-]+\$)" >&2; exit 1; }

command -v codex >/dev/null 2>&1 || { echo "run-frontier-arm.sh: codex CLI not found on PATH" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "run-frontier-arm.sh: jq not found on PATH (required to size the run timeout from corpus/limit case count)" >&2; exit 1; }

export ACR_TEST_TRIAL_CORPUS="$ACR_TRIAL_CORPUS"
export ACR_TEST_TRIAL_OUT="$ACR_TRIAL_RESULTS_DIR/gen-trial-${ARM}.json"
export ACR_TEST_TRIAL_ARM="$ARM"
export ACR_TEST_TRIAL_FRONTIER_MODEL="$MODEL"
export ACR_TEST_TRIAL_FRONTIER_EFFORT="$EFFORT"
export ACR_TEST_TRIAL_FRONTIER_ALLOWED_REPOS="${ACR_TEST_TRIAL_FRONTIER_ALLOWED_REPOS:-full-chaos/dev-health full-chaos/acr}"
export ACR_TEST_TRIAL_FRONTIER_CLICKHOUSE_CONTAINER="${ACR_TEST_TRIAL_FRONTIER_CLICKHOUSE_CONTAINER:-dev-health-clickhouse-1}"

# CHAOS-3853 team-lead ruling: the frontier-baseline-arm NEVER uses ops/.env's
# admin-grant CLICKHOUSE_USER/PASSWORD (that credential has INSERT/ALTER/DROP
# grants, not read-only). It uses the dedicated frontier_trial_ro user's own
# credential file instead -- 0600, never committed, never echoed here.
frontier_ro_env="$dev_health_root/.remember/frontier-trial-ro.env"
[[ -f "$frontier_ro_env" ]] || { echo "run-frontier-arm.sh: $frontier_ro_env not found -- the frontier_trial_ro ClickHouse credential is required and is never the admin ops/.env one" >&2; exit 1; }
frontier_ro_secret() {
  grep -E "^$1=" "$frontier_ro_env" | cut -d= -f2- | tr -d '"'
}
export ACR_TEST_TRIAL_FRONTIER_CLICKHOUSE_USER="$(frontier_ro_secret FRONTIER_TRIAL_CH_USER)"
export ACR_TEST_TRIAL_FRONTIER_CLICKHOUSE_PASSWORD="$(frontier_ro_secret FRONTIER_TRIAL_CH_PASSWORD)"
if [[ -n "$LIMIT" ]]; then
  export ACR_TEST_TRIAL_LIMIT="$LIMIT"
fi

# CHAOS-3853 review P2: the wrapper's own runaway-protection timeout must be
# derived from actual worst-case work (case_count * per-case timeout + 20%
# slack), not a constant -- a constant 3h cap is already exceeded by the
# full 50-case corpus alone at the harness's own 8m-per-case default
# (50 * 8m = 6h40m). This stays a HARD cap (that's the point of it); it is
# just sized correctly instead of guessed.
parse_duration_seconds() {
  local remaining="$1" original="$1" total=0 num unit
  if [[ "$remaining" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$remaining"
    return 0
  fi
  while [[ -n "$remaining" ]]; do
    if [[ "$remaining" =~ ^([0-9]+)(h|m|s) ]]; then
      num="${BASH_REMATCH[1]}"
      unit="${BASH_REMATCH[2]}"
      case "$unit" in
        h) total=$(( total + num * 3600 ));;
        m) total=$(( total + num * 60 ));;
        s) total=$(( total + num ));;
      esac
      remaining="${remaining#${BASH_REMATCH[0]}}"
    else
      echo "run-frontier-arm.sh: cannot parse duration \"$original\" (supported: an integer number of seconds, or h/m/s components like '8m' or '1h30m')" >&2
      return 1
    fi
  done
  printf '%s\n' "$total"
}

CASE_TIMEOUT_RAW="${ACR_TEST_TRIAL_FRONTIER_CASE_TIMEOUT:-8m}"
CASE_TIMEOUT_SEC="$(parse_duration_seconds "$CASE_TIMEOUT_RAW")" || exit 1
# CHAOS-3853 review P2, round 2: a zero (or "0s") per-case timeout would
# silently zero out the whole computed run cap below -- and Go's own
# `-timeout 0` DISABLES the test timeout entirely, the opposite of a
# runaway-protection cap. Reject it loudly rather than let it flow through.
[[ "$CASE_TIMEOUT_SEC" -gt 0 ]] || { echo "run-frontier-arm.sh: ACR_TEST_TRIAL_FRONTIER_CASE_TIMEOUT=\"$CASE_TIMEOUT_RAW\" resolves to zero seconds -- must be a positive duration (a zero Go test -timeout disables the timeout entirely)" >&2; exit 1; }

# CHAOS-3853 review P2, round 2: case count must honor the SAME precedence
# resolveTrialIndices (Go side) uses -- ACR_TEST_TRIAL_INDICES (an exact,
# comma-separated subset) wins over LIMIT whenever it's set, since that is
# what the test process actually runs regardless of LIMIT's value.
if [[ -n "${ACR_TEST_TRIAL_INDICES:-}" ]]; then
  # CHAOS-4302: plain IFS word-splitting, not a `<<<` here-string -- the
  # same small-here-string deadlock class the CHAOS-4155 fix in common.sh
  # eliminated (save/restore IFS and noglob explicitly, same idiom).
  _frontier_saved_ifs="$IFS"
  IFS=','
  set -f
  # shellcheck disable=SC2206 # word-splitting is the point: IFS=',' above
  # plus noglob (set -f) make this a controlled comma split, not an
  # accidental one.
  _frontier_indices=($ACR_TEST_TRIAL_INDICES)
  set +f
  IFS="$_frontier_saved_ifs"
  CASE_COUNT="${#_frontier_indices[@]}"
elif [[ -n "$LIMIT" ]]; then
  CASE_COUNT="$LIMIT"
else
  CASE_COUNT="$(jq 'length' "$ACR_TRIAL_CORPUS")"
fi
[[ "$CASE_COUNT" =~ ^[0-9]+$ && "$CASE_COUNT" -gt 0 ]] || { echo "run-frontier-arm.sh: could not determine a positive case count (got \"$CASE_COUNT\"; source was ${ACR_TEST_TRIAL_INDICES:+ACR_TEST_TRIAL_INDICES}${ACR_TEST_TRIAL_INDICES:-${LIMIT:+the LIMIT arg}${LIMIT:-jq length of $ACR_TRIAL_CORPUS}})" >&2; exit 1; }

# + 20% slack, integer arithmetic (*12/10).
RUN_TIMEOUT_SEC=$(( CASE_COUNT * CASE_TIMEOUT_SEC * 12 / 10 ))
RUN_TIMEOUT="${RUN_TIMEOUT_SEC}s"

echo "ARM=$ARM MODEL=$MODEL EFFORT=$EFFORT limit=${LIMIT:-full} case_count=$CASE_COUNT case_timeout=${CASE_TIMEOUT_RAW} run_timeout=${RUN_TIMEOUT} started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
( cd "$repo_root" && go test -run TestFrontierTrialCorpus -count=1 -v -timeout "$RUN_TIMEOUT" ./internal/runtime/hosted )
echo "ARM=$ARM finished_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
