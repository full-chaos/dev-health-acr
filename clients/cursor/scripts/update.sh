#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
owner_file=".context-fabric-owner.v1"
owner_value="context-fabric-cursor.v1"
# Stages for this target live in a directory scoped to the target's own
# name, never in the shared parent -- so a sibling install under the same
# parent can never observe, prune, or delete this target's generations.
stages_root="${config_root}.stages"

owned_stage() {
  local link prefix remainder stage
  [[ -L "$config_root" ]] || return 1
  link="$(readlink "$config_root")"
  prefix="$(basename "$config_root").stages/"
  [[ "${link:0:${#prefix}}" == "$prefix" ]] || return 1
  remainder="${link:${#prefix}}"
  [[ -n "$remainder" && "$remainder" != */* ]] || return 1
  stage="$stages_root/$remainder"
  [[ -d "$stage" && ! -L "$stage" && -f "$stage/$owner_file" && ! -L "$stage/$owner_file" ]] || return 1
  [[ "$(cat "$stage/$owner_file")" == "$owner_value" ]] || return 1
  printf '%s\n' "$stage"
}

if [[ "$config_root" != /* ]] || ! owned_stage >/dev/null; then
  printf '%s\n' 'refusing to update a target not owned by Context Fabric' >&2
  exit 1
fi
mkdir -p "$stages_root"
stage="$(mktemp -d "$stages_root/XXXXXX")"
parent="$(dirname "$config_root")"
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
target_name="$(basename "$config_root")"
ln -s "$target_name.stages/$(basename "$stage")" "$link"
if mv --version >/dev/null 2>&1; then
  mv -Tf "$link" "$config_root"
else
  mv -hf "$link" "$config_root"
fi
published=1
trap - EXIT
# Every prior generation is retained on disk under $stages_root until an
# owned uninstall removes it -- a reader that resolved any earlier
# generation, however long ago, still finds it intact.
printf 'updated Context Fabric Cursor plugin at %s (all prior generations retained under %s until uninstall)\n' "$config_root" "$stages_root"
