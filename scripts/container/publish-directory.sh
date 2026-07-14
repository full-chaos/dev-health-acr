#!/usr/bin/env bash
set -euo pipefail

stable_root="${1:?stable output path is required}"
candidate_root="${2:?candidate directory is required}"
lock_timeout="${3:-60}"
generation_parent="${stable_root}.generations"
publish_lock="${stable_root}.publish.lock"
next_link="${stable_root}.next.$$-${RANDOM}"
lock_held=0
generation_root=""
generation_target=""
previous_target=""

[[ "$lock_timeout" =~ ^[1-9][0-9]*$ ]] || { printf 'publication lock timeout must be a positive integer\n' >&2; exit 2; }
[[ -d "$candidate_root" && ! -L "$candidate_root" ]] || { printf 'publication candidate must be a directory: %s\n' "$candidate_root" >&2; exit 2; }

cleanup() {
  rm -f "$next_link"
  if [[ -n "$generation_root" ]]; then
    current_target=""
    if [[ -L "$stable_root" ]]; then
      current_target="$(readlink "$stable_root" 2>/dev/null || true)"
    fi
    if [[ "$current_target" != "$generation_target" ]]; then
      rm -rf "$generation_root"
    fi
  fi
  if [[ "$lock_held" == 1 ]]; then
    rm -rf "$publish_lock"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$(dirname "$stable_root")" "$generation_parent"
deadline=$((SECONDS + lock_timeout))
until mkdir "$publish_lock" 2>/dev/null; do
  if [[ -d "$publish_lock" && ! -L "$publish_lock" && -f "${publish_lock}/pid" ]]; then
    lock_owner="$(cat "${publish_lock}/pid" 2>/dev/null || true)"
    if [[ "$lock_owner" =~ ^[1-9][0-9]*$ ]] && ! kill -0 "$lock_owner" 2>/dev/null; then
      rm -rf "$publish_lock"
      continue
    fi
  fi
  if ((SECONDS >= deadline)); then
    printf 'timed out waiting for publication lock: %s\n' "$publish_lock" >&2
    exit 1
  fi
  sleep 1
done
lock_held=1
printf '%s\n' "$$" >"${publish_lock}/pid"

if [[ -e "$stable_root" && ! -L "$stable_root" ]]; then
  printf 'stable publication path must be absent or an atomic pointer: %s\n' "$stable_root" >&2
  exit 1
fi
if [[ -L "$stable_root" ]]; then
  previous_target="$(readlink "$stable_root")"
fi

generation_root="$(mktemp -d "${generation_parent}/generation.XXXXXX")"
rmdir "$generation_root"
generation_target="$(basename "$generation_parent")/$(basename "$generation_root")"
mv "$candidate_root" "$generation_root"
ln -s "$generation_target" "$next_link"
if ! mv -Tf "$next_link" "$stable_root" 2>/dev/null; then
  mv -fh "$next_link" "$stable_root"
fi
generation_root=""

previous_root=""
case "$previous_target" in
  "$(basename "$generation_parent")"/generation.*)
    previous_root="$(dirname "$stable_root")/${previous_target}"
    ;;
esac
for old_generation in "${generation_parent}"/generation.*; do
  [[ -d "$old_generation" ]] || continue
  [[ "$old_generation" == "$(dirname "$stable_root")/${generation_target}" ]] && continue
  [[ -n "$previous_root" && "$old_generation" == "$previous_root" ]] && continue
  rm -rf "$old_generation"
done

rm -rf "$publish_lock"
lock_held=0
