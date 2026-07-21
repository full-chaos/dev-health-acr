#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
bash_dir="$(dirname "$BASH")"
package_path=""
scenario=""
while (($#)); do
  case "$1" in
    --package) package_path="$2"; shift 2 ;;
    --scenario) scenario="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[[ -n "$package_path" && -n "$scenario" && "$package_path" = /* ]] || exit 2
package_root="$package_path"
[[ -d "$package_root" ]] || exit 2

temporary_root="$(mktemp -d)"
cleanup() { chmod -R u+w "$temporary_root" 2>/dev/null || true; rm -rf "$temporary_root"; }
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
write_receipt() {
  local path="$1" value="$2" temporary="${1}.$$.tmp"
  printf '%s\n' "$value" >"$temporary"
  mv "$temporary" "$path"
}
case "${ACR_MCP_TEST_MODE:-healthy}" in
  healthy) printf '%s\n' 'offline doctor: healthy' ;;
  timeout) sleep 6 ;;
  overflow) yes x | head -c 70000 ;;
  descendant) write_receipt "${ACR_MCP_DOCTOR_RECEIPT:?}" "$$"; sleep 30 & write_receipt "${ACR_MCP_DESCENDANT_PID:?}" "$!"; wait ;;
  descendant-missing-receipt) sleep 0.2 ;;
  descendant-malformed-receipt) write_receipt "${ACR_MCP_DOCTOR_RECEIPT:?}" 'not-a-pid'; sleep 0.2 ;;
  windows-doctor) write_receipt "${ACR_MCP_DOCTOR_RECEIPT:?}" "$$"; exec sleep 30 ;;
  nonzero) exit 12 ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$fake_bin/acr-mcp"

run_wrapper() {
  env -i HOME="$home" XDG_CONFIG_HOME="$temporary_root/xdg" CLAUDE_CONFIG_DIR="$temporary_root/claude" CODEX_HOME="$temporary_root/codex" CODEX_SQLITE_HOME="$temporary_root/codex-sqlite" OPENCODE_CONFIG_DIR="$config_root" ACR_NATIVE_DUMMY_TOKEN=not-a-secret PATH="$fake_bin:$bash_dir:/usr/bin:/bin" "$package_root/scripts/$1"
}

expect_rejection() {
  if "$@" >/dev/null 2>&1; then
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
if (Number.isFinite(abortAfterMs) && abortAfterMs > 0) {
  setTimeout(() => controller.abort(), abortAfterMs)
}
const execution = status.execute({}, {
  directory: process.cwd(),
  worktree: process.cwd(),
  abort: controller.signal,
  metadata() {},
  ask: async () => {},
})
if (process.env.ACR_MCP_ABORT_AFTER_RECEIPT === "1") {
  const receipt = process.env.ACR_MCP_ABORT_RECEIPT
  if (receipt === undefined) throw new Error("ACR_MCP_ABORT_RECEIPT is required")
  const deadline = Date.now() + 1000
  let pid: string | undefined
  while (Date.now() < deadline) {
    try {
      const candidate = await Bun.file(receipt).text()
      if (!/^[1-9][0-9]*\n$/.test(candidate)) throw new Error("doctor receipt is malformed before abort")
      pid = candidate.trim()
      break
    } catch (error) {
      if (error instanceof Error && error.message === "doctor receipt is malformed before abort") throw error
      await Bun.sleep(10)
    }
  }
  if (pid === undefined) throw new Error("doctor receipt was not written before abort")
  controller.abort()
}
const result = await execution
process.stdout.write(typeof result === "string" ? result : result.output)
EOF
}

run_status_harness() {
  write_plugin_harness
  env ACR_MCP_TEST_MODE="$1" ACR_MCP_ABORT_AFTER_MS="${2:-0}" ACR_MCP_ABORT_BEFORE="${3:-0}" ACR_MCP_DESCENDANT_PID="${4:-}" ACR_MCP_DOCTOR_RECEIPT="${5:-}" ACR_MCP_ABORT_AFTER_RECEIPT="${6:-}" ACR_MCP_ABORT_RECEIPT="${7:-}" PATH="$fake_bin:$PATH" bun run "$config_root/plugin-harness.ts"
}

assert_descendant_stopped() {
  local receipt="$1" pid
  [[ -s "$receipt" ]] || return 1
  IFS= read -r pid <"$receipt"
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  local deadline=$(( $(date +%s) + 2 ))
  while kill -0 "$pid" 2>/dev/null; do
    (( $(date +%s) < deadline )) || return 1
    sleep 0.05
  done
}

run_descendant_harness() {
  local mode="$1" receipt="$2" doctor_receipt="$3"
  local abort_receipt="$receipt"
  if [[ "$mode" == descendant-malformed-receipt ]]; then abort_receipt="$doctor_receipt"; fi
  export ACR_MCP_DESCENDANT_PID="$receipt"
  export ACR_MCP_DOCTOR_RECEIPT="$doctor_receipt"
  run_status_harness "$mode" 0 0 "$receipt" "$doctor_receipt" 1 "$abort_receipt"
  unset ACR_MCP_DESCENDANT_PID
  unset ACR_MCP_DOCTOR_RECEIPT
}

