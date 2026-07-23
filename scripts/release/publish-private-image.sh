#!/usr/bin/env bash
set -euo pipefail

repo="full-chaos/dev-health-acr"
registry="ghcr.io"
root="$(cd "$(dirname "$0")/../.." && pwd -P)"
source "$root/scripts/release/approval-receipt.sh"

approval_parse_options "$@" || { printf 'usage: publish-private-image.sh --approval-receipt RECEIPT --digest sha256:DIGEST [--dry-run] PRODUCT TAG RUN_ID\n' >&2; exit 1; }
((${#APPROVAL_ARGS[@]} == 3)) || exit 1
product="${APPROVAL_ARGS[0]}"
tag="${APPROVAL_ARGS[1]}"
run_id="${APPROVAL_ARGS[2]}"
version="${tag#v}"
[[ "$product" == acr-api || "$product" == acr-mcp ]]
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]]
[[ "$run_id" =~ ^[1-9][0-9]*$ ]]
repository="$registry/full-chaos/dev-health-acr/$product"
target="oci-image:${repository}@${APPROVAL_DIGEST}"
approval_verify "$APPROVAL_RECEIPT" publish_private_image "$repo" "$target" "$version" "$APPROVAL_DIGEST" || exit 1
if "$APPROVAL_DRY_RUN"; then
  printf 'dry-run approved: private image publication remains blocked before GHCR access\n'
  exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
install -d -m 700 "$tmp/docker"
authfile="$tmp/docker/config.json"

check_sums() {
  if command -v sha256sum >/dev/null; then
    (cd "$1" && sha256sum --check SHA256SUMS)
  else
    (cd "$1" && shasum -a 256 --check SHA256SUMS)
  fi
}

checksum_stdin() {
  if command -v sha256sum >/dev/null; then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}

checksum_file() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

test "$(gh repo view "$repo" --json nameWithOwner,isPrivate --jq '[.nameWithOwner, .isPrivate] | @tsv')" = "$repo"$'\ttrue'
actor="$(gh api user --jq .login)"
operators="$(gh variable get ACR_RELEASE_OPERATORS --repo "$repo")"
[[ ",$operators," == *",$actor,"* ]]
run="$(gh api "repos/$repo/actions/runs/$run_id" --jq '[.name, .event, .conclusion, .head_branch, .head_sha, (.path | split("@")[0])] | @tsv')"
IFS=$'\t' read -r workflow event conclusion head_tag commit workflow_path <<<"$run"
test "$workflow" = Release
test "$event" = push
test "$conclusion" = success
test "$head_tag" = "$tag"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]]
test "$workflow_path" = .github/workflows/release.yml

gh run download "$run_id" --repo "$repo" --name release --dir "$tmp/release"
check_sums "$tmp/release"
archive="${product}_${version}_linux_multiarch.oci.tar"
manifest="$tmp/release/container-release-manifest.json"
jq -e \
  --arg tag "$tag" \
  --arg version "$version" \
  --arg commit "$commit" \
  --arg product "$product" \
  --arg repository "$repository" \
  --arg archive "$archive" \
  --arg digest "$APPROVAL_DIGEST" '
    .schema_version == "container_release_manifest.v1" and
    .tag == $tag and
    .version == $version and
    .commit == $commit and
    ([.images[] | select(
      .product == $product and
      .repository == $repository and
      .archive == $archive and
      .digest == $digest and
      .platforms == ["linux/amd64", "linux/arm64"]
    )] | length == 1)
  ' "$manifest" >/dev/null
archive_sha256="$(jq -er --arg product "$product" '.images[] | select(.product == $product) | .archive_sha256' "$manifest")"
test "$(checksum_file "$tmp/release/$archive")" = "$archive_sha256"

skopeo --version | awk '$1 == "skopeo" && $2 == "version" && $3 == "1.23.0" { found = 1 } END { exit !found }'
cosign version | awk '$1 == "GitVersion:" && $2 == "v3.1.1" { found = 1 } END { exit !found }'
package="dev-health-acr%2F$product"
test "$(gh api "orgs/full-chaos/packages/container/$package" --jq .visibility)" = private
gh auth token | skopeo login --authfile "$authfile" --username "$actor" --password-stdin "$registry"
skopeo copy --all --preserve-digests \
  --authfile "$authfile" \
  --digestfile "$tmp/remote.digest" \
  "oci-archive:$tmp/release/$archive" \
  "docker://${repository}@${APPROVAL_DIGEST}"
test "$(tr -d '\n' <"$tmp/remote.digest")" = "$APPROVAL_DIGEST"
remote_index="$(skopeo inspect --raw --authfile "$authfile" "docker://${repository}@${APPROVAL_DIGEST}")"
test "sha256:$(printf '%s' "$remote_index" | checksum_stdin)" = "$APPROVAL_DIGEST"
jq -e '[.manifests[] | select(.platform.os == "linux") | (.platform.os + "/" + .platform.architecture)] | sort == ["linux/amd64", "linux/arm64"]' <<<"$remote_index" >/dev/null

test "$(gh api "orgs/full-chaos/packages/container/$package" --jq .visibility)" = private
if skopeo inspect --no-creds "docker://${repository}@${APPROVAL_DIGEST}" >/dev/null 2>&1; then
  printf 'GHCR image is anonymously readable: %s@%s\n' "$repository" "$APPROVAL_DIGEST" >&2
  exit 1
fi

key_dir="${HOME}/.config/acr/release"
test -r "$key_dir/cosign.key"
test -r "$root/signing/cosign.pub"
if [[ -z "${COSIGN_PASSWORD:-}" ]]; then
  if command -v security >/dev/null && COSIGN_PASSWORD="$(security find-generic-password -a "$USER" -s acr-release-cosign -w 2>/dev/null)"; then
    :
  else
    read -r -s -p 'Cosign key password: ' COSIGN_PASSWORD
    printf '\n' >&2
  fi
fi
digest_reference="${repository}@${APPROVAL_DIGEST}"
COSIGN_PASSWORD="$COSIGN_PASSWORD" DOCKER_CONFIG="$tmp/docker" \
  cosign sign --yes --key "$key_dir/cosign.key" --use-signing-config=false --tlog-upload=false "$digest_reference"
DOCKER_CONFIG="$tmp/docker" \
  cosign verify --key "$root/signing/cosign.pub" --insecure-ignore-tlog "$digest_reference"
unset COSIGN_PASSWORD
printf 'published private image: %s@%s (release %s)\n' "$repository" "$APPROVAL_DIGEST" "$tag"
