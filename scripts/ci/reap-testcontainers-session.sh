#!/usr/bin/env bash
# Removes, by id, every container this job's testcontainers session left
# behind. CI runs with the ryuk reaper off (TESTCONTAINERS_RYUK_DISABLED): on
# a slow docker host testcontainers-go terminates its own reaper after one
# failed reconnect and then waits 60 s on the dead container, failing every
# later fixture of the binary. The session label is still set with ryuk off.
# Never a prune: only containers carrying this session's label are touched.
set -euo pipefail
session="${TESTCONTAINERS_SESSION_ID:?TESTCONTAINERS_SESSION_ID is not set}"
ids="$(docker ps -aq --filter "label=org.testcontainers.sessionId=$session")"
left=0 removed=0 failed=0
for id in $ids; do
  left=$((left + 1))
  if docker rm -f "$id" >/dev/null; then
    removed=$((removed + 1))
  else
    failed=$((failed + 1))
  fi
done
printf 'reap: session=%s left=%d removed=%d failed=%d\n' "$session" "$left" "$removed" "$failed"
if [ "$failed" -ne 0 ]; then
  printf '::error::%d testcontainers of session %s could not be removed\n' "$failed" "$session"
  exit 1
fi
