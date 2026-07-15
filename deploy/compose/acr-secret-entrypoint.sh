#!/usr/bin/env sh
set -eu

for name in ACR_POSTGRES_DSN ACR_POSTGRES_MIGRATION_DSN ACR_CLICKHOUSE_DSN ACR_EVIDENCE_ID_ACTIVE_KID ACR_EVIDENCE_ID_KEYS; do
  file_var="${name}_FILE"
  eval "file=\${$file_var:-}"
  if [ -n "$file" ]; then
    [ -r "$file" ] || { printf '%s secret file is unreadable\n' "$name" >&2; exit 1; }
    value="$(cat "$file")"
    [ -n "$value" ] || { printf '%s secret file is empty\n' "$name" >&2; exit 1; }
    export "$name=$value"
  fi
done
exec "$@"
