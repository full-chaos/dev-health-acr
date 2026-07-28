#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/binary" "$tmp/container" "$tmp/output"
commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

artifacts=()
for number in 1 2 3 4 5 6 7 8 9 10; do
  name="binary-$number.tar.gz"
  printf 'binary %s\n' "$number" >"$tmp/binary/$name"
  artifacts+=("$name")
done
jq -cn --arg commit "$commit" --argjson artifacts "$(printf '%s\n' "${artifacts[@]}" | jq -R '{name:.,sha256:"fixture"}' | jq -s .)" \
  '{schema_version:"release_manifest.v1",version:"1.2.3",commit:$commit,date:"2026-07-23T00:00:00Z",artifacts:$artifacts}' \
  >"$tmp/binary/release-manifest.json"
(
  cd "$tmp/binary"
  for file in ./*; do shasum -a 256 "${file#./}"; done
) >"$tmp/binary-SHA256SUMS"
mv "$tmp/binary-SHA256SUMS" "$tmp/binary/SHA256SUMS"

for product in acr-api acr-mcp; do
  printf '%s image\n' "$product" >"$tmp/container/${product}_1.2.3_linux_multiarch.oci.tar"
done
jq -cn --arg commit "$commit" \
  '{schema_version:"container_release_manifest.v1",tag:"v1.2.3",version:"1.2.3",commit:$commit,date:"2026-07-23T00:00:00Z",images:[{product:"acr-api"},{product:"acr-mcp"}]}' \
  >"$tmp/container/container-release-manifest.json"
(
  cd "$tmp/container"
  for file in ./*; do shasum -a 256 "${file#./}"; done
) >"$tmp/CONTAINER-SHA256SUMS"
mv "$tmp/CONTAINER-SHA256SUMS" "$tmp/container/CONTAINER-SHA256SUMS"

"$root/scripts/release/assemble-release-assets.sh" \
  --binary "$tmp/binary" \
  --container "$tmp/container" \
  --output "$tmp/output" \
  --tag v1.2.3 \
  --version 1.2.3 \
  --commit "$commit"

test -f "$tmp/output/SHA256SUMS"
if grep -F '  SHA256SUMS' "$tmp/output/SHA256SUMS"; then exit 1; fi
(cd "$tmp/output" && shasum -a 256 --check SHA256SUMS)
test "$(find "$tmp/output" -maxdepth 1 -type f | wc -l | tr -d ' ')" -eq 15

main_commit=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
main_version="1.2.4-main.$main_commit"
cp -R "$tmp/binary" "$tmp/main-binary"
cp -R "$tmp/container" "$tmp/main-container"
jq --arg version "$main_version" --arg commit "$main_commit" \
  '.version = $version | .commit = $commit' \
  "$tmp/main-binary/release-manifest.json" >"$tmp/main-binary/release-manifest.tmp"
mv "$tmp/main-binary/release-manifest.tmp" "$tmp/main-binary/release-manifest.json"
(
  cd "$tmp/main-binary"
  for file in ./*; do
    [[ "${file#./}" == SHA256SUMS ]] && continue
    shasum -a 256 "${file#./}"
  done
) >"$tmp/main-binary/SHA256SUMS"
jq --arg tag "$main_commit" --arg version "$main_version" --arg commit "$main_commit" \
  '.tag = $tag | .version = $version | .commit = $commit' \
  "$tmp/main-container/container-release-manifest.json" >"$tmp/main-container/container-release-manifest.tmp"
mv "$tmp/main-container/container-release-manifest.tmp" "$tmp/main-container/container-release-manifest.json"
(
  cd "$tmp/main-container"
  for file in ./*; do
    [[ "${file#./}" == CONTAINER-SHA256SUMS ]] && continue
    shasum -a 256 "${file#./}"
  done
) >"$tmp/main-container/CONTAINER-SHA256SUMS"
mkdir "$tmp/main-output"
"$root/scripts/release/assemble-release-assets.sh" \
  --binary "$tmp/main-binary" \
  --container "$tmp/main-container" \
  --output "$tmp/main-output" \
  --tag "$main_commit" \
  --version "$main_version" \
  --commit "$main_commit"
(cd "$tmp/main-output" && shasum -a 256 --check SHA256SUMS)

mkdir "$tmp/main-mismatch-output"
if "$root/scripts/release/assemble-release-assets.sh" \
  --binary "$tmp/main-binary" \
  --container "$tmp/main-container" \
  --output "$tmp/main-mismatch-output" \
  --tag "$main_commit" \
  --version "1.2.4-main.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  --commit "$main_commit"; then
  exit 1
fi

printf 'tampered\n' >>"$tmp/binary/binary-1.tar.gz"
mkdir "$tmp/rejected"
if "$root/scripts/release/assemble-release-assets.sh" \
  --binary "$tmp/binary" \
  --container "$tmp/container" \
  --output "$tmp/rejected" \
  --tag v1.2.3 \
  --version 1.2.3 \
  --commit "$commit"; then
  exit 1
fi

printf 'release assembly fixture passed\n'
