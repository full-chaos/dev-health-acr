#!/usr/bin/env bash
# Shared, unit-testable freshness check for a resolved trivy-db metadata.json.
#
# CHAOS-3772: the DB digest is resolved fresh from the mirror on every scan
# run (see scan.sh) instead of being pinned in source, so this check can no
# longer go red purely from wall-clock passage against an unmoving pin. What
# remains here is a genuine sanity alarm: if a DB snapshot resolved moments
# ago already reports an UpdatedAt older than max_age_hours, upstream's own
# publishing has stalled -- that is real information about the mirror, not
# our own inaction.
#
# Usage: check_trivy_db_freshness <metadata.json path> <max age hours> <now, epoch seconds>
# Prints a reason to stderr and returns non-zero on any failure.
check_trivy_db_freshness() {
  local metadata="$1" max_age_hours="$2" now="$3"
  local max_age_seconds=$((max_age_hours * 60 * 60))

  test -f "$metadata" || {
    printf 'trivy-db metadata not found: %s\n' "$metadata" >&2
    return 1
  }
  jq -e --argjson now "$now" --argjson max_age "$max_age_seconds" '
    def epoch: sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601;
    .Version == 2
    and (.UpdatedAt | epoch) <= $now
    and (.DownloadedAt | epoch) <= $now
    and (.NextUpdate | epoch) > (.UpdatedAt | epoch)
    and (($now - (.UpdatedAt | epoch)) <= $max_age)
  ' "$metadata" >/dev/null || {
    printf 'resolved trivy-db metadata is invalid, or its UpdatedAt is older than %s hours -- upstream trivy-db publishing looks stalled, this is not a local pin needing refresh\n' "$max_age_hours" >&2
    return 1
  }
}
