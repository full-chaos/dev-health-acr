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

# cg_validate_fixture <fixture-file> <manifest-json> <command-name>
# Structural required-field check; tolerates any additive/unknown field.
cg_validate_fixture() {
  local fixture="$1" manifest_json="$2" cmd="$3" report ok
  report=$(jq --argjson m "$manifest_json" --arg cmd "$cmd" -f "$cg_lib_dir/validate-fixture.jq" "$fixture")
  ok=$(jq -r '.ok' <<<"$report")
  if [ "$ok" != "true" ]; then
    printf 'error: %s fixture %s failed required-field validation:\n%s\n' "$cmd" "$fixture" "$report" >&2
    return 1
  fi
}

# cg_scan_indexed_commit_keys <fixture-file> <manifest-json>
# Detects any forbidden indexed-commit-shaped key not equal to the sentinel.
cg_scan_indexed_commit_keys() {
  local fixture="$1" manifest_json="$2" report ok
  report=$(jq --argjson m "$manifest_json" -f "$cg_lib_dir/scan-indexed-commit.jq" "$fixture")
  ok=$(jq -r '.ok' <<<"$report")
  if [ "$ok" != "true" ]; then
    printf 'error: %s carries an inferred/forbidden indexed-commit field:\n%s\n' "$fixture" "$report" >&2
    return 1
  fi
}

# cg_version_in_range <version> <manifest-json>
cg_version_in_range() {
  local version="$1" manifest_json="$2" in_range
  in_range=$(jq -n --argjson m "$manifest_json" --arg version "$version" -f "$cg_lib_dir/version-in-range.jq" | jq -r '.in_range')
  [ "$in_range" = "true" ]
}
