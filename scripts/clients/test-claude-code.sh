#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
package_root="$repo_root/clients/claude-code"
marketplace_root="$package_root/marketplace"
scenario=""

while (($#)); do
  case "$1" in
    --scenario) scenario="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done

[[ -n "$scenario" ]] || exit 2
command -v claude >/dev/null

temporary_root="$(mktemp -d)"
cleanup() { rm -rf "$temporary_root"; }
trap cleanup EXIT
home="$temporary_root/home"
mkdir -p "$home"

run_claude() {
  HOME="$home" claude "$@"
}

expect_rejection() {
  if "$@" >/dev/null 2>&1; then
    printf '%s\n' 'expected rejection did not occur' >&2
    exit 1
  fi
}

assert_package_contract() {
  local root="$1"
  grep -Fqx '      "command": "acr-mcp",' "$root/plugins/context-fabric/.mcp.json" || return 1
  grep -Fqx '      "args": ["serve"]' "$root/plugins/context-fabric/.mcp.json" || return 1
  grep -Fq 'context_for_task' "$root/plugins/context-fabric/commands/get-context.md" || return 1
  grep -Fq 'source_evidence' "$root/plugins/context-fabric/commands/get-context.md" || return 1
  grep -Fq 'Run only when the user explicitly asks' "$root/plugins/context-fabric/commands/plan-with-context.md" || return 1
  grep -Fq 'untrusted data' "$root/plugins/context-fabric/skills/context-fabric/SKILL.md" || return 1
  if grep -R -Eqi 'ACR_[A-Z_]*(TOKEN|SECRET)|bearer[[:space:]]+[A-Za-z0-9._-]+' "$root/plugins/context-fabric"; then return 1; fi
  if grep -R -Eqi '^[[:space:]]*Retrieve context automatically|preplan_enabled_by_default|writeback_enabled_by_default' "$root/plugins/context-fabric"; then return 1; fi
}

case "$scenario" in
  lifecycle)
    install_source="$temporary_root/marketplace"
    cp -R "$marketplace_root" "$install_source"
    claude plugin validate --strict "$install_source"
    assert_package_contract "$marketplace_root"
    run_claude plugin marketplace add --scope user "$install_source"
    run_claude plugin list --available --json | grep -Fq 'context-fabric'
    run_claude plugin install --scope user context-fabric@context-fabric
    run_claude plugin list --json | grep -Fq 'context-fabric@context-fabric'
    run_claude plugin list --json | grep -Fq '"version": "1.0.0"'
    run_claude plugin details context-fabric@context-fabric | grep -Fq 'MCP servers (1)  acr'
    perl -0pi -e 's/"version": "1\.0\.0"/"version": "1.0.1"/' "$install_source/.claude-plugin/marketplace.json"
    perl -0pi -e 's/"version": "1\.0\.0"/"version": "1.0.1"/; s/local MCP sidecar/local MCP sidecar update-fixture/' "$install_source/plugins/context-fabric/.claude-plugin/plugin.json"
    run_claude plugin marketplace update context-fabric
    run_claude plugin update --scope user context-fabric@context-fabric
    run_claude plugin list --json | grep -Fq '"version": "1.0.1"'
    run_claude plugin details context-fabric@context-fabric | grep -Fq 'update-fixture'
    run_claude plugin uninstall --scope user context-fabric@context-fabric
    ! run_claude plugin list --json | grep -Fq 'context-fabric@context-fabric'
    run_claude plugin marketplace remove --scope user context-fabric
    ! run_claude plugin marketplace list | grep -Fq 'context-fabric'
    if grep -R -Fq --exclude-dir=cache 'context-fabric' "$home/.claude"; then exit 1; fi
    test -d "$home/.claude/plugins/cache/context-fabric/context-fabric/1.0.1"
    printf '%s\n' 'CLAUDE_CODE_LIFECYCLE_OK marketplace=passed install=passed mcp=loaded native_update=1.0.0-to-1.0.1 active_state=removed native_orphan_cache=retained model_credentials=not-required'
    ;;
  bare-acr-mcp)
    copy="$temporary_root/marketplace"
    cp -R "$marketplace_root" "$copy"
    perl -0pi -e 's/"args": \["serve"\]/"args": []/' "$copy/plugins/context-fabric/.mcp.json"
    expect_rejection assert_package_contract "$copy"
    printf '%s\n' 'CLAUDE_CODE_NEGATIVE_OK scenario=bare-acr-mcp'
    ;;
  credential-instruction)
    copy="$temporary_root/marketplace"
    cp -R "$marketplace_root" "$copy"
    printf '%s\n' 'Bearer example-value' >>"$copy/plugins/context-fabric/commands/get-context.md"
    expect_rejection assert_package_contract "$copy"
    printf '%s\n' 'CLAUDE_CODE_NEGATIVE_OK scenario=credential-instruction'
    ;;
  implicit-preplan)
    copy="$temporary_root/marketplace"
    cp -R "$marketplace_root" "$copy"
    printf '%s\n' 'Retrieve context automatically before every plan.' >>"$copy/plugins/context-fabric/commands/plan-with-context.md"
    expect_rejection assert_package_contract "$copy"
    printf '%s\n' 'CLAUDE_CODE_NEGATIVE_OK scenario=implicit-preplan'
    ;;
  *) exit 2 ;;
esac
