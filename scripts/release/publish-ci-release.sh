#!/usr/bin/env bash
set -euo pipefail

expected_repo="full-chaos/dev-health-acr"
release_dir=""
tag=""
commit=""

while (($#)); do
  case "$1" in
    --dir) release_dir="${2:?}"; shift 2 ;;
    --tag) tag="${2:?}"; shift 2 ;;
    --commit) commit="${2:?}"; shift 2 ;;
    *) printf 'unsupported argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

fail() {
  printf 'release publication: %s\n' "$*" >&2
  exit 1
}

[[ -n "$release_dir" && -d "$release_dir" ]] || fail "--dir must name the assembled release directory"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]] \
  || fail "--tag must be a canonical ACR release tag"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || fail "--commit must be a full Git commit SHA"
[[ "${GITHUB_REPOSITORY:-}" == "$expected_repo" ]] || fail "unexpected repository: ${GITHUB_REPOSITORY:-unset}"
[[ -n "${GITHUB_ACTOR:-}" ]] || fail "GITHUB_ACTOR is required"
[[ -n "${GH_TOKEN:-}" ]] || fail "GH_TOKEN is required"

for command in cosign gh jq skopeo; do
  command -v "$command" >/dev/null 2>&1 || fail "required command is unavailable: $command"
done
skopeo --version | grep -F '1.23.0' >/dev/null \
  || fail "Skopeo 1.23.0 is required"
cosign version | grep -F 'GitVersion:' | grep -F 'v3.0.6' >/dev/null \
  || fail "Cosign v3.0.6 is required"

release_dir="$(cd "$release_dir" && pwd -P)"
version="${tag#v}"
issuer="https://token.actions.githubusercontent.com"
identity_regexp='^https://github\.com/full-chaos/dev-health-acr/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-(dev|beta)\.[0-9]+)?)$'
bundle_name="SHA256SUMS.sigstore.json"
bundle="$release_dir/$bundle_name"
tmp="$(mktemp -d)"
draft_created=false

