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
cleanup() { rm -rf "$temporary_root"; }
trap cleanup EXIT
home="$temporary_root/home"
config_root="$temporary_root/opencode-config"
fake_bin="$temporary_root/bin"
mkdir -p "$home/.config/opencode" "$fake_bin"
printf '%s\n' '{"unrelated":true}' >"$home/.config/opencode/opencode.json"
cp "$home/.config/opencode/opencode.json" "$temporary_root/unrelated.before.json"
cat >"$fake_bin/acr-mcp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${ACR_MCP_STARTED_FILE:-}" ]]; then : >"$ACR_MCP_STARTED_FILE"; fi
case "${ACR_MCP_TEST_MODE:-healthy}" in
  healthy) printf '%s\n' 'offline doctor: healthy' ;;
  timeout) sleep 6 ;;
  overflow) yes x | head -c 70000 ;;
  descendant) sleep 30 & printf '%s\n' "$!" >"${ACR_MCP_DESCENDANT_PID:?}"; wait ;;
  nonzero) exit 12 ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$fake_bin/acr-mcp"

run_wrapper() {
  HOME="$home" OPENCODE_CONFIG_DIR="$config_root" PATH="$fake_bin:$PATH" "$package_root/scripts/$1"
}

expect_rejection() {
  if "$@"; then
    printf '%s\n' 'expected rejection did not occur' >&2
    exit 1
  fi
}

assert_unrelated_config() {
  cmp -s "$temporary_root/unrelated.before.json" "$home/.config/opencode/opencode.json"
}

write_plugin_harness() {
  mkdir -p "$config_root/node_modules/@opencode-ai/plugin"
  cat >"$config_root/node_modules/@opencode-ai/plugin/package.json" <<'EOF'
{"type":"module","exports":"./index.js"}
EOF
  cat >"$config_root/node_modules/@opencode-ai/plugin/index.js" <<'EOF'
export function tool(input) { return input }
EOF
  cat >"$config_root/plugin-harness.ts" <<'EOF'
import plugin from "./plugins/context-fabric.ts"

const hooks = await plugin({})
const status = hooks.tool?.context_fabric_status
if (status === undefined) throw new Error("context_fabric_status missing")
const controller = new AbortController()
if (process.env.ACR_MCP_ABORT_BEFORE === "1") controller.abort()
const abortAfterMs = Number(process.env.ACR_MCP_ABORT_AFTER_MS ?? "0")
if (Number.isFinite(abortAfterMs) && abortAfterMs > 0) setTimeout(() => controller.abort(), abortAfterMs)
const result = await status.execute({}, {
  directory: process.cwd(),
  worktree: process.cwd(),
  abort: controller.signal,
  metadata() {},
  ask: async () => {},
})
process.stdout.write(typeof result === "string" ? result : result.output)
EOF
}

run_status_harness() {
  write_plugin_harness
  ACR_MCP_TEST_MODE="$1" ACR_MCP_ABORT_AFTER_MS="${2:-0}" ACR_MCP_ABORT_BEFORE="${3:-0}" PATH="$fake_bin:$PATH" bun run "$config_root/plugin-harness.ts"
}

