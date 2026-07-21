#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
marker_file=".context-fabric-owner.v1"
marker_prefix="context-fabric-cursor.v1"

payload_files=(
  ".cursor-plugin/plugin.json"
  "mcp.json"
  "commands/get-context.md"
  "commands/plan-with-context-fabric.md"
  "rules/context-fabric.mdc"
  "rules/preplan-optional.mdc"
  "rules/no-automatic-use.mdc"
  "skills/context-fabric/SKILL.md"
)

# A target is owned only if it is a real directory (never a symlink or
# junction -- that would be a leftover from an earlier, unsafe design) and
# carries our exact marker. Never adopt an unowned or legacy-linked target.
owned_directory() {
  [[ -d "$config_root" && ! -L "$config_root" ]] || return 1
  [[ -f "$config_root/$marker_file" && ! -L "$config_root/$marker_file" ]] || return 1
  [[ "$(cat "$config_root/$marker_file" 2>/dev/null)" =~ ^${marker_prefix}\ [0-9]+-[0-9]+$ ]] || return 1
  return 0
}

atomic_write() {
  local dest="$1" src="$2" dest_dir tmp
  dest_dir="$(dirname "$dest")"
  if [[ -L "$dest_dir" || ! -d "$dest_dir" ]]; then
    printf 'refusing to write through a non-directory or symlinked payload parent: %s\n' "$dest_dir" >&2
    return 1
  fi
  if [[ -e "$dest" || -L "$dest" ]]; then
    [[ -f "$dest" && ! -L "$dest" ]] || {
      printf 'refusing to replace a non-regular or symlinked payload destination: %s\n' "$dest" >&2
      return 1
    }
  fi
  tmp="$(mktemp "$dest_dir/.atomic.XXXXXX")" || return 1
  if ! cp "$src" "$tmp" || ! mv -f "$tmp" "$dest"; then
    rm -f "$tmp"
    return 1
  fi
}

ensure_real_directory() {
  local directory="$1"
  if [[ -e "$directory" || -L "$directory" ]]; then
    [[ -d "$directory" && ! -L "$directory" ]] || {
      printf 'refusing to traverse a non-directory or symlinked payload path: %s\n' "$directory" >&2
      return 1
    }
  else
    mkdir "$directory"
  fi
}

payload_revision() {
  (
    cd "$package_root"
    local rel
    for rel in "${payload_files[@]}"; do cksum "$rel"; done
  ) | cksum | awk '{print $1 "-" $2}'
}

write_marker() {
  local marker_value="$1" marker_tmp
  if [[ -e "$config_root/$marker_file" || -L "$config_root/$marker_file" ]]; then
    [[ -f "$config_root/$marker_file" && ! -L "$config_root/$marker_file" ]] || {
      printf 'refusing to replace a non-regular or symlinked marker\n' >&2
      return 1
    }
  fi
  marker_tmp="$(mktemp "$config_root/.atomic.XXXXXX")" || return 1
  if ! printf '%s\n' "$marker_value" >"$marker_tmp" || ! mv -f "$marker_tmp" "$config_root/$marker_file"; then
    rm -f "$marker_tmp"
    return 1
  fi
}

if [[ "$config_root" != /* ]]; then
  printf '%s\n' 'refusing to update a target not owned by Context Fabric' >&2
  exit 1
fi
if [[ -L "$config_root" ]]; then
  printf '%s\n' 'refusing to operate on a legacy symlink or junction target; remove it manually first' >&2
  exit 1
fi
owned_directory || {
  printf '%s\n' 'refusing to update a target not owned by Context Fabric' >&2
  exit 1
}

# Full staged validation before any mutation: every required source file
# must exist and be readable before the target is touched at all.
for rel in "${payload_files[@]}"; do
  [[ -r "$package_root/$rel" ]] || {
    printf 'package source missing required file: %s\n' "$rel" >&2
    exit 1
  }
done

marker_value="$marker_prefix $(payload_revision)"

# Required directories exist before any file is replaced. If a prior update
# was interrupted partway, rerunning starts here again and converges: every
# required path is created (idempotently) before any content is touched.
ensure_real_directory "$config_root/.cursor-plugin"
ensure_real_directory "$config_root/commands"
ensure_real_directory "$config_root/rules"
ensure_real_directory "$config_root/skills"
ensure_real_directory "$config_root/skills/context-fabric"

# Each file is replaced individually and atomically (see install.sh). A
# failure partway through this loop leaves every already-replaced file on
# its new content, every not-yet-reached file on its old content, and the
# target directory itself always fully populated -- no required path is
# ever missing. Rerunning the whole update after a failure is safe: it
# simply re-replaces every file (already-correct ones included) and
# converges to the same end state.
for rel in "${payload_files[@]}"; do
  atomic_write "$config_root/$rel" "$package_root/$rel"
done

# Commit/version marker replaced last, after every content file: proves
# this update ran to completion. A failure above never reaches this line.
write_marker "$marker_value"

printf 'updated Context Fabric Cursor plugin at %s\n' "$config_root"
