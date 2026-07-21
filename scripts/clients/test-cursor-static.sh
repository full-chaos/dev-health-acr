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

fail() { printf 'CURSOR_POWERSHELL_FAIL reason=%s\n' "$1" >&2; exit 1; }

# --- Structural token parity: fast, portable, no pwsh required. The target
# is a stable, real, owned directory -- never a symlink or junction -- so
# there is no directory-cutover primitive to check here; the checks below
# cover legacy-link fail-closed detection, atomic per-file replacement via
# the standard, cross-platform File.Move(overwrite) overload, and the
# marker-written-last ordering. ---
require_wrapper_contract() {
  local root="$1"
  local install="$root/scripts/install.ps1"
  local update="$root/scripts/update.ps1"
  local uninstall="$root/scripts/uninstall.ps1"
  for file in "$install" "$update" "$uninstall"; do
    grep -Fq 'CURSOR_PLUGIN_DIR' "$file" || return 1
    grep -Fq '.context-fabric-owner.v1' "$file" || return 1
    grep -Fq 'context-fabric-cursor.v1' "$file" || return 1
    grep -Fq '$existing.LinkType' "$file" || return 1
    grep -Fq 'legacy symlink or junction' "$file" || return 1
  done
  grep -Fq 'refusing to install into a non-empty target' "$install" || return 1
  grep -Fq '[System.IO.File]::Move($tmp, $Destination, $true)' "$install" || return 1
  grep -Fq '[System.IO.File]::Move($markerTmp, (Join-Path $configRoot $markerFile), $true)' "$install" || return 1
  grep -Fq 'New-Item -ItemType Directory -Force' "$install" || return 1

  grep -Fq 'Test-OwnedDirectory' "$update" || return 1
  grep -Fq 'refusing to update a target not owned by Context Fabric' "$update" || return 1
  grep -Fq '[System.IO.File]::Move($tmp, $Destination, $true)' "$update" || return 1
  grep -Fq '[System.IO.File]::Move($markerTmp, (Join-Path $configRoot $markerFile), $true)' "$update" || return 1
  # The marker is replaced last: its write must appear strictly after the
  # per-file replacement loop in source order.
  local payload_line marker_line
  payload_line="$(grep -n 'foreach (\$rel in \$payloadFiles)' "$update" | tail -1 | cut -d: -f1)"
  marker_line="$(grep -n '\$markerTmp = Join-Path \$configRoot' "$update" | tail -1 | cut -d: -f1)"
  [[ -n "$payload_line" && -n "$marker_line" ]] || return 1
  (( marker_line > payload_line )) || return 1

  grep -Fq 'Test-OwnedDirectory' "$uninstall" || return 1
  grep -Fq 'refusing to remove a target not owned by Context Fabric' "$uninstall" || return 1
}

assert_mutation_rejected() {
  local file="$1" needle="$2" copy
  copy="$(mktemp -d)"
  trap 'rm -rf "$copy"' RETURN
  cp -R "$package_root/." "$copy/"
  case "$needle" in
    CURSOR_PLUGIN_DIR) perl -0pi -e 's/CURSOR_PLUGIN_DIR/REMOVED/g' "$copy/scripts/$file" ;;
    legacy-link-detection) perl -0pi -e 's/\$existing\.LinkType/REMOVED/g' "$copy/scripts/$file" ;;
    'refusing to install into a non-empty target') perl -0pi -e 's/non-empty target/REMOVED/g' "$copy/scripts/$file" ;;
    atomic-write-overwrite-flag) perl -0pi -e 's/::Move\(\$tmp, \$Destination, \$true\)/::Move(\$tmp, \$Destination, \$false)/' "$copy/scripts/$file" ;;
    marker-write-overwrite-flag) perl -0pi -e 's/::Move\(\$markerTmp, \(Join-Path \$configRoot \$markerFile\), \$true\)/::Move(\$markerTmp, (Join-Path \$configRoot \$markerFile), \$false)/' "$copy/scripts/$file" ;;
    Test-OwnedDirectory) perl -0pi -e 's/Test-OwnedDirectory/REMOVED/g' "$copy/scripts/$file" ;;
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
assert_mutation_rejected 'install.ps1' CURSOR_PLUGIN_DIR
assert_mutation_rejected 'install.ps1' legacy-link-detection
assert_mutation_rejected 'install.ps1' 'refusing to install into a non-empty target'
assert_mutation_rejected 'install.ps1' atomic-write-overwrite-flag
assert_mutation_rejected 'update.ps1' Test-OwnedDirectory
assert_mutation_rejected 'update.ps1' atomic-write-overwrite-flag
assert_mutation_rejected 'update.ps1' marker-write-overwrite-flag
assert_mutation_rejected 'uninstall.ps1' Test-OwnedDirectory
assert_mutation_rejected 'uninstall.ps1' legacy-link-detection

if ! command -v pwsh >/dev/null 2>&1; then
  printf '%s\n' 'CURSOR_POWERSHELL_STATIC_OK mutation_proofs=passed semantic_proof=skipped_pwsh_not_installed'
  exit 0
fi

