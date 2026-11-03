#!/usr/bin/env bash
# Executes the in-release FalkorDB AOF scripts (CHAOS-12650) offline, with a stub
# run.sh and a stub redis-cli, on both branches of the start wrapper and every
# exit of the postStart hook. The scripts are the chart's own files, so the bytes
# tested here are the bytes the ConfigMap ships (scripts/deploy/test-helm.sh pins
# that the ConfigMap renders them unchanged).
#
# Why the wrapper exists: Redis loads the AOF instead of dump.rdb when appendonly
# is on at startup. On a data dir that holds only dump.rdb it starts EMPTY and
# writes an empty AOF base (executed on falkordb@sha256:ad09d505..., 1,000 nodes
# -> 0). So AOF may go on at startup only when an AOF manifest exists or there is
# no dump to lose; otherwise the hook turns it on after the load.
#
# Exit 0 when every case holds, 1 on the first failed case.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
files="$repo_root/deploy/helm/acr/files"
start="$files/falkordb-start.sh"
hook="$files/falkordb-aof-on.sh"
for f in "$start" "$hook"; do
  [[ -f "$f" ]] || { echo "FAIL: $f is missing" >&2; exit 1; }
done

work="$(mktemp -d "${TMPDIR:-/tmp}/falkordb-aof-test.XXXXXX")"
trap 'rm -rf -- "$work"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok - $*"; }

# --- the start wrapper -------------------------------------------------------
mkdir -p "$work/bin"
cat >"$work/bin/run.sh" <<'EOF'
#!/bin/sh
printf 'REDIS_ARGS=[%s]\n' "${REDIS_ARGS:-}"
EOF
chmod +x "$work/bin/run.sh"

run_start() { # $1 data dir -> the wrapper's stdout (its own line and the stub's)
  env -i PATH="$PATH" FALKORDB_DATA_PATH="$1" FALKORDB_BIN_PATH="$work/bin" REDIS_ARGS="--existing yes" sh "$start"
}

data="$work/manifest"; mkdir -p "$data/appendonlydir"; : >"$data/dump.rdb"; : >"$data/appendonlydir/appendonly.aof.manifest"
out="$(run_start "$data")"
grep -qF 'REDIS_ARGS=[--existing yes --appendonly yes --appendfsync everysec]' <<<"$out" \
  || fail "start: an AOF manifest must start with AOF on, keeping REDIS_ARGS: $out"
pass "start: AOF manifest present -> appendonly yes, appendfsync everysec, operator REDIS_ARGS kept"

data="$work/rdb-only"; mkdir -p "$data"; : >"$data/dump.rdb"
out="$(run_start "$data")"
grep -qF 'REDIS_ARGS=[--existing yes]' <<<"$out" || fail "start: dump.rdb with no AOF manifest must start with AOF off: $out"
grep -qF 'starting from dump.rdb with AOF off' <<<"$out" || fail "start: the RDB-only branch must say why AOF is off: $out"
pass "start: dump.rdb and no AOF manifest -> AOF off, said in the log"

data="$work/rdb-and-empty-aofdir"; mkdir -p "$data/appendonlydir"; : >"$data/dump.rdb"
out="$(run_start "$data")"
grep -qF 'REDIS_ARGS=[--existing yes]' <<<"$out" || fail "start: an AOF dir with no manifest (a rewrite cut short) must not start with AOF on: $out"
pass "start: an AOF dir without a manifest counts as no AOF"

data="$work/empty"; mkdir -p "$data"
out="$(run_start "$data")"
grep -qF 'REDIS_ARGS=[--existing yes --appendonly yes --appendfsync everysec]' <<<"$out" \
  || fail "start: an empty data dir has nothing to lose and must start with AOF on: $out"
pass "start: empty data dir -> AOF on"

# --- the postStart hook ------------------------------------------------------
# The stub redis-cli reads its state from files in $STATE and appends each call
# to $STATE/calls. loading: 0|1; appendonly: yes|no; set: OK or an error reply
# for CONFIG SET appendonly; rewrite: the INFO lines once AOF is on.
mkdir -p "$work/stub"
cat >"$work/stub/redis-cli" <<'EOF'
#!/bin/sh
echo "$*" >>"$STATE/calls"
case "$*" in
  "INFO persistence")
    printf 'loading:%s\r\n' "$(cat "$STATE/loading")"
    if [ "$(cat "$STATE/appendonly")" = yes ]; then cat "$STATE/rewrite"; fi ;;
  "CONFIG GET appendonly") printf 'appendonly\r\n%s\r\n' "$(cat "$STATE/appendonly")" ;;
  "CONFIG GET appendfsync") printf 'appendfsync\r\neverysec\r\n' ;;
  "CONFIG SET appendfsync everysec") printf 'OK\r\n' ;;
  "CONFIG SET appendonly yes")
    reply="$(cat "$STATE/set")"
    printf '%s\r\n' "$reply"
    if [ "$reply" = OK ]; then echo yes >"$STATE/appendonly"; fi ;;
  *) echo "stub redis-cli: unexpected call: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "$work/stub/redis-cli"

