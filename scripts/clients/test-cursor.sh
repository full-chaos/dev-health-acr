#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
package_path=""
scenario=""
real_client_if_installed=0
while (($#)); do
  case "$1" in
    --package) package_path="$2"; shift 2 ;;
    --scenario) scenario="$2"; shift 2 ;;
    --real-client-if-installed) real_client_if_installed=1; shift ;;
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

# assert_absent fails explicitly (return, not `!`) so `set -e` reliably
# propagates the failure when called as a plain statement -- a bare
# `! grep ...` is exempt from `set -e` and silently swallows a match.
assert_absent() {
  local needle="$1"; shift
  # A path that is itself a symlink to a directory (config_root always is)
  # needs a trailing slash: this platform's grep -R only recurses through
  # a top-level symlink argument when given one, silently skipping it
  # (and thus finding nothing) otherwise -- see the trailing-slash proof
  # this fix is based on.
  local targets=() t
  for t in "$@"; do
    if [[ -d "$t" ]]; then targets+=("$t/"); else targets+=("$t"); fi
  done
  if grep -R -Fq -- "$needle" "${targets[@]}" 2>/dev/null; then
    printf 'forbidden pattern present: %s\n' "$needle" >&2
    return 1
  fi
  return 0
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
  [[ -f "$root/rules/no-automatic-use.mdc" ]]
  [[ -f "$root/skills/context-fabric/SKILL.md" ]]
  grep -Fq '"command": "acr-mcp"' "$root/mcp.json"
  grep -Fq '"args": ["serve"]' "$root/mcp.json"
  grep -Fq '"type": "stdio"' "$root/mcp.json"
  grep -Fq 'disable-model-invocation: true' "$root/skills/context-fabric/SKILL.md"
  assert_absent 'record_episode' "$root"
  assert_absent 'automatic context retrieval' "$root/commands" "$root/skills"
  bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$root"
}

run_native_cursor_check() {
  if ! command -v agent >/dev/null 2>&1; then
    printf '%s\n' 'CURSOR_NATIVE_OK cursor_client=not_installed'
    return 0
  fi
  local failures=0 workspace timeout_cmd=()
  command -v timeout >/dev/null 2>&1 && timeout_cmd=(timeout 15)
  if ! agent --help >/dev/null 2>&1; then
    printf '%s\n' 'CURSOR_NATIVE_FAIL check=help' >&2; failures=1
  fi
  if ! agent --version >/dev/null 2>&1; then
    printf '%s\n' 'CURSOR_NATIVE_FAIL check=version' >&2; failures=1
  fi
  workspace="$temporary_root/native-workspace-$$"
  mkdir -p "$workspace/.cursor"
  cp "$config_root/mcp.json" "$workspace/.cursor/mcp.json"
  local list_output=""
  if ! list_output="$(cd "$workspace" && "${timeout_cmd[@]}" agent mcp list 2>&1)"; then
    printf '%s\n' 'CURSOR_NATIVE_FAIL check=mcp_list_exit' >&2; failures=1
  elif ! printf '%s' "$list_output" | grep -Fq 'acr'; then
    printf '%s\n' 'CURSOR_NATIVE_FAIL check=mcp_registration' >&2; failures=1
  fi
  if ! (cd "$workspace" && "${timeout_cmd[@]}" agent -p --help >/dev/null 2>&1); then
    printf '%s\n' 'CURSOR_NATIVE_FAIL check=headless_smoke' >&2; failures=1
  fi
  if (( failures )); then
    printf '%s\n' 'CURSOR_NATIVE_FAIL cursor_client=installed' >&2
    return 1
  fi
  printf '%s\n' 'CURSOR_NATIVE_OK cursor_client=installed help=passed version=passed mcp_registration=passed headless_smoke=passed'
}

