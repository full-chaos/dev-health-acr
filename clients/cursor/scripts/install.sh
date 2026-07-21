#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
owner_file=".context-fabric-owner.v1"
owner_value="context-fabric-cursor.v1"

if [[ "$config_root" != /* ]]; then
  printf '%s\n' 'CURSOR_PLUGIN_DIR must be an absolute path' >&2
  exit 2
fi
if [[ -e "$config_root" ]]; then
  if [[ -d "$config_root" && -z "$(find "$config_root" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    rmdir "$config_root"
  else
    printf '%s\n' 'refusing to install into a non-empty target' >&2
    exit 1
  fi
fi

parent="$(dirname "$config_root")"
mkdir -p "$parent"
stage="$(mktemp -d "$parent/.context-fabric-cursor.XXXXXX")"
installed=0
cleanup() { if (( ! installed )); then rm -rf "$stage"; fi; }
trap cleanup EXIT
mkdir -p "$stage/.cursor-plugin"
cp "$package_root/.cursor-plugin/plugin.json" "$stage/.cursor-plugin/plugin.json"
cp "$package_root/mcp.json" "$stage/mcp.json"
cp -R "$package_root/commands" "$stage/commands"
cp -R "$package_root/rules" "$stage/rules"
cp -R "$package_root/skills" "$stage/skills"
printf '%s\n' "$owner_value" >"$stage/$owner_file"
ln -s "$(basename "$stage")" "$config_root"
installed=1
trap - EXIT
printf 'installed Context Fabric Cursor plugin at %s\n' "$config_root"
