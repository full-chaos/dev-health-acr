#!/usr/bin/env bash
set -euo pipefail

tag="${1:?usage: publish-private-release.sh TAG RUN_ID}"
run_id="${2:?usage: publish-private-release.sh TAG RUN_ID}"
repo="full-chaos/dev-health-acr"
fingerprint="9DCD0E7D385C8247E2F5E7FC2C43EBC02D8C8781"
root="$(git rev-parse --show-toplevel)"
tmp="$(mktemp -d)"
draft_created=false
cleanup() {
  if "$draft_created"; then gh release delete "$tag" --repo "$repo" --yes >/dev/null 2>&1 || true; fi
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

test "$(gh repo view "$repo" --json nameWithOwner --jq .nameWithOwner)" = "$repo"
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
version="${tag#v}"
jq -e --arg version "$version" --arg commit "$commit" '.schema_version == "release_manifest.v1" and .version == $version and .commit == $commit and (.artifacts | length == 10)' release-manifest.json >/dev/null
jq -r '.artifacts[] | "\(.sha256)  \(.name)"' release-manifest.json > "$tmp/builder-SHA256SUMS"
check_sums "$tmp/builder-SHA256SUMS"
check_sums SHA256SUMS
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
refetch() {
  git -C "$tmp/repo" verify-tag --raw "$tag" 2>&1 | grep -F "[GNUPG:] VALIDSIG $fingerprint "
  test "$(git -C "$tmp/repo" cat-file -t "refs/tags/$tag")" = tag
  test "$(git -C "$tmp/repo" rev-parse "refs/tags/$tag")" = "$tag_object"
  test "$(git -C "$tmp/repo" rev-parse "$tag^{}")" = "$commit"
  remote_refs="$(git -C "$tmp/repo" ls-remote origin "refs/tags/$tag")"
  remote_tag="$(printf '%s\n' "$remote_refs" | awk -v ref="refs/tags/$tag" '$2 == ref { print $1 }')"
  remote_commit="$(printf '%s\n' "$remote_refs" | awk -v ref="refs/tags/$tag^{}" '$2 == ref { print $1 }')"
  test "$remote_tag" = "$tag_object"
  test "$remote_commit" = "$commit"
}
refetch
args=(release create "$tag" --repo "$repo" --verify-tag --draft --title "ACR $tag" --generate-notes)
[[ "$tag" == *-dev.* || "$tag" == *-beta.* ]] && args+=(--prerelease)
gh "${args[@]}"
draft_created=true
refetch
gh release upload "$tag" --repo "$repo" "${assets[@]}"
refetch
mkdir "$tmp/draft"
gh release download "$tag" --repo "$repo" --dir "$tmp/draft"
cd "$tmp/draft"; cosign verify-blob --key "$root/signing/cosign.pub" --signature SHA256SUMS.sig --insecure-ignore-tlog SHA256SUMS; check_sums SHA256SUMS
refetch
if [[ "$tag" == *-dev.* || "$tag" == *-beta.* ]]; then gh release edit "$tag" --repo "$repo" --draft=false --prerelease --latest=false; else gh release edit "$tag" --repo "$repo" --draft=false --latest; fi
draft_created=false
