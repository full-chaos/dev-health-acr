#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
"$root/scripts/docs/verify.sh"

for fixture_and_label in \
  'acr-runs-codegraph-index:ACR-owned CodeGraph index lifecycle claim' \
  'sqlite-access:ACR SQLite access claim' \
  'inferred-indexed-commit:inferred indexed commit claim' \
  'local-config-breaks-hosted:local configuration breaks hosted bootstrap claim' \
  'bare-acr-mcp-server:bare acr-mcp server registration' \
  'credential-in-project:credential copied into project configuration' \
  'codegraph-init:CodeGraph initialization or reindex claim' \
  'direct-api:direct hosted or CodeGraph call claim' \
  'automatic-preplan:automatic pre-plan claim' \
  'public-release:public release claim' \
  'compose-pending:stale Compose pending claim'; do
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

# A broken link in a runbook page (docs/runbooks) must fail the link check. The fixture is not marked as an invalid-claims
# fixture, so the run reaches the link check; it also fails for the missing top-level pages, which is expected: the line
# that names the runbook link is what must be there.
set +e
output="$("$root/scripts/docs/verify.sh" --root "$root/testdata/docs-invalid-links/broken-runbook-link" 2>&1)"
exit_code=$?
set -e
if [[ "$exit_code" -ne 1 ]]; then
  printf 'expected the broken-runbook-link fixture to exit 1, got %s\n%s\n' "$exit_code" "$output" >&2
  exit 1
fi
grep -Fq 'docs/runbooks/page.md: broken local link -> missing-page.md' <<<"$output"

