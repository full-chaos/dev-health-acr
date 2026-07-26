#!/usr/bin/env bash
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  printf '%s must be sourced\n' "${BASH_SOURCE[0]}" >&2
  exit 2
fi

opencode_runtime_fixture_error() {
  printf '[acr-fullstack] FAIL: %s\n' "$*" >&2
  return 1
}

validate_opencode_runtime_fixture_tree() {
  local root="$1"
  python3 - "$root" <<'PY'
import hashlib
import os
import re
import stat
import sys

root = os.path.realpath(sys.argv[1])
manifest = os.path.join(root, "tree-hashes.sha256")
entries = {}
for directory, directories, files in os.walk(root, followlinks=False):
    for name in [*directories, *files]:
        path = os.path.join(directory, name)
        mode = os.lstat(path).st_mode
        if stat.S_ISLNK(mode):
            raise SystemExit("OPENCODE_RUNTIME_FIXTURE contains a symlink")
        elif not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)):
            raise SystemExit("OPENCODE_RUNTIME_FIXTURE contains an unsupported entry")

with open(manifest, encoding="utf-8") as handle:
    for raw in handle:
        line = raw.rstrip("\n")
        match = re.fullmatch(r"([0-9A-Fa-f]{64}) [ *](.+)", line)
        if match is None:
            raise SystemExit("OPENCODE_RUNTIME_FIXTURE manifest has an invalid entry")
        digest, relative = match.groups()
        normalized = os.path.normpath(relative)
        if (
            os.path.isabs(relative)
            or normalized != relative
            or normalized in ("", ".")
            or normalized == ".."
            or normalized.startswith(".." + os.sep)
            or relative in entries
        ):
            raise SystemExit("OPENCODE_RUNTIME_FIXTURE manifest has a non-normalized entry")
        entries[relative] = digest.lower()

regular_files = set()
for directory, _, files in os.walk(root, followlinks=False):
    for name in files:
        path = os.path.join(directory, name)
        if stat.S_ISREG(os.lstat(path).st_mode):
            relative = os.path.relpath(path, root)
            if relative != "tree-hashes.sha256":
                regular_files.add(relative)

if set(entries) != regular_files:
    raise SystemExit("OPENCODE_RUNTIME_FIXTURE manifest does not exactly cover the runtime tree")

for relative, expected in entries.items():
    with open(os.path.join(root, relative), "rb") as handle:
        actual = hashlib.sha256(handle.read()).hexdigest()
    if actual != expected:
        raise SystemExit("OPENCODE_RUNTIME_FIXTURE tree hash verification failed")
PY
}

stage_opencode_runtime_fixture() {
  local fixture="$1" destination="$2" root runtime stage source_manifest_hash staged_manifest_hash
  [[ -z "$fixture" ]] && return 0
  if [[ "$fixture" != /* || ! -d "$fixture" || ! -r "$fixture" || ! -d "$destination" ]]; then
    opencode_runtime_fixture_error 'OPENCODE_RUNTIME_FIXTURE must be an absolute readable directory'
    return 1
  fi
  root="$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$fixture")" \
    || { opencode_runtime_fixture_error 'OPENCODE_RUNTIME_FIXTURE cannot be canonicalized'; return 1; }
  runtime="$root/config/opencode"
  [[ -d "$runtime/node_modules" && -f "$runtime/package.json" && -f "$runtime/package-lock.json" && -f "$root/tree-hashes.sha256" ]] \
    || { opencode_runtime_fixture_error 'OPENCODE_RUNTIME_FIXTURE has an invalid runtime layout'; return 1; }
  validate_opencode_runtime_fixture_tree "$root" || return 1
  source_manifest_hash="$(shasum -a 256 "$root/tree-hashes.sha256" | cut -d' ' -f1)"
  if ! stage="$(mktemp -d "$destination/.opencode-runtime.XXXXXX")"; then
    opencode_runtime_fixture_error 'OPENCODE_RUNTIME_FIXTURE private staging failed'
    return 1
  fi
  if ! mkdir -p "$stage/config/opencode" \
    || ! cp -R "$runtime/." "$stage/config/opencode/" \
    || ! cp "$root/tree-hashes.sha256" "$stage/tree-hashes.sha256" \
    || ! validate_opencode_runtime_fixture_tree "$stage"; then
    rm -rf "$stage"
    return 1
  fi
  staged_manifest_hash="$(shasum -a 256 "$stage/tree-hashes.sha256" | cut -d' ' -f1)"
  if [[ "$source_manifest_hash" != "$staged_manifest_hash" ]]; then
    rm -rf "$stage"
    opencode_runtime_fixture_error 'OPENCODE_RUNTIME_FIXTURE staged manifest hash verification failed'
    return 1
  fi
  if ! rm -rf "$destination/node_modules" "$destination/package.json" "$destination/package-lock.json" \
    || ! mv "$stage/config/opencode/node_modules" "$destination/" \
    || ! mv "$stage/config/opencode/package.json" "$stage/config/opencode/package-lock.json" "$destination/"; then
    rm -rf "$stage"
    opencode_runtime_fixture_error 'OPENCODE_RUNTIME_FIXTURE publish failed'
    return 1
  fi
  rm -rf "$stage"
  printf '%s\n' "$source_manifest_hash"
}

cleanup_opencode_runtime_fixture() {
  local client_home="$1"
  [[ -z "$client_home" ]] || rm -rf "$client_home/config/opencode/node_modules" \
    "$client_home/config/opencode/package.json" "$client_home/config/opencode/package-lock.json"
}

opencode_runtime_fixture_receipt_json() {
  local manifest_hash="$1"
  [[ -z "$manifest_hash" ]] && { printf 'null\n'; return 0; }
  [[ "$manifest_hash" =~ ^[0-9a-f]{64}$ ]] \
    || { opencode_runtime_fixture_error 'OPENCODE_RUNTIME_FIXTURE manifest hash is invalid'; return 1; }
  printf '"%s"\n' "$manifest_hash"
}

opencode_task_argv() {
  local task_id="$1" workspace="$2" model="$3" log_level="$4" prompt="$5"
  printf '%s\0' run --title "acr-fullstack-${task_id}" --pure --format json --print-logs \
    --log-level "$log_level" --dir "$workspace" --model "$model" "$prompt"
}
