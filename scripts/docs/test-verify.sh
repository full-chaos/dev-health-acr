#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
"$root/scripts/docs/verify.sh"

for fixture in acr-runs-codegraph-index sqlite-access inferred-indexed-commit local-config-breaks-hosted; do
  set +e
  output="$("$root/scripts/docs/verify.sh" --root "$root/testdata/docs-invalid/$fixture" 2>&1)"
  exit_code=$?
  set -e
  if [[ "$exit_code" -ne 1 ]]; then
    printf 'expected docs fixture %s to exit 1, got %s\n%s\n' "$fixture" "$exit_code" "$output" >&2
    exit 1
  fi
done
