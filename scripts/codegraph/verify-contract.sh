#!/usr/bin/env bash
# Read-only contract verifier for the CodeGraph 1.2.0 local-index CLI contract
# pinned by docs/adr/0005-codegraph-local-index-provider.md (CHAOS-3007 Task 1).
#
# This script NEVER executes the `codegraph` binary. It only parses static
# JSON fixtures under --fixtures against the contract's manifest.json, so it
# cannot mutate any CodeGraph index by construction.
#
# Usage: scripts/codegraph/verify-contract.sh --fixtures DIR [--scenario NAME]
#
# Scenarios:
#   happy (default)          all seven canonical fixtures satisfy the pinned
#                             contract; an additive field is tolerated and a
#                             deliberately incomplete fixture is confirmed
#                             rejected
#   forbidden-command        proves rejection of a forbidden CodeGraph verb
#   inferred-indexed-commit  proves rejection of an inferred indexed commit
#   unsupported-version      proves rejection of an out-of-range version
#   non-json-mode            proves rejection of a non-JSON invocation
#   missing-field            proves rejection of a fixture missing a required field
#   additive-field           proves an unknown additive field is tolerated
#   sqlite-access            proves rejection of direct .codegraph/*.db access
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/contract.sh
source "$script_dir/lib/contract.sh"

fixtures=""
scenario="happy"

integrity_failure() {
  printf 'error: integrity failure: %s\n' "$1" >&2
  exit 3
}

usage() {
  sed -n '2,22p' "${BASH_SOURCE[0]}"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --fixtures)
      [ $# -ge 2 ] || { printf 'error: --fixtures requires a value\n' >&2; exit 2; }
      fixtures="$2"
      shift 2
      ;;
    --fixtures=*)
      fixtures="${1#--fixtures=}"
      shift
      ;;
    --scenario)
      [ $# -ge 2 ] || { printf 'error: --scenario requires a value\n' >&2; exit 2; }
      scenario="$2"
      shift 2
      ;;
    --scenario=*)
      scenario="${1#--scenario=}"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'error: unknown argument %s\n' "$1" >&2
      exit 2
      ;;
  esac
done

[ -n "$fixtures" ] || { printf 'error: --fixtures DIR is required\n' >&2; exit 2; }
[ -d "$fixtures" ] || { printf 'error: --fixtures %s is not a directory\n' "$fixtures" >&2; exit 2; }
fixtures="$(cd "$fixtures" && pwd)"

manifest_file="$fixtures/manifest.json"
if ! manifest_json="$(cg_manifest_json "$manifest_file")"; then
  integrity_failure 'cannot read manifest'
fi
if ! cg_check_caps "$manifest_json"; then
  integrity_failure 'manifest cap validation failed'
fi
if ! cg_validate_manifest "$manifest_json"; then
  integrity_failure 'manifest fixed-argv validation failed'
fi

canonical_commands=(status query callers callees impact affected files)

run_happy() {
  local cmd
  if ! cg_validate_fixture_versions "$fixtures" "$manifest_json"; then
    return 3
  fi
  for cmd in "${canonical_commands[@]}"; do
    if ! cg_validate_fixture "$fixtures/$cmd.json" "$manifest_json" "$cmd"; then
      return 3
    fi
    if ! cg_scan_indexed_commit_keys "$fixtures/$cmd.json" "$manifest_json" "$cmd"; then
      return 3
    fi
    printf 'ok: %s canonical fixture satisfies the pinned contract\n' "$cmd"
  done
  if ! cg_validate_fixture "$fixtures/additive/status.json" "$manifest_json" status; then
    return 3
  fi
  if ! cg_scan_indexed_commit_keys "$fixtures/additive/status.json" "$manifest_json" status; then
    return 3
  fi
  printf 'ok: additive status fixture tolerates an unknown field\n'
  if cg_validate_fixture "$fixtures/invalid/missing-field-status.json" "$manifest_json" status 2>/dev/null; then
    printf 'error: missing-field-status.json unexpectedly passed validation\n' >&2
    return 3
  fi
  printf 'ok: missing-field-status.json is correctly rejected\n'
  printf 'PASS: all permitted CodeGraph 1.2.0 commands satisfy the pinned contract\n'
}

