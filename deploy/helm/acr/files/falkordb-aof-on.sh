#!/bin/sh
# postStart hook for the in-release FalkorDB when contextFabric.falkordb.persistence
# .appendOnly is on (CHAOS-12650). After a start from dump.rdb (falkordb-start.sh
# left AOF off so the graph is not lost), it waits for the load to end and turns
# AOF on: CONFIG SET appendonly yes makes Redis write the AOF base from memory.
#
# Loud by design: every outcome goes to the container log (PID 1's stdout, which
# is where `kubectl logs` reads), and every failure exits non-zero, so the kubelet
# reports FailedPostStartHook and restarts the container. A silent no-op would
# leave AOF off while the values say on. A restart is safe: with no AOF manifest
# the wrapper starts from dump.rdb again and this hook runs again.
set -u

log_target="${ACR_FALKORDB_LOG:-/proc/1/fd/1}"
wait_seconds="${ACR_FALKORDB_AOF_WAIT_SECONDS:-1800}"
poll_seconds="${ACR_FALKORDB_AOF_POLL_SECONDS:-2}"

log() {
  echo "acr-falkordb-aof: $*" >>"$log_target" 2>/dev/null || echo "acr-falkordb-aof: $*"
}
fail() {
  log "FAILED: $*"
  exit 1
}
field() { # $1 INFO field -> its value in INFO persistence, CR stripped
  redis-cli INFO persistence 2>/dev/null | tr -d '\r' | sed -n "s/^$1://p"
}
config() { # $1 parameter -> its value from CONFIG GET
  redis-cli CONFIG GET "$1" 2>/dev/null | tr -d '\r' | sed -n 2p
}

deadline=$(($(date +%s) + wait_seconds))

# The server answers INFO while it loads; LOADING refuses CONFIG SET appendonly.
until [ "$(field loading)" = "0" ]; do
  [ "$(date +%s)" -lt "$deadline" ] || fail "the server did not finish loading within ${wait_seconds}s; AOF stays off"
  sleep "$poll_seconds"
done

if [ "$(config appendonly)" = "yes" ]; then
  log "AOF is on (appendfsync $(config appendfsync))"
  exit 0
fi

reply="$(redis-cli CONFIG SET appendfsync everysec 2>&1 | tr -d '\r')"
[ "$reply" = "OK" ] || fail "CONFIG SET appendfsync everysec: $reply"
reply="$(redis-cli CONFIG SET appendonly yes 2>&1 | tr -d '\r')"
[ "$reply" = "OK" ] || fail "CONFIG SET appendonly yes: $reply"
log "CONFIG SET appendonly yes: OK; waiting for Redis to write the AOF base from memory"

while :; do
  if [ "$(field aof_rewrite_in_progress)" = "0" ] && [ "$(field aof_rewrite_scheduled)" = "0" ]; then
    status="$(field aof_last_bgrewrite_status)"
    [ "$status" = "ok" ] || fail "the AOF base rewrite failed (aof_last_bgrewrite_status ${status:-unread}); AOF is not durable"
    if [ -n "${FALKORDB_DATA_PATH:-}" ] && [ ! -f "$FALKORDB_DATA_PATH/appendonlydir/appendonly.aof.manifest" ]; then
      fail "the AOF base rewrite ended but $FALKORDB_DATA_PATH/appendonlydir/appendonly.aof.manifest is missing"
    fi
    break
  fi
  [ "$(date +%s)" -lt "$deadline" ] || fail "the AOF base rewrite did not finish within ${wait_seconds}s"
  sleep "$poll_seconds"
done

log "AOF base written; AOF is on (appendfsync $(config appendfsync)); the next start loads the AOF"
