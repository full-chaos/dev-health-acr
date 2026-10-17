#!/usr/bin/env bash
# parse_clickhouse_backup_db must accept the plain and the PID-suffixed
# backup names and leave anything else unchanged (the caller rejects that).
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
eval "$(sed -n '/^parse_clickhouse_backup_db() {/,/^}/p' "$script_dir/trial-data.sh")"

failures=0
check() {
  local name="$1" want="$2" got
  got="$(parse_clickhouse_backup_db "$name")"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $name want '$want' got '$got'" >&2
    failures=$((failures + 1))
  else
    echo "ok: $name"
  fi
}

check "clickhouse-dh_0906-20261017-101500.zip" "dh_0906"
check "clickhouse-dh_0906-20261017-101500-48213.zip" "dh_0906"
check "clickhouse-my-db-20261017-101500-7.zip" "my-db"
check "clickhouse-default-20261017-101500.zip" "default"
check "clickhouse-dh_0906-20261017-101500-abc.zip" "clickhouse-dh_0906-20261017-101500-abc.zip"
check "clickhouse-dh_0906-2026101-101500.zip" "clickhouse-dh_0906-2026101-101500.zip"

[[ "$failures" -eq 0 ]] || exit 1
