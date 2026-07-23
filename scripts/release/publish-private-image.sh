#!/usr/bin/env bash
set -euo pipefail

repo="full-chaos/dev-health-acr"
registry="ghcr.io"
fingerprint="9DCD0E7D385C8247E2F5E7FC2C43EBC02D8C8781"
root="$(cd "$(dirname "$0")/../.." && pwd -P)"

[[ $# -eq 5 && "$1" == --digest ]] || { printf 'usage: publish-private-image.sh --digest sha256:DIGEST PRODUCT TAG RUN_ID\n' >&2; exit 1; }
image_digest="$2"
product="$3"
tag="$4"
run_id="$5"
version="${tag#v}"
[[ "$image_digest" =~ ^sha256:[0-9a-f]{64}$ ]]
[[ "$product" == acr-api || "$product" == acr-mcp ]]
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]]
[[ "$run_id" =~ ^[1-9][0-9]*$ ]]
repository="$registry/full-chaos/dev-health-acr/$product"

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

gh repo clone "$repo" "$tmp/repo" -- --no-checkout
git -C "$tmp/repo" fetch --tags origin main >/dev/null
gnupg="$tmp/gnupg"
install -d -m 700 "$gnupg"
GNUPGHOME="$gnupg" gpg --batch --import "$root/signing/release-tag-signing-key.asc" >/dev/null
test "$(GNUPGHOME="$gnupg" gpg --batch --with-colons --fingerprint "$fingerprint" | awk -F: '$1 == "fpr" {print $10; exit}')" = "$fingerprint"
GNUPGHOME="$gnupg" git -C "$tmp/repo" verify-tag --raw "$tag" 2>&1 | grep -F "[GNUPG:] VALIDSIG $fingerprint "
test "$(git -C "$tmp/repo" cat-file -t "refs/tags/$tag")" = tag
tag_object="$(git -C "$tmp/repo" rev-parse "refs/tags/$tag")"
tag_commit="$(git -C "$tmp/repo" rev-list -n1 "$tag")"
test "$(git -C "$tmp/repo" rev-parse "$tag^{}")" = "$tag_commit"
test "$tag_commit" = "$commit"
git -C "$tmp/repo" merge-base --is-ancestor "$tag_commit" origin/main
refetch_tag() {
  local remote_refs remote_tag remote_commit
  remote_refs="$(git -C "$tmp/repo" ls-remote origin "refs/tags/$tag" "refs/tags/$tag^{}")"
  remote_tag="$(printf '%s\n' "$remote_refs" | awk -v ref="refs/tags/$tag" '$2 == ref { print $1 }')"
  remote_commit="$(printf '%s\n' "$remote_refs" | awk -v ref="refs/tags/$tag^{}" '$2 == ref { print $1 }')"
  test "$remote_tag" = "$tag_object"
  test "$remote_commit" = "$tag_commit"
}

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
  --arg digest "$image_digest" '
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
refetch_tag
skopeo copy --all --preserve-digests \
  --authfile "$authfile" \
  --digestfile "$tmp/remote.digest" \
  "oci-archive:$tmp/release/$archive" \
  "docker://${repository}@${image_digest}"
test "$(tr -d '\n' <"$tmp/remote.digest")" = "$image_digest"
remote_index="$(skopeo inspect --raw --authfile "$authfile" "docker://${repository}@${image_digest}")"
test "sha256:$(printf '%s' "$remote_index" | checksum_stdin)" = "$image_digest"
jq -e '[.manifests[] | select(.platform.os == "linux") | (.platform.os + "/" + .platform.architecture)] | sort == ["linux/amd64", "linux/arm64"]' <<<"$remote_index" >/dev/null

test "$(gh api "orgs/full-chaos/packages/container/$package" --jq .visibility)" = private
if skopeo inspect --no-creds "docker://${repository}@${image_digest}" >/dev/null 2>&1; then
  printf 'GHCR image is anonymously readable: %s@%s\n' "$repository" "$image_digest" >&2
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
digest_reference="${repository}@${image_digest}"
COSIGN_PASSWORD="$COSIGN_PASSWORD" DOCKER_CONFIG="$tmp/docker" \
  cosign sign --yes --key "$key_dir/cosign.key" --use-signing-config=false --tlog-upload=false "$digest_reference"
DOCKER_CONFIG="$tmp/docker" \
  cosign verify --key "$root/signing/cosign.pub" --insecure-ignore-tlog "$digest_reference"
unset COSIGN_PASSWORD
printf 'published private image: %s@%s (release %s)\n' "$repository" "$image_digest" "$tag"
