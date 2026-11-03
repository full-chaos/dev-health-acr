#!/bin/sh
# Start wrapper for the in-release FalkorDB when contextFabric.falkordb.persistence
# .appendOnly is on (CHAOS-12650). It ends in the image's own entrypoint.
#
# Redis loads the AOF instead of dump.rdb when appendonly is on at startup. On a
# data dir that holds only dump.rdb it starts EMPTY and writes an empty AOF base,
# so a plain REDIS_ARGS="--appendonly yes" on an existing graph wipes it. AOF goes
# on at startup only when an AOF manifest exists (Redis then loads the AOF) or when
# there is no dump to lose. Otherwise the server loads dump.rdb with AOF off, and
# the postStart hook (falkordb-aof-on.sh) turns AOF on once the load is done:
# Redis then writes the AOF base from memory, and the next start takes the AOF.
# The RDB save points are left as they are.
set -eu

data="${FALKORDB_DATA_PATH:?FALKORDB_DATA_PATH is not set}"
bin="${FALKORDB_BIN_PATH:?FALKORDB_BIN_PATH is not set}"
aofdir="$data/appendonlydir"

# A stale AOF: AOF was turned off, the graph moved on in dump.rdb alone, and AOF is
# on again. Loading the old AOF would bring back the graph as it was when AOF went
# off. The test is the snapshot time Redis writes into dump.rdb (the ctime aux
# field, taken when the snapshot forks) against the newest write to any AOF file
# (its mtime). A file mtime alone is not enough: a save renames dump.rdb after its
# fork, so dump.rdb can be newer than a write it does not hold. The 2 s slack
# covers a write flushed in the same second as the fork. When the snapshot is
# newer, every AOF write is in it: the AOF directory is moved aside whole (never
# deleted) and the start takes the dump.rdb path, so the postStart hook writes a
# new AOF from memory. An unreadable snapshot time keeps the AOF.
if [ -f "$aofdir/appendonly.aof.manifest" ] && [ -e "$data/dump.rdb" ]; then
  rdb_ctime="$(redis-check-rdb "$data/dump.rdb" 2>/dev/null | sed -n "s/.*AUX FIELD ctime = '\([0-9][0-9]*\)'.*/\1/p" | head -n 1)"
  aof_newest=0
  for f in "$aofdir"/*; do
    [ -f "$f" ] || continue
    m="$(stat -c %Y "$f")"
    [ "$m" -gt "$aof_newest" ] && aof_newest="$m"
  done
  if [ -z "$rdb_ctime" ]; then
    echo "acr-falkordb: WARNING: could not read the snapshot time of dump.rdb; taking the AOF"
  elif [ "$rdb_ctime" -gt $((aof_newest + 2)) ]; then
    aside="$aofdir.stale-$(date -u +%Y%m%dT%H%M%SZ)"
    mv "$aofdir" "$aside"
    echo "acr-falkordb: STALE AOF: dump.rdb was snapshotted at $rdb_ctime, after the last AOF write at $aof_newest; moved $aofdir aside to $aside (kept, not deleted) and starting from dump.rdb"
  fi
fi

if [ -f "$aofdir/appendonly.aof.manifest" ] || [ ! -e "$data/dump.rdb" ]; then
  REDIS_ARGS="${REDIS_ARGS:-} --appendonly yes --appendfsync everysec"
  export REDIS_ARGS
  echo "acr-falkordb: starting with AOF on (appendfsync everysec)"
else
  echo "acr-falkordb: dump.rdb and no AOF manifest: starting from dump.rdb with AOF off; the postStart hook turns AOF on after the load"
fi

exec "$bin/run.sh"
