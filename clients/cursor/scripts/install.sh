#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
marker_file=".context-fabric-owner.v1"
marker_value="context-fabric-cursor.v1"

# The plugin lives at $config_root as a stable, owned, real directory --
# never a symlink or junction. Updates replace files in place; there is no
# directory swap, so mixed-old/new-file reads during an update are the
# accepted tradeoff for never needing a directory-level cutover primitive
# (which is unsafe for a directory target on Windows).
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

# Replace one regular file atomically: write into a same-directory temp
# file, then rename it over the destination. A same-directory rename is a
# single filesystem operation, so the destination is never briefly absent
# -- a reader either sees the old content or the new content, never ENOENT.
atomic_write() {
  local dest="$1" src="$2" dest_dir tmp
  dest_dir="$(dirname "$dest")"
  mkdir -p "$dest_dir"
  tmp="$(mktemp "$dest_dir/.atomic.XXXXXX")"
  cp "$src" "$tmp"
  mv -f "$tmp" "$dest"
}

if [[ "$config_root" != /* ]]; then
  printf '%s\n' 'CURSOR_PLUGIN_DIR must be an absolute path' >&2
  exit 2
fi
if [[ -L "$config_root" ]]; then
  printf '%s\n' 'refusing to operate on a legacy symlink or junction target; remove it manually first' >&2
  exit 1
fi
if [[ -e "$config_root" ]]; then
  if [[ -d "$config_root" && -z "$(find "$config_root" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    : # an empty, unowned directory is fine to install directly into
  else
    printf '%s\n' 'refusing to install into a non-empty target' >&2
    exit 1
  fi
fi

# Full staged validation before any mutation: every required source file
# must exist and be readable before the target is touched at all.
for rel in "${payload_files[@]}"; do
  [[ -r "$package_root/$rel" ]] || {
    printf 'package source missing required file: %s\n' "$rel" >&2
    exit 1
  }
done

# Required directories exist before any file is replaced.
mkdir -p "$config_root/.cursor-plugin" "$config_root/commands" "$config_root/rules" "$config_root/skills/context-fabric"

for rel in "${payload_files[@]}"; do
  atomic_write "$config_root/$rel" "$package_root/$rel"
done

# Commit/version marker written last: its presence with the correct value
# is the only proof of a complete, owned install. If anything above failed,
# execution never reaches this line and the marker still reflects "not yet
# owned" (absent) or the prior owned state, so a rerun converges safely.
marker_tmp="$(mktemp "$config_root/.atomic.XXXXXX")"
printf '%s\n' "$marker_value" >"$marker_tmp"
mv -f "$marker_tmp" "$config_root/$marker_file"

printf 'installed Context Fabric Cursor plugin at %s\n' "$config_root"
