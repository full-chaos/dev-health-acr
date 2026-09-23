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
[[ "$commit" =~ ^[0-9a-f]{40}$ ]]
if [[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]] \
  && [[ "$tag" == "v$version" ]]; then
  :
elif [[ "$tag" == "$commit" ]] \
  && [[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-main\.[0-9a-f]{40}$ ]] \
  && [[ "$version" == *"-main.$commit" ]]; then
  :
else
  printf 'tag/version must identify a canonical version release or matching main commit\n' >&2
  exit 1
fi
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
trap 'rm -f "$tmp_sums" "$tmp_sums".*' EXIT
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
# Per-product manifests (CHAOS-6236). One table-driven pass over the SAME
# lines that make up the combined SHA256SUMS, so a per-product manifest can
# never disagree with it. A line belongs to a product when its filename starts
# with "<product>_" (archives, OCI tar, .spdx.json) or "<product>-" (per-image
# SBOM/trivy scans). Shared files (release-manifest.json, trivy-db-*) match no
# product and stay only in the combined manifest. The combined SHA256SUMS is
# derived BEFORE these files exist, so it never lists them, and they never list
# themselves. Lines are copied byte-for-byte, keeping the combined LC_ALL=C
# order, so the output is deterministic. An empty product manifest is an error:
# it would sign nothing and read as coverage.
product_manifests=(acr-api acr-mcp)
for product in "${product_manifests[@]}"; do
  awk -v a="${product}_" -v b="${product}-" \
    '{ n = substr($0, index($0, "  ") + 2); if (index(n, a) == 1 || index(n, b) == 1) print }' \
    "$tmp_sums" >"$tmp_sums.$product"
  test -s "$tmp_sums.$product" || {
    printf 'no %s assets found for its per-product manifest\n' "$product" >&2
    rm -f "$tmp_sums".*
    exit 1
  }
done
mv "$tmp_sums" "$output_dir/SHA256SUMS"
for product in "${product_manifests[@]}"; do
  mv "$tmp_sums.$product" "$output_dir/${product}-SHA256SUMS"
done
trap - EXIT
check_sums "$output_dir" SHA256SUMS
for product in "${product_manifests[@]}"; do
  check_sums "$output_dir" "${product}-SHA256SUMS"
done
printf 'assembled release assets: %s\n' "$output_dir"
