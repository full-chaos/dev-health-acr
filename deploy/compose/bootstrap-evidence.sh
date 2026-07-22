#!/usr/bin/env sh
set -eu

: "${ACR_LOCAL_STATE_DIR:=/var/lib/context-fabric}"
: "${ACR_EVIDENCE_DB:?ACR_EVIDENCE_DB is required}"
: "${ACR_LOCAL_REPOSITORY:?ACR_LOCAL_REPOSITORY is required}"

marker="$ACR_LOCAL_STATE_DIR/evidence-bootstrap.complete"
if [ -s "$marker" ]; then
  exit 0
fi

export CLICKHOUSE_URI="clickhouse://ch:ch@clickhouse:8123/${ACR_EVIDENCE_DB}"
dev-hops migrate clickhouse
dev-hops fixtures generate \
  --sink "$CLICKHOUSE_URI" \
  --db-type clickhouse \
  --repo-name "$ACR_LOCAL_REPOSITORY" \
  --provider synthetic \
  --days 14 \
  --commits-per-day 6 \
  --pr-count 24 \
  --seed 20260722 \
  --with-metrics \
  --with-work-graph

umask 077
printf 'ok\n' >"$marker"
