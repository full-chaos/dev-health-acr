#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
marker_file=".context-fabric-owner.v1"
marker_value="context-fabric-cursor.v1"

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
  [[ "$(cat "$config_root/$marker_file" 2>/dev/null)" == "$marker_value" ]] || return 1
  return 0
}

atomic_write() {
  local dest="$1" src="$2" dest_dir tmp
  dest_dir="$(dirname "$dest")"
  mkdir -p "$dest_dir"
  tmp="$(mktemp "$dest_dir/.atomic.XXXXXX")"
  cp "$src" "$tmp"
  mv -f "$tmp" "$dest"
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

# Required directories exist before any file is replaced. If a prior update
# was interrupted partway, rerunning starts here again and converges: every
# required path is created (idempotently) before any content is touched.
mkdir -p "$config_root/.cursor-plugin" "$config_root/commands" "$config_root/rules" "$config_root/skills/context-fabric"

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
marker_tmp="$(mktemp "$config_root/.atomic.XXXXXX")"
printf '%s\n' "$marker_value" >"$marker_tmp"
mv -f "$marker_tmp" "$config_root/$marker_file"

printf 'updated Context Fabric Cursor plugin at %s\n' "$config_root"
