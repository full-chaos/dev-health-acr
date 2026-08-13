#!/usr/bin/env bash
# Shared, unit-testable classifier for a trivy image scan's nonzero exit.
#
# CHAOS-3772 F4: `trivy image --exit-code 1` returns nonzero both for real
# HIGH/CRITICAL findings and for trivy failing to execute (partial/corrupt
# report, runtime error). A report that is syntactically valid but carries
# an empty .Results, or no HIGH/CRITICAL entries, must not be reported as a
# vulnerability finding -- that would misdiagnose an infra failure as a CVE.
#
# Usage: trivy_scan_findings <report path>
# On success (real HIGH/CRITICAL findings exist), prints one tab-separated
# line per finding and returns 0. Otherwise prints nothing and returns 1 --
# the caller then reports an execution failure, not a vulnerability.
trivy_scan_findings() {
  local report="$1"
  local findings
  findings="$(jq -r '
    [.Results[]?.Vulnerabilities[]? | select(.Severity == "HIGH" or .Severity == "CRITICAL")] |
    .[] |
    "\(.VulnerabilityID)\t\(.PkgName)\tinstalled=\(.InstalledVersion)\tfixed=\(.FixedVersion // "none")\tseverity=\(.Severity)"
  ' "$report" 2>/dev/null)" || return 1
  [[ -n "$findings" ]] || return 1
  printf '%s\n' "$findings"
}
