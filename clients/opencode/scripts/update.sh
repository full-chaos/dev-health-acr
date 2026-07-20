#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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

if [[ "$config_root" != /* ]] || ! previous_stage="$(owned_stage)"; then
  printf '%s\n' 'refusing to update a target not owned by Context Fabric' >&2
  exit 1
fi
parent="$(dirname "$config_root")"
stage="$(mktemp -d "$parent/.context-fabric-opencode.XXXXXX")"
link="$(mktemp "$parent/.context-fabric-opencode.link.XXXXXX")"
rm "$link"
cleanup() {
  rm -rf "$stage" "$link"
}
trap cleanup EXIT
cp -R "$package_root/config/." "$stage/"
printf '%s\n' "$owner_value" >"$stage/$owner_file"
ln -s "$(basename "$stage")" "$link"
if mv --version >/dev/null 2>&1; then
  mv -Tf "$link" "$config_root"
else
  mv -hf "$link" "$config_root"
fi
rm -rf "$previous_stage"
trap - EXIT
printf 'updated Context Fabric OpenCode config at %s\n' "$config_root"
