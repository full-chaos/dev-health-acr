#!/usr/bin/env bash
set -euo pipefail

config_root="${OPENCODE_CONFIG_DIR:-${HOME:?HOME is required}/.config/context-fabric-opencode}"
owner_file=".context-fabric-owner.v1"
owner_value="context-fabric-opencode.v1"

owned_stage() {
  local link stage
  [[ -L "$config_root" ]] || return 1
  link="$(readlink "$config_root")"
  [[ "$link" == .context-fabric-opencode.* && "$link" != */* ]] || return 1
  stage="$(dirname "$config_root")/$link"
  [[ -d "$stage" && ! -L "$stage" && -f "$stage/$owner_file" && ! -L "$stage/$owner_file" ]] || return 1
  [[ "$(cat "$stage/$owner_file")" == "$owner_value" ]] || return 1
  printf '%s\n' "$stage"
}

if [[ "$config_root" != /* ]] || ! stage="$(owned_stage)"; then
  printf '%s\n' 'refusing to remove a target not owned by Context Fabric' >&2
  exit 1
fi
rm "$config_root"
rm -rf "$stage"
printf 'removed Context Fabric OpenCode config at %s\n' "$config_root"
