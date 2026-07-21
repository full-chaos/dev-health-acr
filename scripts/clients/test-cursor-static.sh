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

# --- Structural token parity: fast, portable, no pwsh required. Covers the
# Windows-only MoveFileEx branch statically, since it cannot be executed on
# this (or most CI) machines -- the non-Windows rename() branch gets real
# execution proof further down instead. ---
require_wrapper_contract() {
  local root="$1"
  local install="$root/scripts/install.ps1"
  local update="$root/scripts/update.ps1"
  local uninstall="$root/scripts/uninstall.ps1"
  for file in "$install" "$update" "$uninstall"; do
    grep -Fq 'CURSOR_PLUGIN_DIR' "$file" || return 1
    grep -Fq '.context-fabric-owner.v1' "$file" || return 1
    grep -Fq 'context-fabric-cursor.v1' "$file" || return 1
  done
  grep -Fq 'SymbolicLink' "$install" || return 1
  grep -Fq 'refusing to install into a non-empty target' "$install" || return 1
  grep -Fq 'New-Item -ItemType Directory' "$install" || return 1
  grep -Fq 'Get-OwnedStage' "$update" || return 1
  grep -Fq 'refusing to update a target not owned by Context Fabric' "$update" || return 1
  grep -Fq 'New-Item -ItemType SymbolicLink' "$update" || return 1
  # Windows branch: a single, bounded, checked MoveFileEx replace -- no
  # copy-fallback flag, no delayed-reboot flag, and the return value is
  # checked with a thrown, non-swallowed error on failure.
  grep -Fq 'kernel32.dll' "$update" || return 1
  grep -Fq 'MoveFileEx(' "$update" || return 1
  grep -Fq '$moveFileReplaceExisting = 0x1' "$update" || return 1
  grep -Fq 'if (-not $ok)' "$update" || return 1
  grep -Fq 'GetLastWin32Error()' "$update" || return 1
  # Non-Windows branch: POSIX rename(2), also checked.
  grep -Fq 'DllImport("libc"' "$update" || return 1
  grep -Fq 'rename(' "$update" || return 1
  grep -Fq 'if ($result -ne 0)' "$update" || return 1
  grep -Fq 'Get-OwnedStage' "$uninstall" || return 1
  grep -Fq 'refusing to remove a target not owned by Context Fabric' "$uninstall" || return 1
}

assert_mutation_rejected() {
  local file="$1" needle="$2" copy
  copy="$(mktemp -d)"
  trap 'rm -rf "$copy"' RETURN
  cp -R "$package_root/." "$copy/"
  case "$needle" in
    CURSOR_PLUGIN_DIR) perl -0pi -e 's/CURSOR_PLUGIN_DIR/REMOVED/g' "$copy/scripts/$file" ;;
    Get-OwnedStage) perl -0pi -e 's/Get-OwnedStage/REMOVED/g' "$copy/scripts/$file" ;;
    'New-Item -ItemType SymbolicLink') perl -0pi -e 's/SymbolicLink/REMOVED/g' "$copy/scripts/$file" ;;
    'refusing to install into a non-empty target') perl -0pi -e 's/non-empty target/REMOVED/g' "$copy/scripts/$file" ;;
    windows-checked-error) perl -0pi -e 's/if \(-not \$ok\) \{[^}]*\}/REMOVED/s' "$copy/scripts/$file" ;;
    windows-replace-flag) perl -0pi -e 's/\$moveFileReplaceExisting = 0x1/REMOVED/' "$copy/scripts/$file" ;;
    windows-movefileex-call) perl -0pi -e 's/MoveFileEx\(/REMOVED(/g' "$copy/scripts/$file" ;;
    posix-checked-error) perl -0pi -e 's/if \(\$result -ne 0\) \{[^}]*\}/REMOVED/s' "$copy/scripts/$file" ;;
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
assert_mutation_rejected 'install.ps1' 'refusing to install into a non-empty target'
assert_mutation_rejected 'update.ps1' Get-OwnedStage
assert_mutation_rejected 'update.ps1' 'New-Item -ItemType SymbolicLink'
assert_mutation_rejected 'update.ps1' windows-checked-error
assert_mutation_rejected 'update.ps1' windows-replace-flag
assert_mutation_rejected 'update.ps1' windows-movefileex-call
assert_mutation_rejected 'update.ps1' posix-checked-error
assert_mutation_rejected 'uninstall.ps1' Get-OwnedStage

if ! command -v pwsh >/dev/null 2>&1; then
  printf '%s\n' 'CURSOR_POWERSHELL_STATIC_OK mutation_proofs=passed semantic_proof=skipped_pwsh_not_installed'
  exit 0
fi

# --- Real pwsh execution: functional lifecycle + behavioral mutation
# proofs, clearly separated from the Windows-only static proof above
# because this process itself is never running on Windows here (macOS/
# Linux pwsh exercises the POSIX rename() branch of Invoke-AtomicReplace
# for real; the MoveFileEx branch is covered only by the static checks). ---
work="$(mktemp -d)"
cleanup() { chmod -R u+w "$work" 2>/dev/null || true; rm -rf "$work"; }
trap cleanup EXIT

pwsh_install() { HOME="$1" CURSOR_PLUGIN_DIR="$2" pwsh -NoProfile -File "$3/scripts/install.ps1"; }
pwsh_update()  { HOME="$1" CURSOR_PLUGIN_DIR="$2" pwsh -NoProfile -File "$3/scripts/update.ps1"; }
pwsh_uninstall() { HOME="$1" CURSOR_PLUGIN_DIR="$2" pwsh -NoProfile -File "$3/scripts/uninstall.ps1"; }

