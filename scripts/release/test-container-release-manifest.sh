#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/source" "$tmp/output"

create_archive() {
  local archive="$1"
  local layout="$tmp/layout"
  local index digest size

  rm -rf "$layout"
  mkdir -p "$layout/blobs/sha256"
  index='{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1,"platform":{"os":"linux","architecture":"amd64"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":1,"platform":{"os":"linux","architecture":"arm64"}}]}'
  printf '%s' "$index" >"$layout/image-index.json"
  digest="$(shasum -a 256 "$layout/image-index.json" | awk '{print $1}')"
  size="$(wc -c <"$layout/image-index.json" | tr -d ' ')"
  mv "$layout/image-index.json" "$layout/blobs/sha256/$digest"
  jq -cn --arg digest "sha256:$digest" --argjson size "$size" \
    '{schemaVersion:2,manifests:[{mediaType:"application/vnd.oci.image.index.v1+json",digest:$digest,size:$size}]}' \
    >"$layout/index.json"
  printf '{"imageLayoutVersion":"1.0.0"}\n' >"$layout/oci-layout"
  tar -cf "$archive" -C "$layout" .
}

create_archive "$tmp/source/acr-api.tar"
create_archive "$tmp/source/acr-mcp.tar"

"$root/scripts/release/write-container-release-manifest.sh" \
  --source "$tmp/source" \
  --output "$tmp/output" \
  --tag v1.2.3 \
  --version 1.2.3 \
  --commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --date 2026-07-23T00:00:00Z

manifest="$tmp/output/container-release-manifest.json"
jq -e '
  .schema_version == "container_release_manifest.v1" and
  .tag == "v1.2.3" and
  .version == "1.2.3" and
  .commit == "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" and
  .date == "2026-07-23T00:00:00Z" and
  (.images | length == 2) and
  ([.images[].product] | sort == ["acr-api", "acr-mcp"]) and
  all(.images[];
    . as $image |
    $image.repository == ("ghcr.io/full-chaos/dev-health-acr/" + $image.product) and
    ($image.archive | test("^" + $image.product + "_1\\.2\\.3_linux_multiarch\\.oci\\.tar$")) and
    ($image.archive_sha256 | test("^[0-9a-f]{64}$")) and
    ($image.digest | test("^sha256:[0-9a-f]{64}$")) and
    $image.platforms == ["linux/amd64", "linux/arm64"])
' "$manifest" >/dev/null

for product in acr-api acr-mcp; do
  archive="$tmp/output/${product}_1.2.3_linux_multiarch.oci.tar"
  test -f "$archive"
  expected="$(jq -r --arg product "$product" '.images[] | select(.product == $product) | .archive_sha256' "$manifest")"
  test "$(shasum -a 256 "$archive" | awk '{print $1}')" = "$expected"
done

main_commit=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
main_version="1.2.4-main.$main_commit"
mkdir "$tmp/main-output"
"$root/scripts/release/write-container-release-manifest.sh" \
  --source "$tmp/source" \
  --output "$tmp/main-output" \
  --tag "$main_commit" \
  --version "$main_version" \
  --commit "$main_commit" \
  --date 2026-07-24T00:00:00Z

jq -e --arg commit "$main_commit" --arg version "$main_version" '
  .schema_version == "container_release_manifest.v1" and
  .tag == $commit and
  .version == $version and
  .commit == $commit and
  (.images | length == 2) and
  all(.images[]; .archive | contains($version))
' "$tmp/main-output/container-release-manifest.json" >/dev/null

mkdir "$tmp/main-mismatch"
if "$root/scripts/release/write-container-release-manifest.sh" \
  --source "$tmp/source" \
  --output "$tmp/main-mismatch" \
  --tag "$main_commit" \
  --version "1.2.4-main.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  --commit "$main_commit" \
  --date 2026-07-24T00:00:00Z; then
  exit 1
fi

mkdir "$tmp/version-mismatch"
if "$root/scripts/release/write-container-release-manifest.sh" \
  --source "$tmp/source" \
  --output "$tmp/version-mismatch" \
  --tag v1.2.3 \
  --version 1.2.4 \
  --commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --date 2026-07-24T00:00:00Z; then
  exit 1
fi

printf 'container release manifest fixture passed\n'
