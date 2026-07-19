#!/usr/bin/env bash
# Shared jq-backed contract validators for scripts/codegraph/verify-contract.sh.
# Sourced only; every check here parses static JSON and never executes the
# `codegraph` binary, so it cannot mutate a CodeGraph index by construction.
set -euo pipefail

cg_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# cg_manifest_json <manifest-file>
# Prints the manifest file's raw JSON text.
cg_manifest_json() {
  local manifest="$1"
  if [ ! -f "$manifest" ]; then
    printf 'error: manifest not found: %s\n' "$manifest" >&2
    return 1
  fi
  cat "$manifest"
}

# cg_check_caps <manifest-json>
# Asserts the production caps this ADR pins have not drifted.
cg_check_caps() {
  local manifest_json="$1" max_cmds max_depth
  max_cmds=$(jq -r '.max_commands_per_task' <<<"$manifest_json")
  max_depth=$(jq -r '.max_traversal_depth' <<<"$manifest_json")
  if [ "$max_cmds" != "8" ]; then
    printf 'error: manifest max_commands_per_task is %s, contract requires 8\n' "$max_cmds" >&2
    return 1
  fi
  if [ "$max_depth" != "2" ]; then
    printf 'error: manifest max_traversal_depth is %s, contract requires 2\n' "$max_depth" >&2
    return 1
  fi
}

# cg_validate_manifest <manifest-json>
# Checks the fixed command allowlist, examples, and command/depth caps.
cg_validate_manifest() {
  local manifest_json="$1" report ok
  report=$(jq -f "$cg_lib_dir/validate-manifest.jq" <<<"$manifest_json")
  ok=$(jq -r '.ok' <<<"$report")
  if [ "$ok" != "true" ]; then
    printf 'error: manifest violates the fixed CodeGraph allowlist:\n%s\n' "$report" >&2
    return 1
  fi
}

# cg_validate_fixture <fixture-file> <manifest-json> <command-name>
# Deep typed required-field check; tolerates any additive/unknown field.
cg_validate_fixture() {
  local fixture="$1" manifest_json="$2" cmd="$3" report ok
  report=$(jq --argjson m "$manifest_json" --arg cmd "$cmd" -f "$cg_lib_dir/validate-fixture.jq" "$fixture")
  ok=$(jq -r '.ok' <<<"$report")
  if [ "$ok" != "true" ]; then
    printf 'error: %s fixture %s failed deep schema validation:\n%s\n' "$cmd" "$fixture" "$report" >&2
    return 1
  fi
}

# cg_scan_indexed_commit_keys <fixture-file> <manifest-json>
# Detects any forbidden raw indexed-commit/ref-shaped key recursively.
cg_scan_indexed_commit_keys() {
  local fixture="$1" manifest_json="$2" report ok
  report=$(jq --argjson m "$manifest_json" -f "$cg_lib_dir/scan-indexed-commit.jq" "$fixture")
  ok=$(jq -r '.ok' <<<"$report")
  if [ "$ok" != "true" ]; then
    printf 'error: %s carries a forbidden raw indexed-commit/ref field:\n%s\n' "$fixture" "$report" >&2
    return 1
  fi
}

# cg_version_in_range <version> <manifest-json>
# Accepts only strict numeric X.Y.Z SemVer inside the pinned range.
cg_version_in_range() {
  local version="$1" manifest_json="$2" ok
  ok=$(jq -n --argjson m "$manifest_json" --arg version "$version" -f "$cg_lib_dir/version-in-range.jq" | jq -r '.ok')
  [ "$ok" = "true" ]
}

# cg_validate_fixture_versions <fixtures-dir> <manifest-json>
# The fixture directory and status output must identify the exact observed
# supported CodeGraph version; no partial, pre-release, or inferred version is
# acceptable.
cg_validate_fixture_versions() {
  local fixtures="$1" manifest_json="$2" observed status_version expected_dir
  observed=$(jq -r '.observed_codegraph_version' <<<"$manifest_json")
  expected_dir="v$(jq -r '.fixture_version' <<<"$manifest_json")"
  status_version=$(jq -r '.version' "$fixtures/status.json")
  if ! cg_version_in_range "$observed" "$manifest_json"; then
    printf 'error: observed_codegraph_version %s is not strict supported SemVer\n' "$observed" >&2
    return 1
  fi
  if ! cg_version_in_range "$status_version" "$manifest_json"; then
    printf 'error: status.version %s is not strict supported SemVer\n' "$status_version" >&2
    return 1
  fi
  if [ "$observed" != "$status_version" ]; then
    printf 'error: status.version %s does not equal observed_codegraph_version %s\n' "$status_version" "$observed" >&2
    return 1
  fi
  if [ "$(basename "$fixtures")" != "$expected_dir" ]; then
    printf 'error: fixture directory %s must equal %s\n' "$(basename "$fixtures")" "$expected_dir" >&2
    return 1
  fi
}