# 1. Real functional lifecycle against the shipped scripts, including
#    retention across several updates for a reader that resolved once.
lifecycle_home="$work/lifecycle-home"
lifecycle_config="$lifecycle_home/.cursor/plugins/local/context-fabric"
mkdir -p "$lifecycle_home"
pwsh_install "$lifecycle_home" "$lifecycle_config" "$package_root" >/dev/null || fail "real_install_execution"
[[ -f "$lifecycle_config/mcp.json" ]] || fail "real_install_missing_mcp_json"
original_resolved="$(dirname "$lifecycle_config")/$(readlink "$lifecycle_config")"
for _ in 1 2 3; do
  pwsh_update "$lifecycle_home" "$lifecycle_config" "$package_root" >/dev/null || fail "real_update_execution"
done
[[ -f "$lifecycle_config/mcp.json" ]] || fail "real_update_missing_mcp_json"
[[ -f "$original_resolved/mcp.json" ]] || fail "real_update_pruned_a_retained_generation"
pwsh_uninstall "$lifecycle_home" "$lifecycle_config" "$package_root" >/dev/null || fail "real_uninstall_execution"
[[ ! -e "$lifecycle_config" ]] || fail "real_uninstall_left_config_root"
[[ ! -e "$original_resolved" ]] || fail "real_uninstall_left_a_retained_generation"

setup_forged_target() {
  local config="$1" stages
  mkdir -p "$(dirname "$config")"
  stages="${config}.stages"
  rm -rf "$config" "$stages"
  mkdir -p "$stages/forged"
  printf 'not-context-fabric' >"$stages/forged/.context-fabric-owner.v1"
  ln -s "$(basename "$config").stages/forged" "$config"
}

# 2. Real rejection of a forged (wrong-owner) target with the shipped scripts.
forged_home="$work/forged-home"
forged_config="$forged_home/.cursor/plugins/local/context-fabric"
setup_forged_target "$forged_config"
if pwsh_update "$forged_home" "$forged_config" "$package_root" >/dev/null 2>&1; then
  fail "baseline_update_should_reject_forged_owner"
fi

# 3. Semantic mutation A: invert install.ps1's success/failure cleanup guard.
#    Correct: cleanup only when NOT installed. Mutant: cleanup only when
#    installed -- destroys the just-created stage on a "successful" run,
#    leaving configRoot a dangling symlink. A token grep for `$installed`
#    would still find the variable; only real execution proves the bug.
mutantA="$work/mutant-install"
cp -R "$package_root" "$mutantA"
perl -0pi -e 's/if \(-not \$installed -and/if (\$installed -and/' "$mutantA/scripts/install.ps1"
grep -Fq 'if ($installed -and' "$mutantA/scripts/install.ps1" || fail "mutation_a_did_not_apply"
mutA_home="$work/mutA-home"
mutA_config="$mutA_home/.cursor/plugins/local/context-fabric"
mkdir -p "$mutA_home"
pwsh_install "$mutA_home" "$mutA_config" "$mutantA" >/dev/null 2>&1 || true
if [[ -f "$mutA_config/mcp.json" ]]; then
  fail "mutation_a_not_detected_installed_guard_inversion"
fi

# 4. Semantic mutation B: invert update.ps1's owner-marker comparison.
#    Correct: reject when the marker does NOT match. Mutant: reject when
#    it DOES match -- a forged target with the WRONG marker is wrongly
#    accepted. Proven only by actually attempting the operation.
mutantB="$work/mutant-update"
cp -R "$package_root" "$mutantB"
perl -0pi -e 's/-ne \$ownerValue\) \{ return \$null \}/-eq \$ownerValue) { return \$null }/' "$mutantB/scripts/update.ps1"
grep -Fq '-eq $ownerValue) { return $null }' "$mutantB/scripts/update.ps1" || fail "mutation_b_did_not_apply"
mutB_home="$work/mutB-home"
mutB_config="$mutB_home/.cursor/plugins/local/context-fabric"
setup_forged_target "$mutB_config"
if pwsh_update "$mutB_home" "$mutB_config" "$mutantB" >/dev/null 2>&1; then
  : # mutation reproduced a real bypass -- detected
else
  fail "mutation_b_not_detected_forged_owner_still_rejected"
fi

# 5. Semantic mutation C: same owner-marker inversion in uninstall.ps1.
mutantC="$work/mutant-uninstall"
cp -R "$package_root" "$mutantC"
perl -0pi -e 's/-ne \$ownerValue\) \{ return \$null \}/-eq \$ownerValue) { return \$null }/' "$mutantC/scripts/uninstall.ps1"
grep -Fq '-eq $ownerValue) { return $null }' "$mutantC/scripts/uninstall.ps1" || fail "mutation_c_did_not_apply"
mutC_home="$work/mutC-home"
mutC_config="$mutC_home/.cursor/plugins/local/context-fabric"
setup_forged_target "$mutC_config"
if pwsh_uninstall "$mutC_home" "$mutC_config" "$mutantC" >/dev/null 2>&1; then
  : # mutation reproduced a real bypass -- detected
else
  fail "mutation_c_not_detected_forged_owner_still_rejected"
fi

printf '%s\n' 'CURSOR_POWERSHELL_STATIC_OK mutation_proofs=passed windows_branch_static=passed semantic_proof=passed real_execution=passed'
