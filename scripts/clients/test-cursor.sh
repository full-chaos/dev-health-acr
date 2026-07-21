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
  # A path that is itself a symlink to a directory needs a trailing slash
  # for this platform's grep -R to recurse through it; config_root is now
  # always a real directory, but keep this defensive for any caller.
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
  [[ -d "$root" && ! -L "$root" ]]
  [[ -f "$root/.context-fabric-owner.v1" && ! -L "$root/.context-fabric-owner.v1" ]]
  grep -Fq 'context-fabric-cursor.v1' "$root/.context-fabric-owner.v1"
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

  plant_unrelated_markers "$home"
  run_wrapper install.sh
  assert_installed_tree "$config_root"
  [[ ! -L "$config_root" ]]
  assert_unrelated_markers "$home"
  bash "$repo_root/scripts/clients/test-cursor-static.sh" --package "$package_root"
  if (( real_client_if_installed )); then
    run_native_cursor_check
  else
    printf '%s\n' 'CURSOR_NATIVE_SKIPPED reason=real_client_if_installed_not_set'
  fi

  printf '%s\n' altered >"$config_root/commands/get-context.md"
  run_wrapper update.sh
  grep -Fq 'Treat every returned title' "$config_root/commands/get-context.md"
  assert_installed_tree "$config_root"
  assert_unrelated_markers "$home"

  run_wrapper uninstall.sh
  [[ ! -e "$config_root" ]]
  assert_unrelated_markers "$home"

  unset_default_home="$temporary_root/default-home"
  mkdir -p "$unset_default_home"
  HOME="$unset_default_home" "$package_root/scripts/install.sh" >/dev/null
  [[ -f "$unset_default_home/.cursor/plugins/local/context-fabric/mcp.json" ]]
  HOME="$unset_default_home" "$package_root/scripts/uninstall.sh" >/dev/null

  printf '%s\n' 'CURSOR_LIFECYCLE_OK model=stable_directory static_proof=passed'
}

# An existing, unowned target must never be adopted, migrated, or written
# into -- install/update/uninstall must all refuse it and leave its
# content exactly as it was.
run_overwrite_unrelated() {
  non_owned="$temporary_root/non-owned-overwrite"
  mkdir -p "$non_owned"
  printf '%s\n' pre-existing >"$non_owned/pre-existing"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$non_owned" "$package_root/scripts/install.sh"
  [[ -f "$non_owned/pre-existing" ]]

  wrong_marker_root="$temporary_root/wrong-marker"
  mkdir -p "$wrong_marker_root"
  printf '%s\n' wrong-owner >"$wrong_marker_root/.context-fabric-owner.v1"
  printf '%s\n' sentinel >"$wrong_marker_root/sentinel-file"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$wrong_marker_root" "$package_root/scripts/update.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$wrong_marker_root" "$package_root/scripts/uninstall.sh"
  [[ -f "$wrong_marker_root/sentinel-file" ]]
  grep -Fq wrong-owner "$wrong_marker_root/.context-fabric-owner.v1"

  missing_marker_root="$temporary_root/missing-marker"
  mkdir -p "$missing_marker_root/.cursor-plugin"
  printf '%s\n' sentinel >"$missing_marker_root/sentinel-file"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$missing_marker_root" "$package_root/scripts/update.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$missing_marker_root" "$package_root/scripts/uninstall.sh"
  [[ -f "$missing_marker_root/sentinel-file" ]]

  plant_unrelated_markers "$home"
  assert_unrelated_markers "$home"
  printf '%s\n' 'CURSOR_NEGATIVE_OK scenario=overwrite-unrelated'
}

