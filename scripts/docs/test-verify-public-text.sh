#!/usr/bin/env bash
# Proves scripts/docs/verify-public-text.sh passes every real page under docs/ AND
# fails on every class it claims to catch, on the old internal rollback paragraph, on
# a page the old list never named, and on a scan that finds nothing. Also proves the
# exact-context exemptions pass and the bare forms still fail.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
check="$root/scripts/docs/verify-public-text.sh"

"$check"   # the default scope: every file under docs/

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
  'lowercase-tracker-id:tracker id' \
  'sha256-digest:sha256 digest' \
  'stray-sha256-line:sha256 digest' \
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

# A listed file that does not exist must fail, not be skipped.
set +e
missing="$("$check" --root "$fixtures/missing-file" --list "$fixtures/missing-file/files.txt" 2>&1)"
missing_code=$?
set -e
if [ "$missing_code" -ne 1 ] || ! grep -qF 'FAIL: docs/absent.md is listed but does not exist' <<<"$missing"; then
  printf 'expected the missing listed file to fail with the does-not-exist line, got %s\n%s\n' "$missing_code" "$missing" >&2
  exit 1
fi

# A page the old list never named is checked by the default scope (docs/**) and was not by
# the old list-mode scope.
unl="$fixtures/unlisted-old-page"
"$check" --root "$unl" --list "$unl/files.txt" >/dev/null
set +e
unl_out="$("$check" --root "$unl" 2>&1)"
unl_code=$?
set -e
if [ "$unl_code" -ne 1 ] || ! grep -qF 'FAIL: docs/old.md:1: tracker id' <<<"$unl_out"; then
  printf 'expected the default scope to fail on the unlisted old page, got %s\n%s\n' "$unl_code" "$unl_out" >&2
  exit 1
fi

# The exact-context exemptions pass: an OCI image digest after @sha256:, a SHA-256 asset line and the two literal hosts.
"$check" --root "$fixtures/exempt-pins" >/dev/null

# A default scope with no file under docs/ must fail, not pass.
tmp_empty="$(mktemp -d)"
mkdir "$tmp_empty/docs"
set +e
empty_default="$("$check" --root "$tmp_empty" 2>&1)"
empty_default_code=$?
set -e
rm -rf "$tmp_empty"
if [ "$empty_default_code" -ne 1 ] || ! grep -qF 'FAIL: the list scanned 0 files' <<<"$empty_default"; then
  printf 'expected an empty docs/ tree to fail with the 0-files line, got %s\n%s\n' "$empty_default_code" "$empty_default" >&2
  exit 1
fi

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