run_lifecycle() {
  non_owned="$temporary_root/non-owned"
  mkdir -p "$non_owned"
  printf '%s\n' user-file >"$non_owned/user-file"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$non_owned" "$package_root/scripts/install.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$non_owned" "$package_root/scripts/update.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$non_owned" "$package_root/scripts/uninstall.sh"
  [[ -f "$non_owned/user-file" ]]

  forged_root="$temporary_root/forged-owned"
  forged_stages="${forged_root}.stages"
  mkdir -p "$forged_stages/forged"
  printf '%s\n' forged >"$forged_stages/forged/.context-fabric-owner.v1"
  ln -s "$(basename "$forged_root").stages/forged" "$forged_root"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$forged_root" "$package_root/scripts/update.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$forged_root" "$package_root/scripts/uninstall.sh"
  [[ -f "$forged_stages/forged/.context-fabric-owner.v1" ]]

  plant_unrelated_markers "$home"
  run_wrapper install.sh
  assert_installed_tree "$config_root"
  assert_unrelated_markers "$home"
  bash "$repo_root/scripts/clients/test-cursor-static.sh" --package "$package_root"
  if (( real_client_if_installed )); then
    run_native_cursor_check
  else
    printf '%s\n' 'CURSOR_NATIVE_SKIPPED reason=real_client_if_installed_not_set'
  fi

  # Retention: update() never prunes -- every prior generation stays on
  # disk until an owned uninstall, so a reader holding any resolved
  # generation across any number of updates remains valid.
  gen1_stage="$(dirname "$config_root")/$(readlink "$config_root")"
  printf '%s\n' altered >"$config_root/commands/get-context.md"
  run_wrapper update.sh
  grep -Fq 'Treat every returned title' "$config_root/commands/get-context.md"
  gen2_stage="$(dirname "$config_root")/$(readlink "$config_root")"
  [[ "$gen2_stage" != "$gen1_stage" ]]
  [[ -f "$gen1_stage/mcp.json" ]]

  run_wrapper update.sh
  gen3_stage="$(dirname "$config_root")/$(readlink "$config_root")"
  [[ -f "$gen1_stage/mcp.json" ]]
  [[ -f "$gen2_stage/mcp.json" ]]
  assert_unrelated_markers "$home"

  run_wrapper uninstall.sh
  [[ ! -e "$config_root" ]]
  [[ ! -e "$gen1_stage" ]]
  [[ ! -e "$gen2_stage" ]]
  [[ ! -e "$gen3_stage" ]]
  assert_unrelated_markers "$home"

  unset_default_home="$temporary_root/default-home"
  mkdir -p "$unset_default_home"
  HOME="$unset_default_home" "$package_root/scripts/install.sh" >/dev/null
  [[ -f "$unset_default_home/.cursor/plugins/local/context-fabric/mcp.json" ]]
  HOME="$unset_default_home" "$package_root/scripts/uninstall.sh" >/dev/null

  printf '%s\n' 'CURSOR_LIFECYCLE_OK retention=full_until_uninstall static_proof=passed'
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
  assert_absent '"preplan_enabled_by_default": true' "$config_root"
  assert_absent '"writeback_enabled_by_default": true' "$config_root"
  assert_absent 'record_episode' "$config_root"
  assert_absent 'automatic context retrieval' "$config_root/commands" "$config_root/skills"
  run_wrapper uninstall.sh
  printf '%s\n' 'CURSOR_NEGATIVE_OK scenario=automatic-default'
}

# Prove the checks above actually catch what they claim to catch: inject
# each forbidden marker into an installed copy and confirm assert_absent
# rejects it (a check that never fails on injected bad data is worthless).
run_forbidden_pattern_regression() {
  run_wrapper install.sh
  local needle
  for needle in 'record_episode' 'automatic context retrieval' '"preplan_enabled_by_default": true'; do
    printf '%s\n' "$needle" >"$config_root/commands/.regression-injected"
    expect_rejection assert_absent "$needle" "$config_root"
    rm -f "$config_root/commands/.regression-injected"
  done
  run_wrapper uninstall.sh
  printf '%s\n' 'CURSOR_FORBIDDEN_PATTERN_REGRESSION_OK'
}

