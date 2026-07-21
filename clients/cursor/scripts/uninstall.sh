#!/usr/bin/env bash
set -euo pipefail

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

if [[ "$config_root" != /* ]] || ! stage="$(owned_stage)"; then
  printf '%s\n' 'refusing to remove a target not owned by Context Fabric' >&2
  exit 1
fi
parent="$(dirname "$config_root")"
rm "$config_root"
rm -rf "$stage"
for entry in "$parent"/.context-fabric-cursor.*; do
  [[ -e "$entry" ]] || continue
  [[ -d "$entry" && ! -L "$entry" ]] || continue
  [[ -f "$entry/$owner_file" && ! -L "$entry/$owner_file" ]] || continue
  [[ "$(cat "$entry/$owner_file" 2>/dev/null)" == "$owner_value" ]] || continue
  rm -rf "$entry"
done
printf 'removed Context Fabric Cursor plugin at %s\n' "$config_root"
