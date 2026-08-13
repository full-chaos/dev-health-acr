#!/usr/bin/env bash
# Shared, unit-testable writer for the resolved trivy-db provenance record.
#
# CHAOS-3772 R2-3: under `set -e`, a bare `cp`/redirect failing (a full or
# unwritable report_root) would abort the whole script silently, between
# recording provenance and judging its freshness -- a run with partial
# provenance and no classification message at all. Every write here is
# explicitly rc-checked (never relies on `set -e` to notice) and any
# failure is reported as its own distinct, loud "provenance write failed"
# message -- not silence, not a vulnerability message, not a skipped
# freshness judgment.
#
# Usage: record_trivy_db_provenance <metadata path> <report_root> <mirror> <resolved_at> <ref>
# Writes report_root/trivy-db-metadata.json and
# report_root/trivy-db-snapshot.txt and returns 0 on success. Returns 1 on
# any write failure, having already printed the reason to stderr.
record_trivy_db_provenance() {
  local metadata="$1" report_root="$2" mirror="$3" resolved_at="$4" ref="$5"
  local rc

  rc=0
  cp "$metadata" "${report_root}/trivy-db-metadata.json" || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    printf 'trivy-db provenance write failed (metadata copy, rc=%s) -- disk or permission issue writing %s/trivy-db-metadata.json, not a vulnerability finding\n' \
      "$rc" "$report_root" >&2
    return 1
  fi

  rc=0
  printf 'mirror=%s\nresolved_at=%s\ndigest=%s\n' "$mirror" "$resolved_at" "$ref" \
    >"${report_root}/trivy-db-snapshot.txt" || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    printf 'trivy-db provenance write failed (snapshot record, rc=%s) -- disk or permission issue writing %s/trivy-db-snapshot.txt, not a vulnerability finding\n' \
      "$rc" "$report_root" >&2
    return 1
  fi
}