run_descendant_and_assert() {
  local mode="$1" receipt="$2" doctor_receipt="$3" output
  output="$(run_descendant_harness "$mode" "$receipt" "$doctor_receipt")"
  [[ "$output" == 'context_fabric_status failed: cancelled' ]]
  assert_descendant_stopped "$receipt"
  assert_descendant_stopped "$doctor_receipt"
}

write_taskkill_fixture() {
  local mode="$1"
  cat >"$fake_bin/taskkill.exe" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
arguments="$*"
pid=""
while (($#)); do
  case "$1" in
    /pid) pid="$2"; shift 2 ;;
    /t|/f) shift ;;
    *) exit 2 ;;
  esac
done
case "${ACR_MCP_TASKKILL_MODE:?}" in
  success) [[ "$arguments" == "/pid $pid /t /f" ]]; kill -TERM "$pid" ;;
  nonzero) exit 17 ;;
  hang) temporary="${ACR_MCP_TASKKILL_RECEIPT:?}.$$.tmp"; printf '%s\n' "$$" >"$temporary"; mv "$temporary" "${ACR_MCP_TASKKILL_RECEIPT:?}"; exec sleep 30 ;;
  *) exit 2 ;;
esac
EOF
  if [[ "$mode" == error ]]; then chmod a-x "$fake_bin/taskkill.exe"; else chmod +x "$fake_bin/taskkill.exe"; fi
}

run_windows_cleanup_case() {
  local mode="$1" expected="$2" output started elapsed taskkill_receipt=""
  local receipt="$temporary_root/windows-$mode.pid"
  if [[ "$mode" == hang ]]; then taskkill_receipt="$temporary_root/taskkill-$mode.pid"; fi
  write_taskkill_fixture "$mode"
  started=$SECONDS
  output="$(env ACR_MCP_TEST_MODE=windows-doctor ACR_MCP_DOCTOR_RECEIPT="$receipt" ACR_MCP_EXPECTED_OUTCOME="$expected" ACR_MCP_TASKKILL_MODE="$mode" ACR_MCP_TASKKILL_RECEIPT="$taskkill_receipt" PATH="$fake_bin:$PATH" bun run "$package_root/doctor-process-driver.ts")"
  elapsed=$(( SECONDS - started ))
  [[ "$output" == "DOCTOR_PROCESS_DRIVER_OK outcome=$expected" ]]
  (( elapsed < 3 ))
  if [[ -n "$taskkill_receipt" ]]; then assert_descendant_stopped "$taskkill_receipt"; fi
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
    descendant_receipt="$temporary_root/descendant.pid"
    doctor_receipt="$temporary_root/doctor.pid"
    run_descendant_and_assert descendant "$descendant_receipt" "$doctor_receipt"
    expect_rejection run_descendant_and_assert descendant-missing-receipt "$temporary_root/missing.pid" "$temporary_root/missing-doctor.pid"
    expect_rejection run_descendant_and_assert descendant-malformed-receipt "$temporary_root/malformed.pid" "$temporary_root/malformed-doctor.pid"
    run_windows_cleanup_case success aborted
    run_windows_cleanup_case error cleanup_failed
    run_windows_cleanup_case nonzero cleanup_failed
    run_windows_cleanup_case hang cleanup_failed
    bash "$repo_root/scripts/clients/test-opencode-static.sh" --package "$package_root"
    HOME="$home" OPENCODE_CONFIG="$config_root/opencode.json" PATH="$fake_bin:$PATH" opencode mcp list >/dev/null
    old_stage="$temporary_root/$(readlink "$config_root")"
    chmod -R a-w "$old_stage"
    expect_rejection run_wrapper update.sh
    [[ -L "$config_root" && -f "$config_root/opencode.json" ]]
    chmod -R u+w "$old_stage"
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
  oracle-regressions)
    run_wrapper install.sh
    descendant_receipt="$temporary_root/descendant.pid"
    doctor_receipt="$temporary_root/doctor.pid"
    run_descendant_and_assert descendant "$descendant_receipt" "$doctor_receipt"
    expect_rejection run_descendant_and_assert descendant-missing-receipt "$temporary_root/missing.pid" "$temporary_root/missing-doctor.pid"
    expect_rejection run_descendant_and_assert descendant-malformed-receipt "$temporary_root/malformed.pid" "$temporary_root/malformed-doctor.pid"
    run_windows_cleanup_case success aborted
    run_windows_cleanup_case error cleanup_failed
    run_windows_cleanup_case nonzero cleanup_failed
    run_windows_cleanup_case hang cleanup_failed
    old_stage="$temporary_root/$(readlink "$config_root")"
    chmod -R a-w "$old_stage"
    expect_rejection run_wrapper update.sh
    [[ -L "$config_root" && -f "$config_root/opencode.json" ]]
    chmod -R u+w "$old_stage"
    bash "$repo_root/scripts/clients/test-opencode-static.sh" --package "$package_root"
    printf '%s\n' 'OPENCODE_REGRESSIONS_OK immutable_stage=preserved receipt_driven_abort=passed malformed_receipt=rejected windows_cleanup_error_nonzero_hang=passed'
    ;;
  *) exit 2 ;;
esac
