#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
marker_file=".context-fabric-owner.v1"
marker_prefix="context-fabric-cursor.v1"

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
  if [[ -L "$dest_dir" || ! -d "$dest_dir" ]]; then
    printf 'refusing to write through a non-directory or symlinked payload parent: %s\n' "$dest_dir" >&2
    return 1
  fi
  if [[ -e "$dest" || -L "$dest" ]]; then
    [[ -f "$dest" && ! -L "$dest" ]] || {
      printf 'refusing to replace a non-regular or symlinked payload destination: %s\n' "$dest" >&2
      return 1
    }
  fi
  tmp="$(mktemp "$dest_dir/.atomic.XXXXXX")" || return 1
  if ! cp "$src" "$tmp" || ! mv -f "$tmp" "$dest"; then
    rm -f "$tmp"
    return 1
  fi
}

ensure_real_directory() {
  local directory="$1"
  if [[ -e "$directory" || -L "$directory" ]]; then
    [[ -d "$directory" && ! -L "$directory" ]] || {
      printf 'refusing to traverse a non-directory or symlinked payload path: %s\n' "$directory" >&2
      return 1
    }
  else
    mkdir "$directory"
  fi
}

payload_revision() {
  (
    cd "$package_root"
    local rel
    for rel in "${payload_files[@]}"; do cksum "$rel"; done
  ) | cksum | awk '{print $1 "-" $2}'
}

write_marker() {
  local marker_value="$1" marker_tmp="$config_root/.atomic.XXXXXX"
  atomic_write_text() {
    local tmp="$1" value="$2" dest="$3"
    tmp="$(mktemp "$tmp")" || return 1
    if ! printf '%s\n' "$value" >"$tmp" || ! mv -f "$tmp" "$dest"; then
      rm -f "$tmp"
      return 1
    fi
  }
  if [[ -e "$config_root/$marker_file" || -L "$config_root/$marker_file" ]]; then
    [[ -f "$config_root/$marker_file" && ! -L "$config_root/$marker_file" ]] || {
      printf 'refusing to replace a non-regular or symlinked marker\n' >&2
      return 1
    }
  fi
  atomic_write_text "$marker_tmp" "$marker_value" "$config_root/$marker_file"
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

marker_value="$marker_prefix $(payload_revision)"

# Required directories exist before any file is replaced.
mkdir -p "$config_root"
[[ -d "$config_root" && ! -L "$config_root" ]] || {
  printf '%s\n' 'refusing to operate on a legacy symlink or junction target; remove it manually first' >&2
  exit 1
}
ensure_real_directory "$config_root/.cursor-plugin"
ensure_real_directory "$config_root/commands"
ensure_real_directory "$config_root/rules"
ensure_real_directory "$config_root/skills"
ensure_real_directory "$config_root/skills/context-fabric"

for rel in "${payload_files[@]}"; do
  atomic_write "$config_root/$rel" "$package_root/$rel"
done

# Commit/version marker written last: its presence with the correct value
# is the only proof of a complete, owned install. If anything above failed,
# execution never reaches this line and the marker still reflects "not yet
# owned" (absent) or the prior owned state, so a rerun converges safely.
write_marker "$marker_value"

printf 'installed Context Fabric Cursor plugin at %s\n' "$config_root"
