#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_root="${CURSOR_PLUGIN_DIR:-${HOME:?HOME is required}/.cursor/plugins/local/context-fabric}"
marker_file=".context-fabric-owner.v1"
marker_prefix="context-fabric-cursor.v1"
target_created=0
install_complete=0
lock_acquired=0
lock_dir=""
install_owner_file=""
install_owner_token=""
owner_setup_failed=0
created_payload_files=()
created_payload_dirs=()

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
  [[ ! -e "$dest" && ! -L "$dest" ]] || {
    printf 'refusing to replace an existing payload destination during install: %s\n' "$dest" >&2
    return 1
  }
  tmp="$(mktemp "$dest_dir/.atomic.XXXXXX")" || return 1
  if ! cp "$src" "$tmp" || ! mv -f "$tmp" "$dest"; then
    rm -f "$tmp"
    return 1
  fi
  created_payload_files+=("$dest")
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
    created_payload_dirs+=("$directory")
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
  [[ ! -e "$config_root/$marker_file" && ! -L "$config_root/$marker_file" ]] || {
    printf 'refusing to replace an existing marker during install\n' >&2
    return 1
  }
  atomic_write_text "$marker_tmp" "$marker_value" "$config_root/$marker_file" || return 1
  created_payload_files+=("$config_root/$marker_file")
}

release_install_lock() {
  (( lock_acquired )) || return 0
  rmdir -- "$lock_dir" || {
    printf 'refusing to leave an unremovable install lock: %s\n' "$lock_dir" >&2
    return 1
  }
  lock_acquired=0
}

cleanup_created_payload() {
  local path index
  for (( index = ${#created_payload_files[@]} - 1; index >= 0; index-- )); do
    path="${created_payload_files[index]}"
    [[ -f "$path" && ! -L "$path" ]] && rm -f -- "$path"
  done
  for (( index = ${#created_payload_dirs[@]} - 1; index >= 0; index-- )); do
    path="${created_payload_dirs[index]}"
    [[ -d "$path" && ! -L "$path" ]] && rmdir -- "$path" 2>/dev/null || true
  done
}

run_owns_created_target() {
  [[ -n "$install_owner_file" && -f "$install_owner_file" && ! -L "$install_owner_file" ]] || return 1
  [[ "$(cat "$install_owner_file")" == "$install_owner_token" ]]
}

cleanup_failed_install() {
  (( install_complete )) && return 0
  if (( target_created )); then
    if run_owns_created_target; then
      cleanup_created_payload
      rm -f -- "$install_owner_file"
      install_owner_file=""
      [[ -d "$config_root" && ! -L "$config_root" ]] && rmdir -- "$config_root" 2>/dev/null || true
    elif (( owner_setup_failed )); then
      [[ -d "$config_root" && ! -L "$config_root" ]] && rmdir -- "$config_root" 2>/dev/null || true
    fi
  else
    cleanup_created_payload
  fi
}

cleanup_install() {
  local status=$?
  cleanup_failed_install
  release_install_lock || status=1
  exit "$status"
}

trap cleanup_install EXIT

if [[ "$config_root" != /* ]]; then
  printf '%s\n' 'CURSOR_PLUGIN_DIR must be an absolute path' >&2
  exit 2
fi
while [[ "$config_root" != / && "$config_root" == */ ]]; do
  config_root="${config_root%/}"
done
config_parent="$(dirname "$config_root")"
mkdir -p "$config_parent"
[[ -d "$config_parent" && ! -L "$config_parent" ]] || {
  printf 'refusing to create an install lock beneath a non-directory or symlinked parent: %s\n' "$config_parent" >&2
  exit 1
}
lock_dir="${config_root}.install.lock"
mkdir -- "$lock_dir" || {
  printf 'refusing to install while another install holds the target lock: %s\n' "$config_root" >&2
  exit 1
}
lock_acquired=1
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
else
  mkdir -- "$config_root" || {
    printf 'refusing to install because the target appeared during creation: %s\n' "$config_root" >&2
    exit 1
  }
  target_created=1
  install_owner_file="$(mktemp "$config_root/.install-owner.XXXXXX")" || {
    owner_setup_failed=1
    exit 1
  }
  install_owner_token="${BASHPID}-${RANDOM}-${RANDOM}"
  printf '%s\n' "$install_owner_token" >"$install_owner_file" || {
    rm -f -- "$install_owner_file"
    install_owner_file=""
    owner_setup_failed=1
    exit 1
  }
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
if (( target_created )); then
  run_owns_created_target || {
    printf '%s\n' 'refusing to complete an install whose created target ownership changed' >&2
    exit 1
  }
  rm -f -- "$install_owner_file"
  install_owner_file=""
fi
install_complete=1

printf 'installed Context Fabric Cursor plugin at %s\n' "$config_root"
