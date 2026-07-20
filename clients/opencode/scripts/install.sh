#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${OPENCODE_CONFIG_DIR:-${HOME:?HOME is required}/.config/context-fabric-opencode}"
owner_file=".context-fabric-owner.v1"
owner_value="context-fabric-opencode.v1"

if [[ "$config_root" != /* ]]; then
  printf '%s\n' 'OPENCODE_CONFIG_DIR must be an absolute path' >&2
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
stage="$(mktemp -d "$parent/.context-fabric-opencode.XXXXXX")"
installed=0
cleanup() { if (( ! installed )); then rm -rf "$stage"; fi; }
trap cleanup EXIT
cp -R "$package_root/config/." "$stage/"
printf '%s\n' "$owner_value" >"$stage/$owner_file"
ln -s "$(basename "$stage")" "$config_root"
installed=1
trap - EXIT
printf 'installed Context Fabric OpenCode config at %s\n' "$config_root"
