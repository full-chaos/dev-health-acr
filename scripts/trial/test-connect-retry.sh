#!/usr/bin/env bash
# CHAOS-4116: consumer-level checks for run-two-turn-parallel.sh's psql_admin
# retry/host-port wiring, against the REAL function via
# ACR_TRIAL_PSQL_ADMIN_SELFTEST=1 -- never a reimplementation of its logic.
#
# NOT part of `make shard-plan`/`make verify` (unlike test-shard-plan.sh):
# this needs ops/.env for real secrets (trial_secret, via common.sh's
# non-plan-only path), the SAME requirement every other live trial script
# in this directory has. It does NOT need a live postgres, model, or
# subscription -- the one thing psql_admin actually talks to (psql) is
# replaced with a fake binary placed first on PATH that records its own
# argv and returns a scripted exit code, so every check here runs in
# milliseconds with no network egress at all.
#
# MUTATION CHECK PERFORMED (not automated -- see this file's own header
# convention elsewhere in this directory): reverting psql_admin's `-h`/`-p`
# flags to the literal 127.0.0.1/5432 (bypassing $PG_HOST/$PG_PORT) makes
# "host/port override reaches psql_admin" below FAIL; reverting the retry
# gate to retry on any nonzero exit (not just rc=2) makes "no retry on a
# real SQL error" FAIL. Both confirmed during development, then reverted.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
launcher="$script_dir/run-two-turn-parallel.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# A minimal real annex fixture -- ANNEX_PATH's own existence check runs
# before the selftest hook, same as every live invocation; content is
# never read past that (the selftest hook exits right after psql_admin).
cat >"$tmp/annex.json" <<'JSON'
{"provenance":{"corpus_sha8":"deadbeef","signoff":{"by":"t","status":"APPROVED"}},
 "cases":{"0":{"band":"b"}}}
JSON

failures=0
check() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $label" >&2
    echo "  want: $want" >&2
    echo "  got:  $got" >&2
    failures=$((failures + 1))
  else
    echo "ok: $label"
  fi
}

# fake_psql writes a psql stand-in to "$tmp/bin/psql" that:
#   - appends its own argv (one call per line) to "$tmp/calls.log"
#   - returns exit codes from $1 (comma-separated), consumed one per call,
#     the LAST value repeating for any call beyond the list's length.
# PGCONNECT_TIMEOUT/PGPASSWORD reach it as env vars, same as a real psql.
fake_psql() {
  mkdir -p "$tmp/bin"
  local codes="$1"
  cat >"$tmp/bin/psql" <<FAKE
#!/usr/bin/env bash
echo "\$*" >>"$tmp/calls.log"
codes="$codes"
IFS=',' read -r -a arr <<<"\$codes"
n=\$(( \$(grep -c '' "$tmp/calls.log" 2>/dev/null || echo 1) ))
idx=\$(( n - 1 ))
if (( idx >= \${#arr[@]} )); then idx=\$(( \${#arr[@]} - 1 )); fi
exit "\${arr[\$idx]}"
FAKE
  chmod +x "$tmp/bin/psql"
}

run_selftest() {
  : >"$tmp/calls.log"
  PATH="$tmp/bin:$PATH" ACR_TRIAL_SHARD_PLAN_ONLY=0 ACR_TRIAL_PSQL_ADMIN_SELFTEST=1 \
    ACR_TRIAL_PG_CONNECT_RETRY_BACKOFF=0 \
    "$@" "$launcher" "$tmp/annex.json" >"$tmp/stdout.log" 2>"$tmp/stderr.log"
  echo $?
}

# 1. Default host/port (no override): psql_admin calls -h 127.0.0.1 -p 5432.
fake_psql "0"
run_selftest >/dev/null
check "default host/port reaches psql_admin" \
  "1" \
  "$(grep -c -- '-h 127.0.0.1 -p 5432' "$tmp/calls.log")"

# 2. ACR_TRIAL_PG_HOST/PORT override reaches psql_admin's actual -h/-p flags
# (the third construction site -- template_dsn/SHARD_DSN are covered by
# test-shard-plan.sh's trial_pg_dsn check; this one is psql_admin's own).
fake_psql "0"
run_selftest env ACR_TRIAL_PG_HOST=pgrelay.internal ACR_TRIAL_PG_PORT=15432 >/dev/null
check "host/port override reaches psql_admin" \
  "1" \
  "$(grep -c -- '-h pgrelay.internal -p 15432' "$tmp/calls.log")"

# 3. Retry: exit 2 ("could not connect") twice, then succeed. psql_admin
# must retry exactly twice more (3 calls total) and return success.
fake_psql "2,2,0"
status="$(run_selftest)"
check "retries on rc=2, succeeds on 3rd attempt: exit status" "0" "$status"
check "retries on rc=2: exactly 3 psql invocations" "3" "$(grep -c '' "$tmp/calls.log")"

# 4. No retry on a real SQL error (rc=1, e.g. ON_ERROR_STOP tripping on a
# genuine statement failure) -- retrying a deterministic failure only
# proves it again, slower. Exactly ONE invocation, immediate failure.
fake_psql "1"
status="$(run_selftest)"
check "no retry on rc=1 (real SQL error): exit status" "1" "$status"
check "no retry on rc=1: exactly 1 psql invocation" "1" "$(grep -c '' "$tmp/calls.log")"

# 5. Exhausts all 3 attempts on persistent rc=2, returns failure.
fake_psql "2,2,2"
status="$(run_selftest)"
check "exhausts retries on persistent rc=2: exit status" "2" "$status"
check "exhausts retries on persistent rc=2: exactly 3 invocations" "3" "$(grep -c '' "$tmp/calls.log")"

# 6. ACR_TRIAL_CLONE_PATH_LOG records the retry-count history -- the
# incident's own diagnostic evidence, not merely a courtesy (CHAOS-4116).
fake_psql "2,0"
run_selftest env ACR_TRIAL_CLONE_PATH_LOG="$tmp/clone-path.log" >/dev/null
check "clone-path log records one FAILED then one ok" \
  "1
1" \
  "$(printf '%s\n%s' "$(grep -c 'FAILED attempt=1/3' "$tmp/clone-path.log")" "$(grep -c 'ok attempt=2/3' "$tmp/clone-path.log")")"

# 7. ACR_TRIAL_PG_CONNECT_RETRIES overrides the attempt cap.
fake_psql "2,2,2"
status="$(run_selftest env ACR_TRIAL_PG_CONNECT_RETRIES=1)"
check "ACR_TRIAL_PG_CONNECT_RETRIES=1 disables retry: exit status" "2" "$status"
check "ACR_TRIAL_PG_CONNECT_RETRIES=1: exactly 1 invocation" "1" "$(grep -c '' "$tmp/calls.log")"

if [[ "$failures" -gt 0 ]]; then
  echo "connect-retry checks FAILED ($failures)" >&2
  exit 1
fi
echo "connect-retry checks passed"
