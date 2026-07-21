#!/usr/bin/env bash
set -euo pipefail

config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
marker_file=".context-fabric-owner.v1"
marker_prefix="context-fabric-cursor.v1"

# Only ever remove a target proven owned: a real directory (never a symlink
# or junction) carrying our exact marker. An unowned or legacy-linked
# target is left completely untouched.
owned_directory() {
  [[ -d "$config_root" && ! -L "$config_root" ]] || return 1
  [[ -f "$config_root/$marker_file" && ! -L "$config_root/$marker_file" ]] || return 1
  [[ "$(cat "$config_root/$marker_file" 2>/dev/null)" =~ ^${marker_prefix}\ [0-9]+-[0-9]+$ ]] || return 1
  return 0
}

if [[ "$config_root" != /* ]]; then
  printf '%s\n' 'refusing to remove a target not owned by Context Fabric' >&2
  exit 1
fi
if [[ -L "$config_root" ]]; then
  printf '%s\n' 'refusing to operate on a legacy symlink or junction target; remove it manually first' >&2
  exit 1
fi
owned_directory || {
  printf '%s\n' 'refusing to remove a target not owned by Context Fabric' >&2
  exit 1
}

rm -rf "$config_root"
printf 'removed Context Fabric Cursor plugin at %s\n' "$config_root"
