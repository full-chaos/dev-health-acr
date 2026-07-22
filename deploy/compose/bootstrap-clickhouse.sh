#!/usr/bin/env sh
set -eu

: "${ACR_LOCAL_STATE_DIR:=/var/lib/context-fabric}"
: "${ACR_EVIDENCE_DB:?ACR_EVIDENCE_DB is required}"
: "${ACR_LOCAL_REPOSITORY:?ACR_LOCAL_REPOSITORY is required}"
: "${ACR_LOCAL_BRANCH:=main}"
: "${ACR_CLICKHOUSE_READER_PASSWORD_FILE:?ACR_CLICKHOUSE_READER_PASSWORD_FILE is required}"

org_id="$(cat "$ACR_LOCAL_STATE_DIR/org-id")"
reader_password="$(cat "$ACR_CLICKHOUSE_READER_PASSWORD_FILE")"
case "$reader_password" in
  *[!0-9a-f]*) printf 'reader password must be lowercase hex\n' >&2; exit 1 ;;
esac

clickhouse-client --host clickhouse --user ch --password ch --multiquery <<SQL
CREATE USER IF NOT EXISTS acr_reader IDENTIFIED WITH sha256_password BY '${reader_password}';
ALTER USER acr_reader IDENTIFIED WITH sha256_password BY '${reader_password}';
ALTER USER acr_reader SETTINGS readonly = 2;
GRANT SELECT ON ${ACR_EVIDENCE_DB}.* TO acr_reader;
INSERT INTO ${ACR_EVIDENCE_DB}.repos
  (id, repo, ref, created_at, settings, tags, last_synced, org_id, provider)
SELECT generateUUIDv4(), '${ACR_LOCAL_REPOSITORY}', '${ACR_LOCAL_BRANCH}', now64(3), NULL, NULL, now64(3), '${org_id}', 'synthetic'
WHERE NOT EXISTS (
  SELECT 1 FROM ${ACR_EVIDENCE_DB}.repos FINAL
  WHERE org_id = '${org_id}' AND repo = '${ACR_LOCAL_REPOSITORY}' AND ref = '${ACR_LOCAL_BRANCH}'
);
SQL