# Prove the strict manifest validator actually catches what it claims to:
# an unknown field, a missing declared component path, an absolute or
# parent-traversing or non-normalized declared path, and an extra MCP
# server riding alongside the intended one must all fail.
run_manifest_regression() {
  local copy="$temporary_root/manifest-regression-unknown-field"
  cp -R "$package_root" "$copy"
  jq '. + {"unexpectedField": true}' "$copy/.cursor-plugin/plugin.json" >"$copy/.cursor-plugin/plugin.json.tmp"
  mv "$copy/.cursor-plugin/plugin.json.tmp" "$copy/.cursor-plugin/plugin.json"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/manifest-regression-missing-path"
  cp -R "$package_root" "$copy"
  rm -rf "$copy/rules"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/manifest-regression-unknown-frontmatter"
  cp -R "$package_root" "$copy"
  printf -- '---\nname: context-fabric\ndescription: x\nbogus: 1\n---\nbody\n' >"$copy/skills/context-fabric/SKILL.md"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/manifest-regression-absolute-path"
  cp -R "$package_root" "$copy"
  jq '.rules = "/etc/"' "$copy/.cursor-plugin/plugin.json" >"$copy/.cursor-plugin/plugin.json.tmp"
  mv "$copy/.cursor-plugin/plugin.json.tmp" "$copy/.cursor-plugin/plugin.json"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/manifest-regression-traversal-path"
  cp -R "$package_root" "$copy"
  jq '.rules = "../../../etc/"' "$copy/.cursor-plugin/plugin.json" >"$copy/.cursor-plugin/plugin.json.tmp"
  mv "$copy/.cursor-plugin/plugin.json.tmp" "$copy/.cursor-plugin/plugin.json"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/manifest-regression-non-normalized-path"
  cp -R "$package_root" "$copy"
  jq '.rules = "./rules//"' "$copy/.cursor-plugin/plugin.json" >"$copy/.cursor-plugin/plugin.json.tmp"
  mv "$copy/.cursor-plugin/plugin.json.tmp" "$copy/.cursor-plugin/plugin.json"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/manifest-regression-extra-server"
  cp -R "$package_root" "$copy"
  jq '.mcpServers.evil = {"type":"stdio","command":"evil","args":[]}' "$copy/mcp.json" >"$copy/mcp.json.tmp"
  mv "$copy/mcp.json.tmp" "$copy/mcp.json"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"
  bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$package_root"
  printf '%s\n' 'CURSOR_MANIFEST_REGRESSION_OK'
}

# Prove the structured always-apply rule policy actually catches what it
# claims to: mutating the optional preplan rule to alwaysApply:true must
# fail (it must stay manual-only), and a guard rule present but with
# weakened content must also fail -- the guard is accepted only with its
# exact restrictive content and alwaysApply:true.
run_always_apply_regression() {
  local copy="$temporary_root/always-apply-preplan-mutant"
  cp -R "$package_root" "$copy"
  perl -0pi -e 's/alwaysApply: false/alwaysApply: true/' "$copy/rules/preplan-optional.mdc"
  grep -Fq 'alwaysApply: true' "$copy/rules/preplan-optional.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/always-apply-guard-content-mutant"
  cp -R "$package_root" "$copy"
  printf -- '---\nalwaysApply: true\n---\nDo whatever seems useful.\n' >"$copy/rules/no-automatic-use.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/always-apply-guard-not-applied-mutant"
  cp -R "$package_root" "$copy"
  perl -0pi -e 's/alwaysApply: true/alwaysApply: false/' "$copy/rules/no-automatic-use.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$package_root"
  printf '%s\n' 'CURSOR_ALWAYS_APPLY_REGRESSION_OK'
}

# A reader that resolves the symlink once and holds that path across
# several later update() calls must still find its files -- generations
# are retained on disk until an owned uninstall, never pruned.
run_slow_reader_multi_update() {
  run_wrapper install.sh
  local original_resolved
  original_resolved="$(dirname "$config_root")/$(readlink "$config_root")"
  [[ -f "$original_resolved/mcp.json" ]]
  local update_count
  for ((update_count = 1; update_count <= 5; update_count++)); do
    run_wrapper update.sh >/dev/null
  done
  if [[ ! -f "$original_resolved/mcp.json" ]]; then
    printf 'slow reader generation was lost across updates\n' >&2
    exit 1
  fi
  if [[ ! -f "$original_resolved/.context-fabric-owner.v1" ]]; then
    printf 'slow reader owner marker was lost across updates\n' >&2
    exit 1
  fi
  run_wrapper uninstall.sh
  if [[ -e "$original_resolved" ]]; then
    printf 'uninstall did not clean up the retained generation\n' >&2
    exit 1
  fi
  printf '%s\n' 'CURSOR_SLOW_READER_OK generations=6 retained_until_uninstall=true'
}

