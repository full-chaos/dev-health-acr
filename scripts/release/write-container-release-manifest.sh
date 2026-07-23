#!/usr/bin/env bash
set -euo pipefail

source_dir=""
output_dir=""
tag=""
version=""
commit=""
date=""

while (($#)); do
  case "$1" in
    --source) source_dir="${2:?}"; shift 2 ;;
    --output) output_dir="${2:?}"; shift 2 ;;
    --tag) tag="${2:?}"; shift 2 ;;
    --version) version="${2:?}"; shift 2 ;;
    --commit) commit="${2:?}"; shift 2 ;;
    --date) date="${2:?}"; shift 2 ;;
    *) printf 'unsupported argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

[[ -d "$source_dir" && -n "$output_dir" ]]
[[ "$tag" == "v$version" ]]
[[ "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]]
[[ "$commit" =~ ^[0-9a-f]{40}$ ]]
[[ "$date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]

mkdir -p "$output_dir"
test -z "$(find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit)" || {
  printf 'container release output directory must be empty\n' >&2
  exit 1
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

checksum() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

archive_member() {
  local archive="$1"
  local expected="$2"
  tar -tf "$archive" | awk -v expected="$expected" '$0 == expected || $0 == "./" expected { print; exit }'
}

for product in acr-api acr-mcp; do
  source_archive="$source_dir/$product.tar"
  archive="${product}_${version}_linux_multiarch.oci.tar"
  test -f "$source_archive"

  root_member="$(archive_member "$source_archive" index.json)"
  test -n "$root_member"
  tar -xOf "$source_archive" "$root_member" >"$tmp/root-index.json"
  image_digest="$(jq -er '.manifests | select(length == 1) | .[0] | select(.mediaType == "application/vnd.oci.image.index.v1+json") | .digest' "$tmp/root-index.json")"
  image_size="$(jq -er '.manifests[0].size' "$tmp/root-index.json")"
  [[ "$image_digest" =~ ^sha256:[0-9a-f]{64}$ ]]
  [[ "$image_size" =~ ^[1-9][0-9]*$ ]]

  blob="blobs/sha256/${image_digest#sha256:}"
  blob_member="$(archive_member "$source_archive" "$blob")"
  test -n "$blob_member"
  tar -xOf "$source_archive" "$blob_member" >"$tmp/image-index.json"
  test "$(wc -c <"$tmp/image-index.json" | tr -d ' ')" = "$image_size"
  test "sha256:$(checksum "$tmp/image-index.json")" = "$image_digest"
  jq -e '[.manifests[] | select(.platform.os == "linux") | (.platform.os + "/" + .platform.architecture)] | sort == ["linux/amd64", "linux/arm64"]' "$tmp/image-index.json" >/dev/null

  cp "$source_archive" "$output_dir/$archive"
  archive_sha256="$(checksum "$output_dir/$archive")"
  jq -cn \
    --arg product "$product" \
    --arg repository "ghcr.io/full-chaos/dev-health-acr/$product" \
    --arg archive "$archive" \
    --arg archive_sha256 "$archive_sha256" \
    --arg digest "$image_digest" \
    '{product:$product,repository:$repository,archive:$archive,archive_sha256:$archive_sha256,digest:$digest,platforms:["linux/amd64","linux/arm64"]}' \
    >"$tmp/$product.json"
done

jq -s \
  --arg tag "$tag" \
  --arg version "$version" \
  --arg commit "$commit" \
  --arg date "$date" \
  '{schema_version:"container_release_manifest.v1",tag:$tag,version:$version,commit:$commit,date:$date,images:.}' \
  "$tmp/acr-api.json" "$tmp/acr-mcp.json" >"$output_dir/container-release-manifest.json"

printf 'container release manifest: %s\n' "$output_dir/container-release-manifest.json"