cleanup() {
  local release_state
  if "$draft_created"; then
    release_state="$(gh release view "$tag" --repo "$expected_repo" --json isDraft --jq .isDraft 2>/dev/null || true)"
    if [[ "$release_state" == true ]]; then
      gh release delete "$tag" --repo "$expected_repo" --yes >/dev/null 2>&1 || true
    elif [[ "$release_state" != false ]]; then
      printf 'leaving Release untouched because its draft state could not be verified: %s\n' "$tag" >&2
    fi
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

checksum_file() {
  sha256sum "$1" | awk '{print $1}'
}

checksum_stdin() {
  sha256sum | awk '{print $1}'
}

check_sums() {
  local dir="$1"
  (cd "$dir" && sha256sum --check SHA256SUMS)
}

verify_downloaded_release() {
  local dir="$1"
  local label="$2"
  local expected="$tmp/${label}-expected-assets"
  local actual="$tmp/${label}-actual-assets"

  test -f "$dir/SHA256SUMS" || fail "$label is missing SHA256SUMS"
  test -f "$dir/$bundle_name" || fail "$label is missing $bundle_name"
  cosign verify-blob "$dir/SHA256SUMS" \
    --bundle "$dir/$bundle_name" \
    --certificate-identity-regexp "$identity_regexp" \
    --certificate-oidc-issuer "$issuer" >/dev/null
  check_sums "$dir"
  {
    printf '%s\n' SHA256SUMS "$bundle_name"
    awk '{print $2}' "$dir/SHA256SUMS"
  } | LC_ALL=C sort -u >"$expected"
  find "$dir" -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort >"$actual"
  cmp "$expected" "$actual" >/dev/null \
    || fail "$label contains a missing or unexpected asset"
}

manifest_digest() {
  local reference="$1"
  local raw
  raw="$(skopeo inspect --raw --authfile "$authfile" "docker://$reference")" || return 1
  printf 'sha256:%s\n' "$(printf '%s' "$raw" | checksum_stdin)"
}

test -f "$release_dir/SHA256SUMS" || fail "SHA256SUMS is missing"
test -f "$release_dir/release-manifest.json" || fail "release-manifest.json is missing"
test -f "$release_dir/container-release-manifest.json" || fail "container-release-manifest.json is missing"
check_sums "$release_dir"

jq -e --arg version "$version" --arg commit "$commit" '
  .schema_version == "release_manifest.v1" and
  .version == $version and
  .commit == $commit and
  ([.artifacts[].name] | sort) == ([
    "acr-api_" + $version + "_linux_amd64.tar.gz",
    "acr-api_" + $version + "_linux_arm64.tar.gz",
    "acr-api_" + $version + "_darwin_amd64.tar.gz",
    "acr-api_" + $version + "_darwin_arm64.tar.gz",
    "acr-api_" + $version + "_windows_amd64.zip",
    "acr-mcp_" + $version + "_linux_amd64.tar.gz",
    "acr-mcp_" + $version + "_linux_arm64.tar.gz",
    "acr-mcp_" + $version + "_darwin_amd64.tar.gz",
    "acr-mcp_" + $version + "_darwin_arm64.tar.gz",
    "acr-mcp_" + $version + "_windows_amd64.zip"
  ] | sort) and
  all(.artifacts[]; (.sha256 | test("^[0-9a-f]{64}$")))
' "$release_dir/release-manifest.json" >/dev/null \
  || fail "binary release manifest does not match the requested release"
jq -r '.artifacts[] | "\(.sha256)  \(.name)"' \
  "$release_dir/release-manifest.json" >"$tmp/binary-manifest-SHA256SUMS"
(cd "$release_dir" && sha256sum --check "$tmp/binary-manifest-SHA256SUMS") \
  || fail "binary release manifest checksums do not match the archives"

jq -e --arg tag "$tag" --arg version "$version" --arg commit "$commit" '
  .schema_version == "container_release_manifest.v1" and
  .tag == $tag and
  .version == $version and
  .commit == $commit and
  ([.images[].product] | sort == ["acr-api", "acr-mcp"]) and
  all(.images[];
    . as $image |
    $image.repository == ("ghcr.io/full-chaos/dev-health-acr/" + $image.product) and
    $image.archive == ($image.product + "_" + $version + "_linux_multiarch.oci.tar") and
    ($image.archive_sha256 | test("^[0-9a-f]{64}$")) and
    ($image.digest | test("^sha256:[0-9a-f]{64}$")) and
    $image.platforms == ["linux/amd64", "linux/arm64"])
' "$release_dir/container-release-manifest.json" >/dev/null \
  || fail "container release manifest does not match the requested release"

install -d -m 700 "$tmp/docker"
authfile="$tmp/docker/config.json"
export DOCKER_CONFIG="$tmp/docker"
printf '%s' "$GH_TOKEN" \
  | skopeo login --authfile "$authfile" --username "$GITHUB_ACTOR" --password-stdin ghcr.io >/dev/null

while IFS=$'\t' read -r product repository archive image_digest archive_sha256; do
  test -f "$release_dir/$archive" || fail "container archive is missing: $archive"
  test "$(checksum_file "$release_dir/$archive")" = "$archive_sha256" \
    || fail "container archive checksum mismatch: $archive"

  tag_reference="${repository}:${tag}"
  digest_reference="${repository}@${image_digest}"
  existing_raw="$tmp/$product-existing-manifest.json"
  existing_error="$tmp/$product-existing-manifest.err"
  if skopeo inspect --raw --authfile "$authfile" "docker://$tag_reference" \
    >"$existing_raw" 2>"$existing_error"; then
    existing_digest="sha256:$(checksum_file "$existing_raw")"
    test "$existing_digest" = "$image_digest" \
      || fail "immutable GHCR tag conflict: $tag_reference points to $existing_digest, expected $image_digest"
  else
    if ! grep -Eqi '(manifest unknown|name unknown|not found|HTTP[^0-9]*404|status[^0-9]*404)' "$existing_error"; then
      cat "$existing_error" >&2
      fail "cannot determine whether GHCR tag exists: $tag_reference"
    fi
    skopeo copy --all --preserve-digests \
      --authfile "$authfile" \
      --digestfile "$tmp/$product.digest" \
      "oci-archive:$release_dir/$archive" \
      "docker://$tag_reference"
    test "$(tr -d '\n' <"$tmp/$product.digest")" = "$image_digest" \
      || fail "GHCR reported the wrong digest for $tag_reference"
  fi

  test "$(manifest_digest "$tag_reference")" = "$image_digest" \
    || fail "published GHCR tag does not resolve to the verified image: $tag_reference"
  test "$(manifest_digest "$digest_reference")" = "$image_digest" \
    || fail "published GHCR digest does not match the verified image: $digest_reference"

  if ! cosign verify \
    --certificate-identity-regexp "$identity_regexp" \
    --certificate-oidc-issuer "$issuer" \
    "$digest_reference" >/dev/null 2>&1; then
    cosign sign --yes "$digest_reference"
  fi
  cosign verify \
    --certificate-identity-regexp "$identity_regexp" \
    --certificate-oidc-issuer "$issuer" \
    "$digest_reference" >/dev/null
done < <(
  jq -r '.images[] | [.product, .repository, .archive, .digest, .archive_sha256] | @tsv' \
    "$release_dir/container-release-manifest.json"
)

release_lookup_error="$tmp/release-lookup.err"
if release_json="$(gh api "repos/$expected_repo/releases/tags/$tag" 2>"$release_lookup_error")"; then
  test "$(jq -r .tag_name <<<"$release_json")" = "$tag" \
    || fail "existing GitHub Release has the wrong tag"
  test "$(jq -r .draft <<<"$release_json")" = false \
    || fail "an existing draft GitHub Release requires manual review: $tag"

  mkdir "$tmp/existing-release"
  gh release download "$tag" --repo "$expected_repo" --dir "$tmp/existing-release"
  verify_downloaded_release "$tmp/existing-release" existing-release
  cmp "$release_dir/SHA256SUMS" "$tmp/existing-release/SHA256SUMS" >/dev/null \
    || fail "existing GitHub Release assets differ from the verified build"
  cp "$tmp/existing-release/$bundle_name" "$bundle"
  printf 'release already published and verified: %s\n' "$tag"
  exit 0
elif ! grep -Eqi '(Not Found|HTTP 404)' "$release_lookup_error"; then
  cat "$release_lookup_error" >&2
  fail "cannot determine whether GitHub Release exists: $tag"
fi

rm -f "$bundle"
cosign sign-blob "$release_dir/SHA256SUMS" --bundle "$bundle" --yes
cosign verify-blob "$release_dir/SHA256SUMS" \
  --bundle "$bundle" \
  --certificate-identity-regexp "$identity_regexp" \
  --certificate-oidc-issuer "$issuer" >/dev/null

assets=("$release_dir/SHA256SUMS" "$bundle")
while IFS=' ' read -r _ name; do
  test -n "$name"
  test -f "$release_dir/$name"
  assets+=("$release_dir/$name")
done <"$release_dir/SHA256SUMS"

notes="$tmp/release-notes.md"
{
  printf '## Published artifacts\n\n'
  printf 'This release contains deterministic `acr-api` and `acr-mcp` binaries for Linux, macOS, and Windows, plus verified multi-platform OCI archives and SPDX SBOMs.\n\n'
  printf 'Versioned container images:\n\n'
  jq -r --arg tag "$tag" '.images[] | "- `" + .repository + ":" + $tag + "` (`" + .repository + "@" + .digest + "`)"' \
    "$release_dir/container-release-manifest.json"
  printf '\nVerify `%s` before extracting a binary archive. Deploy containers by digest when immutability is required.\n' "$bundle_name"
} >"$notes"

release_args=(
  release create "$tag"
  --repo "$expected_repo"
  --verify-tag
  --draft
  --title "ACR $tag"
  --generate-notes
  --notes-file "$notes"
)
if [[ "$tag" == *-dev.* || "$tag" == *-beta.* ]]; then
  release_args+=(--prerelease)
fi
gh "${release_args[@]}"
draft_created=true
gh release upload "$tag" --repo "$expected_repo" "${assets[@]}"

mkdir "$tmp/draft-release"
gh release download "$tag" --repo "$expected_repo" --dir "$tmp/draft-release"
verify_downloaded_release "$tmp/draft-release" draft-release
cmp "$release_dir/SHA256SUMS" "$tmp/draft-release/SHA256SUMS" >/dev/null \
  || fail "downloaded draft assets differ from the verified build"

if [[ "$tag" == *-dev.* || "$tag" == *-beta.* ]]; then
  gh release edit "$tag" --repo "$expected_repo" --draft=false --prerelease --latest=false
else
  gh release edit "$tag" --repo "$expected_repo" --draft=false --prerelease=false --latest
fi
test "$(gh release view "$tag" --repo "$expected_repo" --json isDraft --jq .isDraft)" = false \
  || fail "GitHub Release did not reach the published state"
draft_created=false

printf 'published GitHub Release and GHCR images: %s\n' "$tag"
