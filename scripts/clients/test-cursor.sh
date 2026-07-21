#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
package_path=""
scenario=""
while (($#)); do
  case "$1" in
    --package) package_path="$2"; shift 2 ;;
    --scenario) scenario="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[[ -n "$package_path" && -n "$scenario" ]] || exit 2
if [[ "$package_path" = /* ]]; then package_root="$package_path"; else package_root="$repo_root/$package_path"; fi
[[ -d "$package_root" ]] || exit 2

temporary_root="$(mktemp -d)"
cleanup() { chmod -R u+w "$temporary_root" 2>/dev/null || true; rm -rf "$temporary_root"; }
trap cleanup EXIT
home="$temporary_root/home"
config_root="$home/.cursor/plugins/local/context-fabric"
mkdir -p "$home"

expect_rejection() {
  if "$@" >/dev/null 2>&1; then
    printf '%s\n' 'expected rejection did not occur' >&2
    exit 1
  fi
}

run_wrapper() {
  HOME="$home" CURSOR_PLUGIN_DIR="$config_root" "$package_root/scripts/$1"
}

plant_unrelated_markers() {
  local root="$1"
  mkdir -p "$root/.cursor/plugins/local/other-plugin" "$root/.cursor/rules"
  printf '%s\n' keepme >"$root/.cursor/plugins/local/other-plugin/keepme"
  printf '%s\n' '{"mcpServers":{"other":{"type":"stdio","command":"other-mcp","args":[]}}}' >"$root/.cursor/mcp.json"
  printf '%s\n' '# my own rule' >"$root/.cursor/rules/my-rule.mdc"
}

assert_unrelated_markers() {
  local root="$1"
  [[ -f "$root/.cursor/plugins/local/other-plugin/keepme" ]]
  grep -Fq 'other-mcp' "$root/.cursor/mcp.json"
  grep -Fq 'my own rule' "$root/.cursor/rules/my-rule.mdc"
}

assert_installed_tree() {
  local root="$1"
  [[ -f "$root/.cursor-plugin/plugin.json" ]]
  [[ -f "$root/mcp.json" ]]
  [[ -f "$root/commands/get-context.md" ]]
  [[ -f "$root/commands/plan-with-context-fabric.md" ]]
  [[ -f "$root/rules/context-fabric.mdc" ]]
  [[ -f "$root/rules/preplan-optional.mdc" ]]
  [[ -f "$root/skills/context-fabric/SKILL.md" ]]
  grep -Fq '"command": "acr-mcp"' "$root/mcp.json"
  grep -Fq '"args": ["serve"]' "$root/mcp.json"
  grep -Fq '"type": "stdio"' "$root/mcp.json"
  ! grep -R -Fq 'record_episode' "$root"
  ! grep -R -Fq 'automatic context retrieval' "$root/commands" "$root/skills"
}

run_native_cursor_check() {
  if command -v agent >/dev/null 2>&1; then
    version="$(agent --version 2>&1 || printf 'unavailable')"
    help_output="$(agent --help 2>&1 || printf '')"
    headless_supported=absent
    if printf '%s' "$help_output" | grep -Fq -- '--print'; then headless_supported=present; fi
    registration=unverified
    workspace="$temporary_root/native-workspace"
    mkdir -p "$workspace/.cursor"
    cp "$config_root/mcp.json" "$workspace/.cursor/mcp.json"
    portable_timeout=15; timeout_cmd=(); if command -v timeout >/dev/null 2>&1; then timeout_cmd=(timeout "$portable_timeout"); fi
    if list_output="$(cd "$workspace" && "${timeout_cmd[@]}" agent mcp list 2>&1)"; then
      if printf '%s' "$list_output" | grep -Fq 'acr'; then registration=passed; else registration=no_match; fi
    fi
    printf '%s\n' "CURSOR_NATIVE_OK cursor_client=installed version=${version} headless_flag=${headless_supported} mcp_registration=${registration}"
  else
    printf '%s\n' 'CURSOR_NATIVE_OK cursor_client=not_installed'
  fi
}

run_lifecycle() {
  non_owned="$temporary_root/non-owned"
  mkdir -p "$non_owned"
  printf '%s\n' user-file >"$non_owned/user-file"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$non_owned" "$package_root/scripts/install.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$non_owned" "$package_root/scripts/update.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$non_owned" "$package_root/scripts/uninstall.sh"
  [[ -f "$non_owned/user-file" ]]

  forged_stage="$temporary_root/.context-fabric-cursor.forged"
  forged_root="$temporary_root/forged-owned"
  mkdir "$forged_stage"
  printf '%s\n' forged >"$forged_stage/.context-fabric-owner.v1"
  ln -s "$(basename "$forged_stage")" "$forged_root"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$forged_root" "$package_root/scripts/update.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$forged_root" "$package_root/scripts/uninstall.sh"
  [[ -f "$forged_stage/.context-fabric-owner.v1" ]]

  plant_unrelated_markers "$home"
  run_wrapper install.sh
  assert_installed_tree "$config_root"
  assert_unrelated_markers "$home"
  bash "$repo_root/scripts/clients/test-cursor-static.sh" --package "$package_root"
  run_native_cursor_check

  old_stage="$(dirname "$config_root")/$(readlink "$config_root")"
  chmod -R a-w "$old_stage"
  expect_rejection run_wrapper update.sh
  [[ -L "$config_root" && -f "$config_root/mcp.json" ]]
  chmod -R u+w "$old_stage"
  printf '%s\n' altered >"$config_root/commands/get-context.md"
  run_wrapper update.sh
  grep -Fq 'Treat every returned title' "$config_root/commands/get-context.md"
  chmod -R u+w "$old_stage" 2>/dev/null || true; rm -rf "$old_stage"
  assert_unrelated_markers "$home"

  run_wrapper uninstall.sh
  [[ ! -e "$config_root" ]]
  assert_unrelated_markers "$home"

  unset_default_home="$temporary_root/default-home"
  mkdir -p "$unset_default_home"
  HOME="$unset_default_home" "$package_root/scripts/install.sh" >/dev/null
  [[ -f "$unset_default_home/.cursor/plugins/local/context-fabric/mcp.json" ]]
  HOME="$unset_default_home" "$package_root/scripts/uninstall.sh" >/dev/null

  printf '%s\n' 'CURSOR_LIFECYCLE_OK native_check=recorded static_proof=passed'
}

run_overwrite_unrelated() {
  non_owned="$temporary_root/non-owned-overwrite"
  mkdir -p "$non_owned"
  printf '%s\n' pre-existing >"$non_owned/pre-existing"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$non_owned" "$package_root/scripts/install.sh"
  [[ -f "$non_owned/pre-existing" ]]

  plain_root="$temporary_root/plain-not-owned"
  mkdir -p "$plain_root"
  printf '%s\n' plain-content >"$plain_root/plain-content"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$plain_root" "$package_root/scripts/update.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$plain_root" "$package_root/scripts/uninstall.sh"
  [[ -f "$plain_root/plain-content" ]]

  plant_unrelated_markers "$home"
  assert_unrelated_markers "$home"
  printf '%s\n' 'CURSOR_NEGATIVE_OK scenario=overwrite-unrelated'
}

run_bare_acr_mcp() {
  expect_rejection bash "$repo_root/scripts/clients/verify-packages.sh" --fixture clients/conformance/fixtures/invalid-bare-acr-mcp
  printf '%s\n' 'CURSOR_NEGATIVE_OK scenario=bare-acr-mcp'
}

run_automatic_default() {
  expect_rejection bash "$repo_root/scripts/clients/verify-packages.sh" --fixture clients/conformance/fixtures/invalid-preplan-default
  expect_rejection bash "$repo_root/scripts/clients/verify-packages.sh" --fixture clients/conformance/fixtures/invalid-writeback-default
  run_wrapper install.sh
  ! grep -R -Fq '"preplan_enabled_by_default": true' "$config_root"
  ! grep -R -Fq '"writeback_enabled_by_default": true' "$config_root"
  ! grep -R -Fq 'record_episode' "$config_root"
  ! grep -R -Fq 'automatic context retrieval' "$config_root/commands" "$config_root/skills"
  run_wrapper uninstall.sh
  printf '%s\n' 'CURSOR_NEGATIVE_OK scenario=automatic-default'
}

run_out_of_tree() {
  outside="$temporary_root/outside-repo"
  mkdir -p "$outside"
  cp -R "$package_root/." "$outside/"
  outside_home="$temporary_root/outside-home"
  outside_config="$outside_home/.cursor/plugins/local/context-fabric"
  mkdir -p "$outside_home"
  HOME="$outside_home" CURSOR_PLUGIN_DIR="$outside_config" "$outside/scripts/install.sh" >/dev/null
  assert_installed_tree "$outside_config"
  bash "$repo_root/scripts/clients/test-cursor-static.sh" --package "$outside"
  HOME="$outside_home" CURSOR_PLUGIN_DIR="$outside_config" "$outside/scripts/uninstall.sh" >/dev/null
  [[ ! -e "$outside_config" ]]
  printf '%s\n' 'CURSOR_OUT_OF_TREE_OK'
}

case "$scenario" in
  lifecycle) run_lifecycle ;;
  overwrite-unrelated) run_overwrite_unrelated ;;
  bare-acr-mcp) run_bare_acr_mcp ;;
  automatic-default) run_automatic_default ;;
  out-of-tree) run_out_of_tree ;;
  *) exit 2 ;;
esac