# Two independent installs sharing the same parent directory must never
# observe, prune, or delete each other's generations -- stages are scoped
# to the exact target path, not the shared parent.
run_two_install_isolation() {
  local config_a="$home/.cursor/plugins/local/context-fabric-a"
  local config_b="$home/.cursor/plugins/local/context-fabric-b"
  HOME="$home" CURSOR_PLUGIN_DIR="$config_a" "$package_root/scripts/install.sh" >/dev/null
  HOME="$home" CURSOR_PLUGIN_DIR="$config_b" "$package_root/scripts/install.sh" >/dev/null
  local update_count
  for ((update_count = 1; update_count <= 3; update_count++)); do
    HOME="$home" CURSOR_PLUGIN_DIR="$config_a" "$package_root/scripts/update.sh" >/dev/null
  done
  local a_count b_count
  a_count="$(find "${config_a}.stages" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
  b_count="$(find "${config_b}.stages" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
  if [[ "$a_count" != 4 ]]; then
    printf 'unexpected A generation count: %s\n' "$a_count" >&2
    exit 1
  fi
  if [[ "$b_count" != 1 ]]; then
    printf 'B was affected by A updates: count=%s\n' "$b_count" >&2
    exit 1
  fi
  HOME="$home" CURSOR_PLUGIN_DIR="$config_a" "$package_root/scripts/uninstall.sh" >/dev/null
  if [[ -e "$config_a" || -e "${config_a}.stages" ]]; then
    printf 'uninstall of A left residue\n' >&2
    exit 1
  fi
  if [[ ! -f "$config_b/mcp.json" ]]; then
    printf 'B was damaged by uninstalling A\n' >&2
    exit 1
  fi
  HOME="$home" CURSOR_PLUGIN_DIR="$config_b" "$package_root/scripts/uninstall.sh" >/dev/null
  printf '%s\n' 'CURSOR_TWO_INSTALL_ISOLATION_OK'
}

# Prove native-check failures actually propagate: a fake `agent` binary
# that always exits 42 must make run_native_cursor_check fail, not
# silently degrade to a warning.
run_native_check_regression() {
  run_wrapper install.sh
  local fake_bin="$temporary_root/fake-agent-bin"
  mkdir -p "$fake_bin"
  cat >"$fake_bin/agent" <<'FAKE'
#!/usr/bin/env bash
exit 42
FAKE
  chmod +x "$fake_bin/agent"
  (
    PATH="$fake_bin:$PATH"
    if run_native_cursor_check >/dev/null 2>&1; then
      printf '%s\n' 'expected native check failure did not occur' >&2
      exit 1
    fi
  )
  run_wrapper uninstall.sh
  printf '%s\n' 'CURSOR_NATIVE_REGRESSION_OK fake_agent_exit42=propagated'
}

# Stress-test the retention scheme: a reader resolving the symlink
# concurrently with several update() swaps must never see a missing
# mcp.json or owner marker file at the path it just resolved.
run_concurrent_readers() {
  run_wrapper install.sh
  local failures_file="$temporary_root/reader-failures.log"
  local stop_file="$temporary_root/reader-stop"
  : >"$failures_file"
  (
    while [[ ! -e "$stop_file" ]]; do
      link="$(readlink "$config_root" 2>/dev/null || true)"
      if [[ -n "$link" ]]; then
        resolved="$(dirname "$config_root")/$link"
        [[ -f "$resolved/mcp.json" ]] || printf 'missing mcp.json at %s\n' "$resolved" >>"$failures_file"
        [[ -f "$resolved/.context-fabric-owner.v1" ]] || printf 'missing owner file at %s\n' "$resolved" >>"$failures_file"
      fi
    done
  ) &
  local reader_pid=$!
  local swap_count
  for ((swap_count = 1; swap_count <= 12; swap_count++)); do
    run_wrapper update.sh >/dev/null
  done
  : >"$stop_file"
  wait "$reader_pid"
  if [[ -s "$failures_file" ]]; then
    printf 'concurrent reader detected missing files:\n' >&2
    cat "$failures_file" >&2
    exit 1
  fi
  run_wrapper uninstall.sh
  printf '%s\n' 'CURSOR_CONCURRENT_READER_STRESS_OK swaps=12 missing_files=0'
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
  forbidden-pattern-regression) run_forbidden_pattern_regression ;;
  manifest-regression) run_manifest_regression ;;
  always-apply-regression) run_always_apply_regression ;;
  slow-reader-multi-update) run_slow_reader_multi_update ;;
  two-install-isolation) run_two_install_isolation ;;
  native-check-regression) run_native_check_regression ;;
  concurrent-readers) run_concurrent_readers ;;
  out-of-tree) run_out_of_tree ;;
  *) exit 2 ;;
esac
