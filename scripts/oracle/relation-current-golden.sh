#!/usr/bin/env bash
# Regenerates (or with --check, verifies) the golden of the relation-current
# differential oracle by EXECUTING the ops functions, never by reading them.
# Usage: scripts/oracle/relation-current-golden.sh <ops checkout> [--check]
set -euo pipefail
ops="${1:?path to the dev-health ops checkout}"
mode="${2:-}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
dir="$root/internal/contextfabric/devhealthfacts/testdata/relationoracle"
source_file="$ops/internal/jobs/metrics/workitemmetrics/blocked.go"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/workitemmetrics"
cp "$source_file" "$work/workitemmetrics/blocked.go"
cp "$dir/generate_main.go.txt" "$work/main.go"
printf 'module oraclegen\n\ngo 1.22\n' >"$work/go.mod"
(cd "$work" && go run .) >"$work/golden.json"
digest="$(sha256sum "$source_file" | cut -d' ' -f1)"
if [ "$mode" = "--check" ]; then
	cmp "$work/golden.json" "$dir/relation_current_golden.json"
	test "$digest" = "$(cat "$dir/ops_blocked_go.sha256")"
	echo "relation-current golden is current (ops blocked.go $digest)"
	exit 0
fi
cp "$work/golden.json" "$dir/relation_current_golden.json"
echo "$digest" >"$dir/ops_blocked_go.sha256"
