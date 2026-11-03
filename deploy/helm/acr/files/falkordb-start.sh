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

if [ -f "$data/appendonlydir/appendonly.aof.manifest" ] || [ ! -e "$data/dump.rdb" ]; then
  REDIS_ARGS="${REDIS_ARGS:-} --appendonly yes --appendfsync everysec"
  export REDIS_ARGS
  echo "acr-falkordb: starting with AOF on (appendfsync everysec)"
else
  echo "acr-falkordb: dump.rdb and no AOF manifest: starting from dump.rdb with AOF off; the postStart hook turns AOF on after the load"
fi

exec "$bin/run.sh"
