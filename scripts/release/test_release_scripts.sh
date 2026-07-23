#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/setup/signing"

cat > "$tmp/bin/cosign" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MOCK_LOG"
if [[ "$1" == version ]]; then printf '%s\n' "$MOCK_COSIGN_VERSION"; exit 0; fi
exit 1
EOF
cat > "$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MOCK_LOG"
case "$1 $2" in
  'api user') printf 'chrisgeo\n' ;;
  'variable get') printf 'chrisgeo\n' ;;
  'repo view') printf 'full-chaos/dev-health-acr\n' ;;
  'release view') if [[ "${MOCK_RELEASE_VIEW_FAIL:-}" == true ]]; then exit 1; fi; printf 'first-asset\nsecond-asset\n' ;;
esac
EOF
cat > "$tmp/bin/git" <<'EOF'
#!/usr/bin/env bash
if [[ "$1 $2 $3" == 'rev-parse --show-toplevel' ]]; then printf '%s\n' "$MOCK_ROOT"; fi
EOF
chmod 755 "$tmp/bin"/*

touch "$tmp/setup/signing/cosign.pub"
if (cd "$tmp/setup" && PATH="$tmp/bin:$PATH" HOME="$tmp/home" COSIGN_PASSWORD=test MOCK_LOG="$tmp/setup.log" "$root/scripts/release/setup-cosign-key.sh"); then exit 1; fi
test ! -e "$tmp/setup.log"

mkdir -p "$tmp/version"
if (cd "$tmp/version" && PATH="$tmp/bin:$PATH" HOME="$tmp/home" COSIGN_PASSWORD=test MOCK_LOG="$tmp/version.log" MOCK_COSIGN_VERSION='GitVersion:    v3.1.1' "$root/scripts/release/setup-cosign-key.sh"); then exit 1; fi
test ! -e "$tmp/home/.config/acr/release/cosign.key"
grep -Fx 'version' "$tmp/version.log" >/dev/null

mkdir -p "$tmp/wrong-version"
if (cd "$tmp/wrong-version" && PATH="$tmp/bin:$PATH" HOME="$tmp/wrong-home" COSIGN_PASSWORD=test MOCK_LOG="$tmp/wrong.log" MOCK_COSIGN_VERSION='GitVersion:    v3.1.0' "$root/scripts/release/setup-cosign-key.sh"); then exit 1; fi
test ! -e "$tmp/wrong-home/.config/acr/release/cosign.key"
test "$(wc -l < "$tmp/wrong.log")" -eq 1

if PATH="$tmp/bin:$PATH" MOCK_LOG="$tmp/revoke.log" "$root/scripts/release/revoke-private-release.sh" v1.2.3 INCIDENT-1; then exit 1; fi
test ! -e "$tmp/revoke.log"

if PATH="$tmp/bin:$PATH" MOCK_LOG="$tmp/publish.log" MOCK_ROOT="$root" "$root/scripts/release/publish-private-release.sh" not-a-tag 1; then exit 1; fi
test ! -e "$tmp/publish.log"

tag_remote="$tmp/tag-remote.git"
tag_source="$tmp/tag-source"
tag_name="v1.2.3-dev.1"
git init --bare "$tag_remote" >/dev/null
git init "$tag_source" >/dev/null
git -C "$tag_source" config user.name release-test
git -C "$tag_source" config user.email release-test@example.invalid
git -C "$tag_source" commit --allow-empty -m fixture >/dev/null
git -C "$tag_source" tag -a "$tag_name" -m fixture
git -C "$tag_source" remote add origin "$tag_remote"
git -C "$tag_source" push origin HEAD:main "refs/tags/$tag_name" >/dev/null
tag_object="$(git -C "$tag_source" rev-parse "refs/tags/$tag_name")"
tag_commit="$(git -C "$tag_source" rev-parse "$tag_name^{}")"
single_refs="$(git ls-remote "$tag_remote" "refs/tags/$tag_name")"
single_commit="$(printf '%s\n' "$single_refs" | awk -v ref="refs/tags/$tag_name^{}" '$2 == ref { print $1 }')"
test -z "$single_commit"
tag_refs="$(git ls-remote "$tag_remote" "refs/tags/$tag_name" "refs/tags/$tag_name^{}")"
remote_object="$(printf '%s\n' "$tag_refs" | awk -v ref="refs/tags/$tag_name" '$2 == ref { print $1 }')"
remote_commit="$(printf '%s\n' "$tag_refs" | awk -v ref="refs/tags/$tag_name^{}" '$2 == ref { print $1 }')"
test "$remote_object" = "$tag_object"
test "$remote_commit" = "$tag_commit"
grep -F "ls-remote origin \"refs/tags/\$tag\" \"refs/tags/\$tag^{}\"" "$root/scripts/release/publish-private-release.sh" >/dev/null

if grep -E 'gh release upload.*cosign\.pub' "$root/scripts/release/publish-private-release.sh"; then exit 1; fi
grep -E 'awk -v name=.*[$]2 == name' "$root/docs/release-policy.md" >/dev/null
if grep -E 'grep -F.*archive.*SHA256SUMS' "$root/docs/release-policy.md"; then exit 1; fi
grep -F 'set -euo pipefail' "$root/docs/release-policy.md" >/dev/null
grep -F "\$ErrorActionPreference = 'Stop'" "$root/docs/release-policy.md" >/dev/null
if grep -E 'gh release upload.*\.\/\*' "$root/scripts/release/publish-private-release.sh"; then exit 1; fi
grep -F 'assets=(SHA256SUMS SHA256SUMS.sig)' "$root/scripts/release/publish-private-release.sh" >/dev/null
grep -F 'gh release delete-asset' "$root/scripts/release/revoke-private-release.sh" >/dev/null
if grep -F 'remote get-url origin' "$root/scripts/release/publish-private-release.sh"; then exit 1; fi
grep -E 'gh repo clone.*--no-checkout' "$root/scripts/release/publish-private-release.sh" >/dev/null

workflow_go_version() {
  local line value
  local -a versions=()

  test -r "$1" || return 1
  while IFS= read -r line; do
    if [[ "$line" =~ ^[[:space:]]*go-version:[[:space:]]*(.+)[[:space:]]*$ ]]; then
      value="${BASH_REMATCH[1]}"
      value="${value#"${value%%[![:space:]]*}"}"
      value="${value%"${value##*[![:space:]]}"}"
      case "$value" in
        \"*\") value="${value#\"}"; value="${value%\"}" ;;
        \'*\') value="${value#\'}"; value="${value%\'}" ;;
      esac
      [[ "$value" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
      versions+=("$value")
    elif [[ "$line" =~ ^[[:space:]]*go-version: ]]; then
      return 1
    fi
  done < "$1"

  test "${#versions[@]}" -eq 1 || return 1
  printf '%s\n' "${versions[0]}"
}

assert_go_version_pins() {
  local ci_go_version release_go_version

  ci_go_version="$(workflow_go_version "$1")" || return 1
  release_go_version="$(workflow_go_version "$2")" || return 1
  test "$ci_go_version" = 1.26.5 || return 1
  test "$release_go_version" = 1.26.5 || return 1
  test "$ci_go_version" = "$release_go_version"
}

ci_workflow="$root/.github/workflows/ci.yml"
release_workflow="$root/.github/workflows/release.yml"
assert_go_version_pins "$ci_workflow" "$release_workflow"

grep -v '^[[:space:]]*go-version:' "$ci_workflow" > "$tmp/ci-missing-go-version.yml"
if assert_go_version_pins "$tmp/ci-missing-go-version.yml" "$release_workflow"; then exit 1; fi
sed 's/^\([[:space:]]*go-version:\).*/\1 "1.26.4"/' "$ci_workflow" > "$tmp/ci-mismatched-go-version.yml"
if assert_go_version_pins "$tmp/ci-mismatched-go-version.yml" "$release_workflow"; then exit 1; fi

