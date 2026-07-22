#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
package_path=""
while (($#)); do
  case "$1" in
    --package) package_path="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
[[ -n "$package_path" ]] || exit 2
if [[ "$package_path" = /* ]]; then package_root="$package_path"; else package_root="$repo_root/$package_path"; fi

require_wrapper_contract() {
  local root="$1"
  local install="$root/scripts/install.ps1"
  local update="$root/scripts/update.ps1"
  local uninstall="$root/scripts/uninstall.ps1"
  for file in "$install" "$update" "$uninstall"; do
    grep -Fq 'OPENCODE_CONFIG_DIR' "$file" || return 1
    grep -Fq '.context-fabric-owner.v1' "$file" || return 1
    grep -Fq 'context-fabric-opencode.v1' "$file" || return 1
  done
  grep -Fq 'SymbolicLink' "$install" || return 1
  grep -Fq 'refusing to install into a non-empty target' "$install" || return 1
  grep -Fq 'New-Item -ItemType Directory' "$install" || return 1
  grep -Fq 'Get-OwnedStage' "$update" || return 1
  grep -Fq 'refusing to update a target not owned by Context Fabric' "$update" || return 1
  grep -Fq '[IO.File]::Move' "$update" || return 1
  grep -Fq 'New-Item -ItemType SymbolicLink' "$update" || return 1
  grep -Fq 'Get-OwnedStage' "$uninstall" || return 1
  grep -Fq 'refusing to remove a target not owned by Context Fabric' "$uninstall" || return 1
  local cleanup="$root/config/lib/doctor-process-cleanup.ts"
  grep -Fq 'taskkill.exe' "$cleanup" || return 1
  grep -Fq '["/pid", String(child.pid), "/t", "/f"]' "$cleanup" || return 1
  grep -Fq 'runOfflineDoctor' "$root/config/plugins/context-fabric.ts" || return 1
}

assert_mutation_rejected() {
  local file="$1" needle="$2" copy
  copy="$(mktemp -d)"
  trap 'rm -rf "$copy"' RETURN
  cp -R "$package_root/." "$copy/"
  case "$needle" in
    OPENCODE_CONFIG_DIR) perl -0pi -e 's/OPENCODE_CONFIG_DIR/REMOVED/g' "$copy/scripts/$file" ;;
    Get-OwnedStage) perl -0pi -e 's/Get-OwnedStage/REMOVED/g' "$copy/scripts/$file" ;;
    '[IO.File]::Move') perl -0pi -e 's/Move/REMOVED/g' "$copy/scripts/$file" ;;
    'New-Item -ItemType SymbolicLink') perl -0pi -e 's/SymbolicLink/REMOVED/g' "$copy/scripts/$file" ;;
    'refusing to install into a non-empty target') perl -0pi -e 's/non-empty target/REMOVED/g' "$copy/scripts/$file" ;;
    *) exit 2 ;;
  esac
  if require_wrapper_contract "$copy"; then
    printf '%s\n' "mutation unexpectedly passed: $needle" >&2
    exit 1
  fi
  rm -rf "$copy"
  trap - RETURN
}

require_wrapper_contract "$package_root"
assert_mutation_rejected 'install.ps1' OPENCODE_CONFIG_DIR
assert_mutation_rejected 'install.ps1' 'refusing to install into a non-empty target'
assert_mutation_rejected 'update.ps1' Get-OwnedStage
assert_mutation_rejected 'update.ps1' '[IO.File]::Move'
assert_mutation_rejected 'update.ps1' 'New-Item -ItemType SymbolicLink'
assert_mutation_rejected 'uninstall.ps1' Get-OwnedStage
printf '%s\n' 'OPENCODE_POWERSHELL_STATIC_OK mutation_proofs=passed'