# --- Real pwsh execution (never on Windows in this environment -- see the
# static Windows-only note below for what that leaves unverified): full
# lifecycle, legacy-link fail-closed, and mid-update-failure/retry, all
# exercised against the actual shipped scripts. ---
work="$(mktemp -d)"
cleanup() { chmod -R u+w "$work" 2>/dev/null || true; rm -rf "$work"; }
trap cleanup EXIT

pwsh_install() { HOME="$1" CURSOR_PLUGIN_DIR="$2" pwsh -NoProfile -File "$3/scripts/install.ps1"; }
pwsh_update()  { HOME="$1" CURSOR_PLUGIN_DIR="$2" pwsh -NoProfile -File "$3/scripts/update.ps1"; }
pwsh_uninstall() { HOME="$1" CURSOR_PLUGIN_DIR="$2" pwsh -NoProfile -File "$3/scripts/uninstall.ps1"; }

lifecycle_home="$work/lifecycle-home"
lifecycle_config="$lifecycle_home/.cursor/plugins/local/context-fabric"
mkdir -p "$lifecycle_home"
pwsh_install "$lifecycle_home" "$lifecycle_config" "$package_root" >/dev/null || fail "real_install_execution"
[[ -d "$lifecycle_config" && ! -L "$lifecycle_config" ]] || fail "real_install_not_a_real_directory"
[[ -f "$lifecycle_config/mcp.json" ]] || fail "real_install_missing_mcp_json"
pwsh_update "$lifecycle_home" "$lifecycle_config" "$package_root" >/dev/null || fail "real_update_execution"
pwsh_update "$lifecycle_home" "$lifecycle_config" "$package_root" >/dev/null || fail "real_update_execution_repeat"
[[ -f "$lifecycle_config/mcp.json" ]] || fail "real_update_missing_mcp_json"
pwsh_uninstall "$lifecycle_home" "$lifecycle_config" "$package_root" >/dev/null || fail "real_uninstall_execution"
[[ ! -e "$lifecycle_config" ]] || fail "real_uninstall_left_config_root"

# Legacy symlink target: every operation must fail closed and leave the
# link and its target untouched.
legacy_real="$work/legacy-real"
legacy_target="$work/legacy-target"
mkdir -p "$legacy_real"
printf 'legacy-content' >"$legacy_real/f"
ln -s "$(basename "$legacy_real")" "$legacy_target"
if pwsh_install "$work" "$legacy_target" "$package_root" >/dev/null 2>&1; then fail "legacy_link_install_should_refuse"; fi
if pwsh_update "$work" "$legacy_target" "$package_root" >/dev/null 2>&1; then fail "legacy_link_update_should_refuse"; fi
if pwsh_uninstall "$work" "$legacy_target" "$package_root" >/dev/null 2>&1; then fail "legacy_link_uninstall_should_refuse"; fi
[[ -L "$legacy_target" ]] || fail "legacy_link_was_removed"
[[ -f "$legacy_real/f" ]] || fail "legacy_link_target_was_removed"

# Mid-update failure and retry convergence: make the last-replaced
# directory read-only, confirm the update fails but nothing required goes
# missing and the marker is untouched, then confirm a retry converges.
mid_home="$work/mid-home"
mid_config="$mid_home/.cursor/plugins/local/context-fabric"
mkdir -p "$mid_home"
pwsh_install "$mid_home" "$mid_config" "$package_root" >/dev/null || fail "mid_update_install_execution"
marker_before="$(cat "$mid_config/.context-fabric-owner.v1")"
chmod a-w "$mid_config/skills/context-fabric"
if pwsh_update "$mid_home" "$mid_config" "$package_root" >/dev/null 2>&1; then
  chmod u+w "$mid_config/skills/context-fabric"
  fail "mid_update_should_have_failed"
fi
[[ -f "$mid_config/mcp.json" ]] || { chmod u+w "$mid_config/skills/context-fabric"; fail "mid_update_missing_mcp_json"; }
[[ -f "$mid_config/skills/context-fabric/SKILL.md" ]] || { chmod u+w "$mid_config/skills/context-fabric"; fail "mid_update_missing_skill_file"; }
if [[ "$(cat "$mid_config/.context-fabric-owner.v1")" != "$marker_before" ]]; then
  chmod u+w "$mid_config/skills/context-fabric"
  fail "mid_update_marker_rewritten_despite_failure"
fi
chmod u+w "$mid_config/skills/context-fabric"
pwsh_update "$mid_home" "$mid_config" "$package_root" >/dev/null || fail "mid_update_retry_execution"
if ! diff -q "$package_root/skills/context-fabric/SKILL.md" "$mid_config/skills/context-fabric/SKILL.md" >/dev/null; then
  fail "mid_update_retry_did_not_converge"
fi
pwsh_uninstall "$mid_home" "$mid_config" "$package_root" >/dev/null || fail "mid_update_cleanup_uninstall_execution"

# Windows-only note: this process never runs on Windows here, so NTFS
# junction detection and File.Move(overwrite=true) behavior against a file
# another process holds open are not live-verified -- only the structural
# checks above (LinkType-based detection, which covers both SymbolicLink
# and Junction generically; the standard, documented File.Move(overwrite)
# overload) run. No claim of live Windows verification is made.
printf '%s\n' 'CURSOR_POWERSHELL_STATIC_OK mutation_proofs=passed real_execution=passed windows_live_verification=not_claimed'
