#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
"$root/scripts/docs/verify.sh"

for fixture_and_label in \
  'acr-runs-codegraph-index:ACR-owned CodeGraph index lifecycle claim' \
  'sqlite-access:ACR SQLite access claim' \
  'inferred-indexed-commit:inferred indexed commit claim' \
  'local-config-breaks-hosted:local configuration breaks hosted bootstrap claim'; do
  fixture="${fixture_and_label%%:*}"
  label="${fixture_and_label#*:}"
  set +e
  output="$("$root/scripts/docs/verify.sh" --root "$root/testdata/docs-invalid/$fixture" 2>&1)"
  exit_code=$?
  set -e
  if [[ "$exit_code" -ne 1 ]]; then
    printf 'expected docs fixture %s to exit 1, got %s\n%s\n' "$fixture" "$exit_code" "$output" >&2
    exit 1
  fi
  grep -Fq "FAIL: $label present in:" <<<"$output"
done