grep -F 'name: binary-release' "$release_workflow" >/dev/null
grep -F 'name: container-release' "$release_workflow" >/dev/null
grep -F 'name: release' "$release_workflow" >/dev/null
grep -F 'make container-oci' "$release_workflow" >/dev/null
grep -F 'CONTAINER_SCAN_OCI_ROOT: .tmp/container-oci' "$ci_workflow" >/dev/null
grep -F 'CONTAINER_SCAN_OCI_ROOT: .tmp/container-oci' "$release_workflow" >/dev/null
grep -F 'write-container-release-manifest.sh' "$release_workflow" >/dev/null
grep -F 'assemble-release-assets.sh' "$release_workflow" >/dev/null
if grep -F 'packages: write' "$release_workflow"; then exit 1; fi
grep -F 'skopeo copy --all --preserve-digests' "$root/scripts/release/publish-private-image.sh" >/dev/null
grep -F 'gh run download' "$root/scripts/release/publish-private-image.sh" | grep -F -- '--name release' >/dev/null
grep -F 'container-release-manifest.json' "$root/scripts/release/publish-private-release.sh" >/dev/null
grep -F 'cosign verify --key' "$root/scripts/release/publish-private-release.sh" | grep -F 'signing/cosign.pub' >/dev/null
grep -F '## Private distribution' "$root/scripts/release/publish-private-release.sh" >/dev/null
grep -F -- '--json isDraft --jq .isDraft' "$root/scripts/release/publish-private-release.sh" >/dev/null
grep -F 'release_state' "$root/scripts/release/publish-private-release.sh" | grep -F '== false' >/dev/null
