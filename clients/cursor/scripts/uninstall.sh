#!/usr/bin/env bash
set -euo pipefail

config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
owner_file=".context-fabric-owner.v1"
owner_value="context-fabric-cursor.v1"
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
  printf '%s\n' 'refusing to remove a target not owned by Context Fabric' >&2
  exit 1
fi
rm "$config_root"
rm -rf "$stages_root"
printf 'removed Context Fabric Cursor plugin at %s\n' "$config_root"
