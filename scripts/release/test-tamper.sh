#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
release_dir=""
while (($#)); do
  case "$1" in
    --release-dir) release_dir="${2:?}"; shift 2 ;;
    *) exit 1 ;;
  esac
done
[[ -d "$release_dir" ]]

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

copy_release() {
  local destination="$1"
  mkdir "$destination"
  cp -R "$release_dir"/. "$destination"/
}

expect_verify_failure() {
  local directory="$1"
  if go run "$root/cmd/releasebuild" verify --dir "$directory"; then
    printf 'tampered release unexpectedly verified: %s\n' "$directory" >&2
    exit 1
  fi
}

archive_dir="$tmp/archive"
copy_release "$archive_dir"
archive="$(jq -r '.artifacts[] | select(.product == "acr-mcp" and .goos == "linux" and .goarch == "amd64") | .name' "$archive_dir/release-manifest.json")"
[[ -n "$archive" && -f "$archive_dir/$archive" ]]
printf '\nTAMPER\n' >> "$archive_dir/$archive"
expect_verify_failure "$archive_dir"

checksum_dir="$tmp/checksum"
copy_release "$checksum_dir"
archive="$(jq -r '.artifacts[] | select(.product == "acr-api" and .goos == "darwin" and .goarch == "amd64") | .name' "$checksum_dir/release-manifest.json")"
while read -r checksum name; do
  if [[ "$name" == "$archive" ]]; then
    printf '%064d  %s\n' 0 "$name"
  else
    printf '%s  %s\n' "$checksum" "$name"
  fi
done < "$checksum_dir/SHA256SUMS" > "$checksum_dir/SHA256SUMS.tmp"
mv "$checksum_dir/SHA256SUMS.tmp" "$checksum_dir/SHA256SUMS"
expect_verify_failure "$checksum_dir"

sidecar_dir="$tmp/sidecar"
copy_release "$sidecar_dir"
jq '(.artifacts[] | select(.product == "acr-mcp") | .product) = "acr-api"' "$sidecar_dir/release-manifest.json" > "$sidecar_dir/release-manifest.json.tmp"
mv "$sidecar_dir/release-manifest.json.tmp" "$sidecar_dir/release-manifest.json"
expect_verify_failure "$sidecar_dir"
go test "$root/internal/mcpclientfixtures" -run '^TestInstallSidecarSnippetNeverExtractsAfterCosignVerificationFails$' -count=1
printf 'archive, checksum, and incompatible-sidecar tamper gates rejected as expected\n'
