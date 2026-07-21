#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
owner_file=".context-fabric-owner.v1"
owner_value="context-fabric-cursor.v1"

owned_stage() {
  local link stage
  [[ -L "$config_root" ]] || return 1
  link="$(readlink "$config_root")"
  [[ "$link" == .context-fabric-cursor.* && "$link" != */* ]] || return 1
  stage="$(dirname "$config_root")/$link"
  [[ -d "$stage" && ! -L "$stage" && -f "$stage/$owner_file" && ! -L "$stage/$owner_file" ]] || return 1
  [[ "$(cat "$stage/$owner_file")" == "$owner_value" ]] || return 1
  printf '%s\n' "$stage"
}

if [[ "$config_root" != /* ]] || ! previous_stage="$(owned_stage)"; then
  printf '%s\n' 'refusing to update a target not owned by Context Fabric' >&2
  exit 1
fi
parent="$(dirname "$config_root")"
stage="$(mktemp -d "$parent/.context-fabric-cursor.XXXXXX")"
link="$(mktemp "$parent/.context-fabric-cursor.link.XXXXXX")"
rm "$link"
published=0
cleanup() {
  rm -rf "$link"
  if (( ! published )); then rm -rf "$stage"; fi
}
trap cleanup EXIT
mkdir -p "$stage/.cursor-plugin"
cp "$package_root/.cursor-plugin/plugin.json" "$stage/.cursor-plugin/plugin.json"
cp "$package_root/mcp.json" "$stage/mcp.json"
cp -R "$package_root/commands" "$stage/commands"
cp -R "$package_root/rules" "$stage/rules"
cp -R "$package_root/skills" "$stage/skills"
printf '%s\n' "$owner_value" >"$stage/$owner_file"
ln -s "$(basename "$stage")" "$link"
if mv --version >/dev/null 2>&1; then
  mv -Tf "$link" "$config_root"
else
  mv -hf "$link" "$config_root"
fi
published=1
trap - EXIT
if ! rm -rf "$previous_stage"; then
  printf '%s\n' 'updated Context Fabric Cursor plugin; old owned stage cleanup failed' >&2
  exit 1
fi
printf 'updated Context Fabric Cursor plugin at %s\n' "$config_root"