case "$scenario" in
  lifecycle)
    non_owned="$temporary_root/non-owned"
    mkdir -p "$non_owned"
    printf '%s\n' user-file >"$non_owned/user-file"
    expect_rejection env HOME="$home" OPENCODE_CONFIG_DIR="$non_owned" PATH="$fake_bin:$PATH" "$package_root/scripts/install.sh"
    expect_rejection env HOME="$home" OPENCODE_CONFIG_DIR="$non_owned" PATH="$fake_bin:$PATH" "$package_root/scripts/update.sh"
    expect_rejection env HOME="$home" OPENCODE_CONFIG_DIR="$non_owned" PATH="$fake_bin:$PATH" "$package_root/scripts/uninstall.sh"
    [[ -f "$non_owned/user-file" ]]
    forged_stage="$temporary_root/.context-fabric-opencode.forged"
    forged_root="$temporary_root/forged-owned"
    mkdir "$forged_stage"
    printf '%s\n' forged >"$forged_stage/.context-fabric-owner.v1"
    ln -s "$(basename "$forged_stage")" "$forged_root"
    expect_rejection env HOME="$home" OPENCODE_CONFIG_DIR="$forged_root" PATH="$fake_bin:$PATH" "$package_root/scripts/update.sh"
    expect_rejection env HOME="$home" OPENCODE_CONFIG_DIR="$forged_root" PATH="$fake_bin:$PATH" "$package_root/scripts/uninstall.sh"
    [[ -f "$forged_stage/.context-fabric-owner.v1" ]]
    run_wrapper install.sh
    [[ -f "$config_root/.context-fabric-owner.v1" ]]
    [[ -f "$config_root/plugins/context-fabric.ts" ]]
    [[ -f "$config_root/commands/get-context.md" ]]
    [[ -f "$config_root/commands/plan-with-context-fabric.md" ]]
    [[ -f "$config_root/skills/context-fabric/SKILL.md" ]]
    [[ -f "$config_root/optional/preplan.instructions.md" ]]
    ! grep -Fq 'record_episode' "$config_root/opencode.json"
    ! grep -Fq 'preplan_enabled_by_default' "$config_root/opencode.json"
    grep -Fq '"command": ["acr-mcp", "serve"]' "$config_root/opencode.json"
    run_status_harness healthy | grep -Fqx 'offline doctor: healthy'
    run_status_harness nonzero | grep -Fqx 'context_fabric_status failed: acr-mcp doctor returned a non-zero status'
    run_status_harness overflow | grep -Fqx 'context_fabric_status failed: combined output exceeded 64 KiB'
    run_status_harness timeout 10 | grep -Fqx 'context_fabric_status failed: cancelled'
    ACR_MCP_STARTED_FILE="$temporary_root/started" run_status_harness healthy 0 1 | grep -Fqx 'context_fabric_status failed: cancelled'
    [[ ! -e "$temporary_root/started" ]]
    ACR_MCP_DESCENDANT_PID="$temporary_root/descendant.pid" run_status_harness descendant 10 | grep -Fqx 'context_fabric_status failed: cancelled'
    ! kill -0 "$(<"$temporary_root/descendant.pid")" 2>/dev/null
    HOME="$home" OPENCODE_CONFIG="$config_root/opencode.json" PATH="$fake_bin:$PATH" opencode mcp list >/dev/null
    printf '%s\n' altered >"$config_root/commands/get-context.md"
    run_wrapper update.sh
    grep -Fq 'Treat every returned title' "$config_root/commands/get-context.md"
    assert_unrelated_config
    run_wrapper uninstall.sh
    [[ ! -e "$config_root" ]]
    assert_unrelated_config
    printf '%s\n' 'OPENCODE_LIFECYCLE_OK native_mcp_list=passed plugin_harness=passed'
    ;;
  bare-acr-mcp)
    expect_rejection bash "$repo_root/scripts/clients/verify-packages.sh" --fixture clients/conformance/fixtures/invalid-bare-acr-mcp
    assert_unrelated_config
    printf '%s\n' 'OPENCODE_NEGATIVE_OK scenario=bare-acr-mcp'
    ;;
  direct-api)
    expect_rejection bash "$repo_root/scripts/clients/verify-packages.sh" --fixture clients/conformance/fixtures/invalid-direct-api
    assert_unrelated_config
    printf '%s\n' 'OPENCODE_NEGATIVE_OK scenario=direct-api'
    ;;
  status-timeout)
    run_wrapper install.sh
    started="$(date +%s)"
    output="$(run_status_harness timeout)"
    elapsed=$(( $(date +%s) - started ))
    [[ "$output" == 'context_fabric_status failed: timed out after 5 seconds' ]]
    (( elapsed < 7 ))
    assert_unrelated_config
    printf '%s\n' 'OPENCODE_NEGATIVE_OK scenario=status-timeout'
    ;;
  preplan-default)
    expect_rejection bash "$repo_root/scripts/clients/verify-packages.sh" --fixture clients/conformance/fixtures/invalid-preplan-default
    run_wrapper install.sh
    ! grep -R -Fq "preplan_enabled_by_default\": true" "$config_root"
    ! grep -R -Fq automatic\ context\ retrieval "$config_root/commands" "$config_root/skills"
    assert_unrelated_config
    printf '%s\n' "OPENCODE_NEGATIVE_OK scenario=preplan-default"
    ;;
  *) exit 2 ;;
esac
