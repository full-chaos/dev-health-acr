#!/usr/bin/env bash
# Unit test for record_trivy_db_provenance (scripts/container/lib/trivy-db-provenance.sh).
#
# CHAOS-3772 R2-3: proves an unwritable report_root fails loudly with its
# own distinct message, on both the metadata-copy write and the
# snapshot-record write, instead of aborting silently under `set -e` with
# no classification at all. Pure bash against synthetic fixtures, no
# docker required.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/container/lib/trivy-db-provenance.sh
source "${repo_root}/scripts/container/lib/trivy-db-provenance.sh"

fixture="$(mktemp -d)"
cleanup() {
  chmod -R u+rwx "$fixture" 2>/dev/null || true
  rm -rf "$fixture"
}
trap cleanup EXIT

pass=0
fail=0

metadata="${fixture}/metadata.json"
printf '{"Version":2}\n' >"$metadata"

# Success path: both files land with the expected content.
ok_dir="${fixture}/ok"
mkdir -p "$ok_dir"
if record_trivy_db_provenance "$metadata" "$ok_dir" 'mirror:tag' '2026-08-13T00:00:00Z' 'mirror@sha256:deadbeef' 2>"${fixture}/prov-ok.err"; then
  if [[ -f "${ok_dir}/trivy-db-metadata.json" ]] && grep -q 'digest=mirror@sha256:deadbeef' "${ok_dir}/trivy-db-snapshot.txt"; then
    printf 'ok: success path writes both files with correct content\n'
    pass=$((pass + 1))
  else
    printf 'FAIL: success path did not write expected content\n' >&2
    fail=$((fail + 1))
  fi
else
  printf 'FAIL: success path unexpectedly failed\n' >&2
  fail=$((fail + 1))
fi

# Metadata-copy failure: report_root itself is not writable, so even the
# first write fails.
readonly_dir="${fixture}/readonly"
mkdir -p "$readonly_dir"
chmod 555 "$readonly_dir"
if record_trivy_db_provenance "$metadata" "$readonly_dir" 'mirror:tag' '2026-08-13T00:00:00Z' 'mirror@sha256:deadbeef' >"${fixture}/prov-readonly.out" 2>"${fixture}/prov-readonly.err"; then
  printf 'FAIL: metadata-copy failure was not detected (report_root read-only)\n' >&2
  fail=$((fail + 1))
elif grep -q 'provenance write failed (metadata copy' "${fixture}/prov-readonly.err"; then
  printf 'ok: unwritable report_root fails with a distinct metadata-copy message\n'
  pass=$((pass + 1))
else
  printf 'FAIL: metadata-copy failure did not print the expected distinct message\n' >&2
  cat "${fixture}/prov-readonly.err" >&2
  fail=$((fail + 1))
fi
chmod 755 "$readonly_dir"

# Snapshot-record failure in isolation: report_root is writable (metadata
# copy succeeds), but the snapshot path is pre-occupied by a directory, so
# only the second write fails.
snapshot_blocked_dir="${fixture}/snapshot-blocked"
mkdir -p "$snapshot_blocked_dir"
mkdir -p "${snapshot_blocked_dir}/trivy-db-snapshot.txt"
if record_trivy_db_provenance "$metadata" "$snapshot_blocked_dir" 'mirror:tag' '2026-08-13T00:00:00Z' 'mirror@sha256:deadbeef' >"${fixture}/prov-snap.out" 2>"${fixture}/prov-snap.err"; then
  printf 'FAIL: snapshot-record failure was not detected (path occupied by a directory)\n' >&2
  fail=$((fail + 1))
elif grep -q 'provenance write failed (snapshot record' "${fixture}/prov-snap.err"; then
  if [[ -f "${snapshot_blocked_dir}/trivy-db-metadata.json" ]]; then
    printf 'ok: blocked snapshot path fails with a distinct snapshot-record message, after the metadata copy still landed\n'
    pass=$((pass + 1))
  else
    printf 'FAIL: metadata copy should have already succeeded before the snapshot write failed\n' >&2
    fail=$((fail + 1))
  fi
else
  printf 'FAIL: snapshot-record failure did not print the expected distinct message\n' >&2
  cat "${fixture}/prov-snap.err" >&2
  fail=$((fail + 1))
fi

test "$fail" -eq 0 || {
  printf '%s trivy-db provenance assertions failed, %s passed\n' "$fail" "$pass" >&2
  exit 1
}
printf 'all %s trivy-db provenance assertions passed\n' "$pass"
