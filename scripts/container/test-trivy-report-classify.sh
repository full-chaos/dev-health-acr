#!/usr/bin/env bash
# Unit test for trivy_scan_findings (scripts/container/lib/trivy-report-classify.sh).
#
# CHAOS-3772 F4: a trivy report that is valid JSON but carries an empty (or
# HIGH/CRITICAL-free) .Results must be classified as an execution failure,
# never as a vulnerability finding -- otherwise an infra failure prints a
# misleading CVE diagnosis. Pure jq/bash against synthetic fixtures, no
# docker required.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/container/lib/trivy-report-classify.sh
source "${repo_root}/scripts/container/lib/trivy-report-classify.sh"

fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

pass=0
fail=0

expect_findings() {
  local label="$1" report="$2" expected_count="$3"
  local findings actual_count
  if findings="$(trivy_scan_findings "$report")"; then
    actual_count="$(printf '%s\n' "$findings" | grep -c .)"
    if [[ "$actual_count" -eq "$expected_count" ]]; then
      printf 'ok: %s (%s findings)\n' "$label" "$actual_count"
      pass=$((pass + 1))
    else
      printf 'FAIL: %s (expected %s findings, got %s)\n' "$label" "$expected_count" "$actual_count" >&2
      fail=$((fail + 1))
    fi
  else
    printf 'FAIL (expected findings): %s\n' "$label" >&2
    fail=$((fail + 1))
  fi
}

expect_no_findings() {
  local label="$1" report="$2"
  if trivy_scan_findings "$report" >/dev/null 2>&1; then
    printf 'FAIL (expected no findings): %s\n' "$label" >&2
    fail=$((fail + 1))
  else
    printf 'ok: %s (correctly classified as execution failure, not a finding)\n' "$label"
    pass=$((pass + 1))
  fi
}

real="${fixture}/real.json"
jq -n '{Results: [{Target: "x", Vulnerabilities: [
  {VulnerabilityID: "CVE-2026-1", PkgName: "libexample", InstalledVersion: "1.0", FixedVersion: "1.1", Severity: "HIGH"},
  {VulnerabilityID: "CVE-2026-2", PkgName: "libother", InstalledVersion: "2.0", Severity: "CRITICAL"},
  {VulnerabilityID: "CVE-2026-3", PkgName: "libquiet", InstalledVersion: "3.0", FixedVersion: "3.1", Severity: "LOW"}
]}]}' >"$real"
expect_findings 'real HIGH/CRITICAL findings' "$real" 2

# CHAOS-3772 F4: exactly the misdiagnosis case -- valid JSON, .Results
# present, but empty. A nonzero trivy exit against this must not be
# reported as a CVE.
empty_results="${fixture}/empty-results.json"
jq -n '{Results: []}' >"$empty_results"
expect_no_findings 'valid report with empty Results' "$empty_results"

low_only="${fixture}/low-only.json"
jq -n '{Results: [{Target: "x", Vulnerabilities: [
  {VulnerabilityID: "CVE-2026-4", PkgName: "libquiet", InstalledVersion: "3.0", Severity: "LOW"}
]}]}' >"$low_only"
expect_no_findings 'report with only sub-threshold severities' "$low_only"

expect_no_findings 'missing report file' "${fixture}/missing.json"

malformed="${fixture}/malformed.json"
printf 'not json' >"$malformed"
expect_no_findings 'malformed report' "$malformed"

test "$fail" -eq 0 || {
  printf '%s trivy report classification assertions failed, %s passed\n' "$fail" "$pass" >&2
  exit 1
}
printf 'all %s trivy report classification assertions passed\n' "$pass"
