#!/usr/bin/env bash
# The rendered trial ClickHouse must switch off every system log table and mount the override.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
rendered="$(bash "$script_dir/trial-data.sh" render)"

failures=0
check() {
  if grep -qF -- "$2" <<<"$rendered"; then echo "ok: $1"; else echo "FAIL: $1 (missing: $2)" >&2; failures=$((failures + 1)); fi
}

check "override file is mounted" "mountPath: /etc/clickhouse-server/config.d/logs-off.xml"
check "override file comes from the config map key" "subPath: logs-off.xml"
for log in text_log trace_log processors_profile_log metric_log asynchronous_metric_log part_log query_log query_thread_log query_views_log query_metric_log error_log; do
  check "$log removed" "<$log remove=\"1\"/>"
done

[[ $failures -eq 0 ]] || exit 1
