#!/usr/bin/env bash
# Proves scripts/docs/verify-public-text.sh passes the listed real pages AND
# fails on every class it claims to catch, on the old internal rollback
# paragraph and on a list that scans nothing.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
check="$root/scripts/docs/verify-public-text.sh"

"$check"

fixtures="$root/testdata/docs-public-text-invalid"

# A clean page must pass.
"$check" --root "$fixtures/clean" --list "$fixtures/clean/files.txt" >/dev/null

for case_and_label in \
  'tracker-id:tracker id' \
  'record-name:drill record name' \
  'lane-name:lane name' \
  'run-id:run id' \
  'record-checksum:record checksum' \
  'local-path:local path' \
  'internal-host:internal host' \
  'old-rollback:drill record name'; do
  fixture="${case_and_label%%:*}"
  label="${case_and_label#*:}"
  set +e
  output="$("$check" --root "$fixtures/$fixture" --list "$fixtures/$fixture/files.txt" 2>&1)"
  exit_code=$?
  set -e
  if [ "$exit_code" -ne 1 ]; then
    printf 'expected fixture %s to exit 1, got %s\n%s\n' "$fixture" "$exit_code" "$output" >&2
    exit 1
  fi
  if ! grep -qF "FAIL: docs/page.md:1: $label" <<<"$output"; then
    printf 'fixture %s failed without the expected "%s" line\n%s\n' "$fixture" "$label" "$output" >&2
    exit 1
  fi
done

# The old rollback paragraph carries all of: a record name, a checksum and a run id.
set +e
old="$("$check" --root "$fixtures/old-rollback" --list "$fixtures/old-rollback/files.txt" 2>&1)"
set -e
for label in 'drill record name' 'record checksum' 'run id'; do
  grep -qF "FAIL: docs/page.md:1: $label" <<<"$old" || { printf 'old-rollback fixture missed the %s hit\n%s\n' "$label" "$old" >&2; exit 1; }
done

# An empty list scans 0 files and must fail.
set +e
empty="$("$check" --root "$fixtures/empty-list" --list "$fixtures/empty-list/files.txt" 2>&1)"
empty_code=$?
set -e
if [ "$empty_code" -ne 1 ] || ! grep -qF 'FAIL: the list scanned 0 files' <<<"$empty"; then
  printf 'expected the empty list to fail with the 0-files line, got %s\n%s\n' "$empty_code" "$empty" >&2
  exit 1
fi

printf 'ok: verify-public-text.sh passes the listed pages and rejects every planted class\n'