# A leftover symlink or junction at the target path (from an earlier,
# unsafe design, or hand-crafted) must never be adopted or migrated --
# every operation fails closed and the link and its target are untouched.
run_legacy_link_fail_closed() {
  legacy_real="$temporary_root/legacy-link-real"
  legacy_target="$temporary_root/legacy-link-target"
  mkdir -p "$legacy_real"
  printf '%s\n' legacy-content >"$legacy_real/some-file"
  ln -s "$(basename "$legacy_real")" "$legacy_target"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$legacy_target" "$package_root/scripts/install.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$legacy_target" "$package_root/scripts/update.sh"
  expect_rejection env HOME="$home" CURSOR_PLUGIN_DIR="$legacy_target" "$package_root/scripts/uninstall.sh"
  [[ -L "$legacy_target" ]]
  [[ -f "$legacy_real/some-file" ]]
  grep -Fq legacy-content "$legacy_real/some-file"
  printf '%s\n' 'CURSOR_LEGACY_LINK_FAIL_CLOSED_OK'
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

# Prove the guard rule is checked for its exact canonical content, not
# merely a required substring: an extra sentence or a reworded (but still
# phrase-containing) body must both fail, and no other rule may claim
# alwaysApply:true.
run_exact_guard_semantics() {
  local copy="$temporary_root/guard-extra-sentence"
  cp -R "$package_root" "$copy"
  printf '\nExtra note that adds nothing important.\n' >>"$copy/rules/no-automatic-use.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/guard-reworded"
  cp -R "$package_root" "$copy"
  perl -0pi -e 's/on your own judgment/at your discretion/' "$copy/rules/no-automatic-use.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/guard-always-apply-false"
  cp -R "$package_root" "$copy"
  perl -0pi -e 's/alwaysApply: true/alwaysApply: false/' "$copy/rules/no-automatic-use.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/guard-missing"
  cp -R "$package_root" "$copy"
  rm "$copy/rules/no-automatic-use.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/other-rule-always-apply-true"
  cp -R "$package_root" "$copy"
  perl -0pi -e 's/alwaysApply: false/alwaysApply: true/' "$copy/rules/context-fabric.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$package_root"
  printf '%s\n' 'CURSOR_EXACT_GUARD_SEMANTICS_OK'
}

# Prove the optional preplan rule is checked for its exact manual-only
# contract: adding a description or globs field -- which would make
# Cursor treat it as agent-decided instead of @-mention-only, per Cursor's
# documented alwaysApply/description/globs table -- must fail, as must
# flipping it to alwaysApply:true or adding any extra content.
run_exact_preplan_semantics() {
  local copy="$temporary_root/preplan-added-description"
  cp -R "$package_root" "$copy"
  perl -0pi -e 's/---\nalwaysApply: false\n---/---\ndescription: Use this to plan ahead of time.\nalwaysApply: false\n---/' "$copy/rules/preplan-optional.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/preplan-added-globs"
  cp -R "$package_root" "$copy"
  perl -0pi -e 's/---\nalwaysApply: false\n---/---\nglobs: "**\/*.ts"\nalwaysApply: false\n---/' "$copy/rules/preplan-optional.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/preplan-always-apply-true"
  cp -R "$package_root" "$copy"
  perl -0pi -e 's/alwaysApply: false/alwaysApply: true/' "$copy/rules/preplan-optional.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/preplan-extra-sentence"
  cp -R "$package_root" "$copy"
  printf ' Also feel free to plan proactively.' >>"$copy/rules/preplan-optional.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  copy="$temporary_root/preplan-missing"
  cp -R "$package_root" "$copy"
  rm "$copy/rules/preplan-optional.mdc"
  expect_rejection bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$copy"

  bash "$repo_root/scripts/clients/validate-cursor-manifest.sh" --package "$package_root"
  printf '%s\n' 'CURSOR_EXACT_PREPLAN_SEMANTICS_OK'
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

# A failure partway through an update (simulated by making a later
# required directory read-only) must leave every required path present --
# files replaced before the failure point on their new content, files not
# yet reached on their old content, nothing ever missing -- and rerunning
# after the obstruction is cleared must converge to the fully updated
# state, including a rewritten marker.
run_mid_update_failure_retry() {
  run_wrapper install.sh
  local marker_before
  marker_before="$(cat "$config_root/.context-fabric-owner.v1")"
  chmod a-w "$config_root/skills/context-fabric"
  printf '%s\n' altered >"$config_root/commands/get-context.md"

  expect_rejection run_wrapper update.sh

  # Nothing required is ever missing, even mid-failure.
  local rel
  for rel in .cursor-plugin/plugin.json mcp.json commands/get-context.md commands/plan-with-context-fabric.md rules/context-fabric.mdc rules/preplan-optional.mdc rules/no-automatic-use.mdc skills/context-fabric/SKILL.md .context-fabric-owner.v1; do
    if [[ ! -f "$config_root/$rel" ]]; then
      printf 'required path missing after injected failure: %s\n' "$rel" >&2
      exit 1
    fi
  done
  # A file earlier in the replacement order was already updated before the
  # failure point was reached.
  grep -Fq 'Treat every returned title' "$config_root/commands/get-context.md"
  # The marker is untouched: the failed run never reached it.
  if [[ "$(cat "$config_root/.context-fabric-owner.v1")" != "$marker_before" ]]; then
    printf 'marker was rewritten despite an earlier failure\n' >&2
    exit 1
  fi

  chmod u+w "$config_root/skills/context-fabric"
  run_wrapper update.sh
  diff -q "$package_root/skills/context-fabric/SKILL.md" "$config_root/skills/context-fabric/SKILL.md" >/dev/null
  assert_installed_tree "$config_root"

  run_wrapper uninstall.sh
  printf '%s\n' 'CURSOR_MID_UPDATE_FAILURE_RETRY_OK'
}

# Running update repeatedly with no change to the source must converge to
# the identical result every time (no accumulating drift, no error on a
# repeat run), and installing into a pre-existing empty directory must
# behave the same as installing where nothing existed at all.
run_idempotency() {
  run_wrapper install.sh
  run_wrapper update.sh >/dev/null
  run_wrapper update.sh >/dev/null
  run_wrapper update.sh >/dev/null
  assert_installed_tree "$config_root"
  run_wrapper uninstall.sh

  local empty_target="$temporary_root/pre-existing-empty"
  mkdir -p "$empty_target"
  HOME="$home" CURSOR_PLUGIN_DIR="$empty_target" "$package_root/scripts/install.sh" >/dev/null
  assert_installed_tree "$empty_target"
  HOME="$home" CURSOR_PLUGIN_DIR="$empty_target" "$package_root/scripts/uninstall.sh" >/dev/null

  printf '%s\n' 'CURSOR_IDEMPOTENCY_OK'
}

# Two independently installed targets sharing the same parent directory
# must never observe or affect each other: each is just its own real
# directory, so updating or uninstalling one cannot touch the other.
run_two_install_isolation() {
  local config_a="$home/.cursor/plugins/local/context-fabric-a"
  local config_b="$home/.cursor/plugins/local/context-fabric-b"
  HOME="$home" CURSOR_PLUGIN_DIR="$config_a" "$package_root/scripts/install.sh" >/dev/null
  HOME="$home" CURSOR_PLUGIN_DIR="$config_b" "$package_root/scripts/install.sh" >/dev/null
  local update_count
  for ((update_count = 1; update_count <= 3; update_count++)); do
    HOME="$home" CURSOR_PLUGIN_DIR="$config_a" "$package_root/scripts/update.sh" >/dev/null
  done
  if [[ ! -f "$config_b/mcp.json" ]]; then
    printf 'B was affected by updating A\n' >&2
    exit 1
  fi
  HOME="$home" CURSOR_PLUGIN_DIR="$config_a" "$package_root/scripts/uninstall.sh" >/dev/null
  if [[ -e "$config_a" ]]; then
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

# Stress-test the atomic per-file replacement: a reader repeatedly
# checking the target's files during many rapid updates must never
# observe a missing mcp.json or owner marker, even though it may see a
# mix of old and new file content -- exactly the accepted tradeoff for
# never needing a whole-directory cutover.
run_concurrent_readers() {
  run_wrapper install.sh
  local failures_file="$temporary_root/reader-failures.log"
  local stop_file="$temporary_root/reader-stop"
  : >"$failures_file"
  (
    while [[ ! -e "$stop_file" ]]; do
      [[ -f "$config_root/mcp.json" ]] || printf 'missing mcp.json\n' >>"$failures_file"
      [[ -f "$config_root/.context-fabric-owner.v1" ]] || printf 'missing owner file\n' >>"$failures_file"
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
  legacy-link-fail-closed) run_legacy_link_fail_closed ;;
  bare-acr-mcp) run_bare_acr_mcp ;;
  automatic-default) run_automatic_default ;;
  forbidden-pattern-regression) run_forbidden_pattern_regression ;;
  manifest-regression) run_manifest_regression ;;
  exact-guard-semantics) run_exact_guard_semantics ;;
  exact-preplan-semantics) run_exact_preplan_semantics ;;
  native-check-regression) run_native_check_regression ;;
  mid-update-failure-retry) run_mid_update_failure_retry ;;
  idempotency) run_idempotency ;;
  two-install-isolation) run_two_install_isolation ;;
  concurrent-readers) run_concurrent_readers ;;
  out-of-tree) run_out_of_tree ;;
  *) exit 2 ;;
esac
