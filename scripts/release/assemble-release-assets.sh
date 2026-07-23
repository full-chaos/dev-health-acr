#!/usr/bin/env bash
set -euo pipefail

binary_dir=""
container_dir=""
output_dir=""
tag=""
version=""
commit=""

while (($#)); do
  case "$1" in
    --binary) binary_dir="${2:?}"; shift 2 ;;
    --container) container_dir="${2:?}"; shift 2 ;;
    --output) output_dir="${2:?}"; shift 2 ;;
    --tag) tag="${2:?}"; shift 2 ;;
    --version) version="${2:?}"; shift 2 ;;
    --commit) commit="${2:?}"; shift 2 ;;
    *) printf 'unsupported argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

[[ -d "$binary_dir" && -d "$container_dir" && -n "$output_dir" ]]
[[ "$tag" == "v$version" ]]
[[ "$commit" =~ ^[0-9a-f]{40}$ ]]
mkdir -p "$output_dir"
test -z "$(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit)" || {
  printf 'release output directory must be empty\n' >&2
  exit 1
}

check_sums() {
  local dir="$1"
  local sums="$2"
  if command -v sha256sum >/dev/null; then
    (cd "$dir" && sha256sum --check "$sums")
  else
    (cd "$dir" && shasum -a 256 --check "$sums")
  fi
}

check_sums "$binary_dir" SHA256SUMS
check_sums "$container_dir" CONTAINER-SHA256SUMS
jq -e --arg version "$version" --arg commit "$commit" \
  '.schema_version == "release_manifest.v1" and .version == $version and .commit == $commit and (.artifacts | length == 10)' \
  "$binary_dir/release-manifest.json" >/dev/null
jq -e --arg tag "$tag" --arg version "$version" --arg commit "$commit" \
  '.schema_version == "container_release_manifest.v1" and .tag == $tag and .version == $version and .commit == $commit and ([.images[].product] | sort == ["acr-api", "acr-mcp"])' \
  "$container_dir/container-release-manifest.json" >/dev/null

for source in "$binary_dir" "$container_dir"; do
  while IFS= read -r -d '' file; do
    name="$(basename "$file")"
    [[ "$name" == SHA256SUMS || "$name" == CONTAINER-SHA256SUMS ]] && continue
    test ! -e "$output_dir/$name"
    cp "$file" "$output_dir/$name"
  done < <(find "$source" -maxdepth 1 -type f -print0)
done

tmp_sums="$(mktemp)"
trap 'rm -f "$tmp_sums"' EXIT
(
  cd "$output_dir"
  find . -maxdepth 1 -type f -exec basename {} \; | LC_ALL=C sort \
    | while IFS= read -r file; do
      if command -v sha256sum >/dev/null; then
        sha256sum "$file"
      else
        shasum -a 256 "$file"
      fi
    done
) >"$tmp_sums"
mv "$tmp_sums" "$output_dir/SHA256SUMS"
trap - EXIT
check_sums "$output_dir" SHA256SUMS
printf 'assembled release assets: %s\n' "$output_dir"
