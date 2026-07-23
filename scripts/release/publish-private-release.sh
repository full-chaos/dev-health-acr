#!/usr/bin/env bash
set -euo pipefail

repo="full-chaos/dev-health-acr"
fingerprint="9DCD0E7D385C8247E2F5E7FC2C43EBC02D8C8781"
root="$(cd "$(dirname "$0")/../.." && pwd -P)"
source "$root/scripts/release/approval-receipt.sh"
approval_parse_options "$@" || { printf 'usage: publish-private-release.sh --approval-receipt RECEIPT --digest sha256:DIGEST [--dry-run] TAG RUN_ID\n' >&2; exit 1; }
((${#APPROVAL_ARGS[@]} == 2)) || exit 1
tag="${APPROVAL_ARGS[0]}"
run_id="${APPROVAL_ARGS[1]}"
version="${tag#v}"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]] || exit 1
approval_verify "$APPROVAL_RECEIPT" publish_private_release "$repo" "github-release:$repo:$tag" "$version" "$APPROVAL_DIGEST" || exit 1
if "$APPROVAL_DRY_RUN"; then
  printf 'dry-run approved: private release publication remains blocked before GitHub access\n'
  exit 0
fi
tmp="$(mktemp -d)"
draft_created=false
cleanup() {
  local release_state
  if "$draft_created"; then
    release_state="$(gh release view "$tag" --repo "$repo" --json isDraft --jq .isDraft 2>/dev/null || true)"
    if [[ "$release_state" == true ]]; then
      gh release delete "$tag" --repo "$repo" --yes >/dev/null 2>&1 || true
    elif [[ "$release_state" != false ]]; then
      printf 'leaving Release untouched because its draft state could not be verified: %s\n' "$tag" >&2
    fi
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

check_sums() {
  if command -v sha256sum >/dev/null; then
    sha256sum --check "$1"
  else
    shasum -a 256 --check "$1"
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
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]]

gh repo clone "$repo" "$tmp/repo" -- --no-checkout
git -C "$tmp/repo" fetch --tags origin main >/dev/null
GNUPGHOME="$tmp/gnupg"; export GNUPGHOME; install -d -m 700 "$GNUPGHOME"
gpg --batch --import "$root/signing/release-tag-signing-key.asc" >/dev/null
test "$(gpg --batch --with-colons --fingerprint "$fingerprint" | awk -F: '$1 == "fpr" {print $10; exit}')" = "$fingerprint"
git -C "$tmp/repo" verify-tag --raw "$tag" 2>&1 | grep -F "[GNUPG:] VALIDSIG $fingerprint "
test "$(git -C "$tmp/repo" cat-file -t "refs/tags/$tag")" = tag
tag_object="$(git -C "$tmp/repo" rev-parse "refs/tags/$tag")"
commit="$(git -C "$tmp/repo" rev-list -n1 "$tag")"
test "$(git -C "$tmp/repo" rev-parse "$tag^{}")" = "$commit"
git -C "$tmp/repo" merge-base --is-ancestor "$commit" origin/main
test "$(gh api "repos/$repo/actions/runs/$run_id" --jq '[.name, .event, .conclusion, .head_branch, .head_sha, (.path | split("@")[0])] | @tsv')" = "Release	push	success	$tag	$commit	.github/workflows/release.yml"
gh run download "$run_id" --repo "$repo" --name release --dir "$tmp/release"
cd "$tmp/release"
jq -e --arg version "$version" --arg commit "$commit" '.schema_version == "release_manifest.v1" and .version == $version and .commit == $commit and (.artifacts | length == 10)' release-manifest.json >/dev/null
jq -e --arg tag "$tag" --arg version "$version" --arg commit "$commit" '
  .schema_version == "container_release_manifest.v1" and
  .tag == $tag and
  .version == $version and
  .commit == $commit and
  ([.images[].product] | sort == ["acr-api", "acr-mcp"]) and
  all(.images[];
    . as $image |
    $image.repository == ("ghcr.io/full-chaos/dev-health-acr/" + $image.product) and
    ($image.archive_sha256 | test("^[0-9a-f]{64}$")) and
    ($image.digest | test("^sha256:[0-9a-f]{64}$")) and
    $image.platforms == ["linux/amd64", "linux/arm64"])
' container-release-manifest.json >/dev/null
jq -r '.artifacts[] | "\(.sha256)  \(.name)"' release-manifest.json > "$tmp/builder-SHA256SUMS"
check_sums "$tmp/builder-SHA256SUMS"
check_sums SHA256SUMS
test "sha256:$(checksum_file SHA256SUMS)" = "$APPROVAL_DIGEST"
skopeo --version | awk '$1 == "skopeo" && $2 == "version" && $3 == "1.23.0" { found = 1 } END { exit !found }'
cosign version | awk '$1 == "GitVersion:" && $2 == "v3.1.1" { found = 1 } END { exit !found }'
key_dir="${HOME}/.config/acr/release"
test -r "$key_dir/cosign.key"; test -r "$root/signing/cosign.pub"
if command -v security >/dev/null && COSIGN_PASSWORD="$(security find-generic-password -a "$USER" -s acr-release-cosign -w 2>/dev/null)"; then :; else read -r -s -p 'Cosign key password: ' COSIGN_PASSWORD; printf '\n' >&2; fi
COSIGN_PASSWORD="$COSIGN_PASSWORD" cosign sign-blob --yes --key "$key_dir/cosign.key" --output-signature SHA256SUMS.sig --use-signing-config=false --new-bundle-format=false --tlog-upload=false SHA256SUMS
cosign verify-blob --key "$root/signing/cosign.pub" --signature SHA256SUMS.sig --insecure-ignore-tlog SHA256SUMS
unset COSIGN_PASSWORD
assets=(SHA256SUMS SHA256SUMS.sig)
while IFS=' ' read -r _ name; do
  test -n "$name"
  test -f "$name"
  assets+=("$name")
done < SHA256SUMS
for file in ./*; do
  allowed=false
  for asset in "${assets[@]}"; do [[ "$(basename "$file")" == "$asset" ]] && allowed=true; done
  "$allowed" || { printf 'unexpected release file: %s\n' "$file" >&2; exit 1; }
done
install -d -m 700 "$tmp/docker"
authfile="$tmp/docker/config.json"
gh auth token | skopeo login --authfile "$authfile" --username "$actor" --password-stdin ghcr.io
while IFS=$'\t' read -r product repository image_digest; do
  remote_index="$(skopeo inspect --raw --authfile "$authfile" "docker://${repository}@${image_digest}")"
  test "sha256:$(printf '%s' "$remote_index" | checksum_stdin)" = "$image_digest"
  jq -e '[.manifests[] | select(.platform.os == "linux") | (.platform.os + "/" + .platform.architecture)] | sort == ["linux/amd64", "linux/arm64"]' <<<"$remote_index" >/dev/null
  test "$(gh api "orgs/full-chaos/packages/container/dev-health-acr%2F$product" --jq .visibility)" = private
  if skopeo inspect --no-creds "docker://${repository}@${image_digest}" >/dev/null 2>&1; then
    printf 'GHCR image is anonymously readable: %s@%s\n' "$repository" "$image_digest" >&2
    exit 1
  fi
  DOCKER_CONFIG="$tmp/docker" cosign verify --key "$root/signing/cosign.pub" --insecure-ignore-tlog "${repository}@${image_digest}"
done < <(jq -r '.images[] | [.product, .repository, .digest] | @tsv' container-release-manifest.json)
refetch() {
  git -C "$tmp/repo" verify-tag --raw "$tag" 2>&1 | grep -F "[GNUPG:] VALIDSIG $fingerprint "
  test "$(git -C "$tmp/repo" cat-file -t "refs/tags/$tag")" = tag
  test "$(git -C "$tmp/repo" rev-parse "refs/tags/$tag")" = "$tag_object"
  test "$(git -C "$tmp/repo" rev-parse "$tag^{}")" = "$commit"
  remote_refs="$(git -C "$tmp/repo" ls-remote origin "refs/tags/$tag" "refs/tags/$tag^{}")"
  remote_tag="$(printf '%s\n' "$remote_refs" | awk -v ref="refs/tags/$tag" '$2 == ref { print $1 }')"
  remote_commit="$(printf '%s\n' "$remote_refs" | awk -v ref="refs/tags/$tag^{}" '$2 == ref { print $1 }')"
  test "$remote_tag" = "$tag_object"
  test "$remote_commit" = "$commit"
}
if gh release view "$tag" --repo "$repo" >/dev/null 2>&1; then
  printf 'refusing to replace existing GitHub Release: %s\n' "$tag" >&2
  exit 1
fi
distribution_notes="$(jq -r --arg tag "$tag" '
  "## Private distribution\n\n" +
  "Verify `SHA256SUMS.sig` and the targeted asset checksum before extraction. " +
  "Deploy containers only by the signed digest references below.\n\n" +
  ([.images[] | "- `" + .repository + "@" + .digest + "`"] | join("\n"))
' container-release-manifest.json)"
args=(release create "$tag" --repo "$repo" --verify-tag --draft --title "ACR $tag" --generate-notes --notes "$distribution_notes")
[[ "$tag" == *-dev.* || "$tag" == *-beta.* ]] && args+=(--prerelease)
refetch
gh "${args[@]}"
draft_created=true
refetch
gh release upload "$tag" --repo "$repo" "${assets[@]}"
refetch
mkdir "$tmp/draft"
gh release download "$tag" --repo "$repo" --dir "$tmp/draft"
cd "$tmp/draft"; cosign verify-blob --key "$root/signing/cosign.pub" --signature SHA256SUMS.sig --insecure-ignore-tlog SHA256SUMS; check_sums SHA256SUMS
refetch
# The verified draft becomes operator-owned before publication. If the publish
# response is ambiguous, cleanup must leave either state intact for inspection.
draft_created=false
set +e
if [[ "$tag" == *-dev.* || "$tag" == *-beta.* ]]; then
  gh release edit "$tag" --repo "$repo" --draft=false --prerelease --latest=false
else
  gh release edit "$tag" --repo "$repo" --draft=false --latest
fi
edit_status=$?
set -e
release_state="$(gh release view "$tag" --repo "$repo" --json isDraft --jq .isDraft 2>/dev/null || true)"
if [[ "$release_state" == false ]]; then
  refetch
elif ((edit_status != 0)); then
  printf 'GitHub Release publication failed and final state is not published: %s\n' "$tag" >&2
  exit "$edit_status"
else
  printf 'cannot verify published GitHub Release state: %s\n' "$tag" >&2
  exit 1
fi