run_forbidden_command() {
  local file="$fixtures/invalid/forbidden-command.json" verb
  verb="$(jq -r '.attempted_argv[1]' "$file")"
  if jq -e --arg v "$verb" '.forbidden_commands | index($v) != null' <<<"$manifest_json" >/dev/null; then
    printf 'rejected: forbidden CodeGraph command "%s" is not permitted in production\n' "$verb" >&2
    return 1
  fi
  printf 'error: manifest integrity failure: "%s" is not classified as forbidden\n' "$verb" >&2
  return 3
}

run_inferred_indexed_commit() {
  local file="$fixtures/invalid/inferred-indexed-commit.json"
  if cg_scan_indexed_commit_keys "$file" "$manifest_json" status 2>/dev/null; then
    printf 'error: fixture integrity failure: inferred-indexed-commit.json passed the raw commit/ref scan\n' >&2
    return 3
  fi
  printf 'rejected: fixture claims an inferred indexed commit; contract requires indexed_commit_unknown\n' >&2
  return 1
}

run_unsupported_version() {
  local file="$fixtures/invalid/unsupported-version.json" version
  version="$(jq -r '.version' "$file")"
  if cg_version_in_range "$version" "$manifest_json"; then
    printf 'error: fixture integrity failure: version %s was unexpectedly supported\n' "$version" >&2
    return 3
  fi
  printf 'rejected: CodeGraph version %s is outside the supported range %s\n' \
    "$version" "$(jq -r '.supported_codegraph_version_range' <<<"$manifest_json")" >&2
  return 1
}

run_non_json_mode() {
  local file="$fixtures/invalid/non-json-mode.json"
  if jq -e '.attempted_argv | index("--json") != null' "$file" >/dev/null; then
    printf 'error: fixture integrity failure: attempted_argv already contains --json\n' >&2
    return 3
  fi
  printf 'rejected: attempted invocation omits --json and would emit non-JSON output\n' >&2
  return 1
}

run_missing_field() {
  local file="$fixtures/invalid/missing-field-status.json"
  if cg_validate_fixture "$file" "$manifest_json" status 2>/dev/null; then
    printf 'error: fixture integrity failure: missing-field-status.json passed validation\n' >&2
    return 3
  fi
  printf 'rejected: missing-field-status.json is missing a required status field\n' >&2
  return 1
}

run_additive_field() {
  local file="$fixtures/additive/status.json"
  if ! cg_validate_fixture "$file" "$manifest_json" status; then
    return 3
  fi
  if ! cg_scan_indexed_commit_keys "$file" "$manifest_json" status; then
    return 3
  fi
  printf 'ok: additive status fixture tolerates an unknown field\n'
}

run_sqlite_access() {
  local file="$fixtures/invalid/sqlite-access.json"
  if jq -e '.attempted_argv | any(test("\\.codegraph/.*\\.db"))' "$file" >/dev/null; then
    printf 'rejected: direct .codegraph/*.db access bypasses the CLI JSON contract\n' >&2
    return 1
  fi
  printf 'error: fixture integrity failure: sqlite-access.json lacks a .codegraph/*.db path\n' >&2
  return 3
}

case "$scenario" in
  happy) run_happy ;;
  forbidden-command) run_forbidden_command ;;
  inferred-indexed-commit) run_inferred_indexed_commit ;;
  unsupported-version) run_unsupported_version ;;
  non-json-mode) run_non_json_mode ;;
  missing-field) run_missing_field ;;
  additive-field) run_additive_field ;;
  sqlite-access) run_sqlite_access ;;
  *)
    printf 'error: unknown scenario "%s"\n' "$scenario" >&2
    exit 2
    ;;
esac