hook_case() { # $1 name, then key=value state; runs the hook, sets rc and log
  local name="$1"; shift
  STATE="$work/state-$name"; mkdir -p "$STATE"
  echo 0 >"$STATE/loading"; echo no >"$STATE/appendonly"; echo OK >"$STATE/set"
  printf 'aof_rewrite_in_progress:0\r\naof_rewrite_scheduled:0\r\naof_last_bgrewrite_status:ok\r\n' >"$STATE/rewrite"
  local kv
  for kv in "$@"; do printf '%b\n' "${kv#*=}" >"$STATE/${kv%%=*}"; done
  : >"$STATE/log"
  set +e
  env -i PATH="$work/stub:$PATH" STATE="$STATE" ACR_FALKORDB_LOG="$STATE/log" ACR_FALKORDB_AOF_WAIT_SECONDS=3 ACR_FALKORDB_AOF_POLL_SECONDS=1 sh "$hook" >/dev/null 2>&1
  rc=$?
  set -e
  log="$(cat "$STATE/log")"
}

hook_case already appendonly=yes
[[ $rc -eq 0 ]] || fail "hook: AOF already on must exit 0 (rc $rc): $log"
grep -qF 'AOF is on (appendfsync everysec)' <<<"$log" || fail "hook: AOF already on must say so: $log"
grep -q 'CONFIG SET' "$STATE/calls" && fail "hook: AOF already on must not CONFIG SET anything"
pass "hook: AOF already on -> logged, no CONFIG SET, exit 0"

hook_case turn-on
[[ $rc -eq 0 ]] || fail "hook: turning AOF on must exit 0 (rc $rc): $log"
grep -qxF 'CONFIG SET appendonly yes' "$STATE/calls" || fail "hook: must CONFIG SET appendonly yes"
grep -qF 'CONFIG SET appendonly yes: OK' <<<"$log" || fail "hook: must log the CONFIG SET result: $log"
grep -qF 'AOF base written' <<<"$log" || fail "hook: must log that the AOF base was written: $log"
pass "hook: AOF off after an RDB start -> CONFIG SET appendonly yes, result and base logged, exit 0"

hook_case set-refused set='ERR something refused'
[[ $rc -ne 0 ]] || fail "hook: a refused CONFIG SET must fail the hook"
grep -qF 'FAILED: CONFIG SET appendonly yes: ERR something refused' <<<"$log" || fail "hook: a refused CONFIG SET must be logged: $log"
pass "hook: a refused CONFIG SET -> FAILED in the log, non-zero exit"

hook_case rewrite-err rewrite='aof_rewrite_in_progress:0\r\naof_rewrite_scheduled:0\r\naof_last_bgrewrite_status:err\r'
[[ $rc -ne 0 ]] || fail "hook: a failed AOF rewrite must fail the hook"
grep -qF 'FAILED: the AOF base rewrite failed' <<<"$log" || fail "hook: a failed rewrite must be logged: $log"
pass "hook: a failed AOF base rewrite -> FAILED in the log, non-zero exit"

hook_case rewrite-stuck rewrite='aof_rewrite_in_progress:1\r\naof_rewrite_scheduled:0\r\naof_last_bgrewrite_status:ok\r'
[[ $rc -ne 0 ]] || fail "hook: a rewrite that never ends must fail the hook at its deadline"
grep -qF 'FAILED: the AOF base rewrite did not finish' <<<"$log" || fail "hook: a stuck rewrite must be logged: $log"
pass "hook: an AOF rewrite past the deadline -> FAILED, non-zero exit"

hook_case never-loaded loading=1
[[ $rc -ne 0 ]] || fail "hook: a server that never finishes loading must fail the hook at its deadline"
grep -qF 'FAILED: the server did not finish loading' <<<"$log" || fail "hook: a load past the deadline must be logged: $log"
grep -q 'CONFIG SET' "$STATE/calls" && fail "hook: must not CONFIG SET before the load ends"
pass "hook: a load past the deadline -> FAILED, no CONFIG SET, non-zero exit"

echo "test-falkordb-aof: all cases passed"
