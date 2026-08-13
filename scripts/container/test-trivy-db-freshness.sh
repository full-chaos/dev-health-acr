#!/usr/bin/env bash
# Unit test for check_trivy_db_freshness (scripts/container/lib/trivy-db-freshness.sh).
#
# This simulates the exact defect CHAOS-3772 removed: a DB snapshot whose
# UpdatedAt is far in the past. Before CHAOS-3772 that could only happen
# because the source-committed digest pin itself never advanced, so this
# went red on wall-clock passage alone, guaranteed, every max-age window.
# After CHAOS-3772 the digest is resolved fresh from the mirror on every
# run (scan.sh), so reaching this failure now means the freshly-resolved
# snapshot is itself stale -- a real signal about upstream, not us. This
# test proves the check still catches that case correctly; it is pure
# bash/jq against synthetic fixtures, no docker required.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/container/lib/trivy-db-freshness.sh
source "${repo_root}/scripts/container/lib/trivy-db-freshness.sh"

fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

# Fixed epoch keeps every assertion deterministic regardless of wall-clock
# time or host timezone.
now=1755000000
max_age_hours=168

iso_at() { jq -nr --argjson e "$1" '$e | todate'; }

write_metadata() {
  local path="$1" updated_at="$2" downloaded_at="$3" next_update="$4" version="${5:-2}"
  jq -n \
    --argjson version "$version" \
    --arg updated "$(iso_at "$updated_at")" \
    --arg downloaded "$(iso_at "$downloaded_at")" \
    --arg next "$(iso_at "$next_update")" \
    '{Version: $version, UpdatedAt: $updated, DownloadedAt: $downloaded, NextUpdate: $next}' \
    >"$path"
}

pass=0
fail=0

expect_pass() {
  local label="$1" metadata="$2"
  if check_trivy_db_freshness "$metadata" "$max_age_hours" "$now" 2>/dev/null; then
    printf 'ok: %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL (expected pass): %s\n' "$label" >&2
    fail=$((fail + 1))
  fi
}

expect_fail() {
  local label="$1" metadata="$2"
  if check_trivy_db_freshness "$metadata" "$max_age_hours" "$now" 2>/dev/null; then
    printf 'FAIL (expected failure): %s\n' "$label" >&2
    fail=$((fail + 1))
  else
    printf 'ok: %s (correctly rejected)\n' "$label"
    pass=$((pass + 1))
  fi
}

# A snapshot resolved moments ago, updated an hour ago: must pass.
fresh="${fixture}/fresh.json"
write_metadata "$fresh" $((now - 3600)) $((now - 3600)) $((now + 18000))
expect_pass 'fresh snapshot within max age' "$fresh"

# The exact failure mode CHAOS-3772 eliminated: UpdatedAt 200h in the past,
# past the 168h sanity threshold. The check must still catch this -- it is
# only no longer *reachable from wall-clock passage against a fixed pin*.
stale="${fixture}/stale.json"
write_metadata "$stale" $((now - 200 * 3600)) $((now - 200 * 3600)) $((now - 190 * 3600))
expect_fail 'stale snapshot beyond max age' "$stale"

# Structurally invalid metadata (NextUpdate not after UpdatedAt) must fail
# even when the age itself is within bounds.
inconsistent="${fixture}/inconsistent.json"
write_metadata "$inconsistent" $((now - 3600)) $((now - 3600)) $((now - 7200))
expect_fail 'internally inconsistent metadata' "$inconsistent"

# A missing file (mirror never produced a snapshot) must fail with its own
# distinct reason, not a jq parse error.
expect_fail 'missing metadata file' "${fixture}/missing.json"

test "$fail" -eq 0 || {
  printf '%s freshness check assertions failed, %s passed\n' "$fail" "$pass" >&2
  exit 1
}
printf 'all %s trivy-db freshness assertions passed\n' "$pass"
