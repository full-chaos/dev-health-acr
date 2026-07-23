#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/fixture" "$tmp/home/.config/acr/release"

tag=v1.2.3
version=1.2.3
run_id=123
commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
product=acr-api
archive="${product}_${version}_linux_multiarch.oci.tar"
remote_raw='{"manifests":[{"platform":{"architecture":"amd64","os":"linux"}},{"platform":{"architecture":"arm64","os":"linux"}}],"schemaVersion":2}'
digest="sha256:$(printf '%s' "$remote_raw" | shasum -a 256 | awk '{print $1}')"
repository="ghcr.io/full-chaos/dev-health-acr/$product"

printf 'fixture archive\n' >"$tmp/fixture/$archive"
archive_sha256="$(shasum -a 256 "$tmp/fixture/$archive" | awk '{print $1}')"
jq -cn \
  --arg tag "$tag" \
  --arg version "$version" \
  --arg commit "$commit" \
  --arg archive "$archive" \
  --arg archive_sha256 "$archive_sha256" \
  --arg digest "$digest" \
  --arg repository "$repository" \
  '{schema_version:"container_release_manifest.v1",tag:$tag,version:$version,commit:$commit,date:"2026-07-23T00:00:00Z",images:[{product:"acr-api",repository:$repository,archive:$archive,archive_sha256:$archive_sha256,digest:$digest,platforms:["linux/amd64","linux/arm64"]}]}' \
  >"$tmp/fixture/container-release-manifest.json"
(
  cd "$tmp/fixture"
  for file in "$archive" container-release-manifest.json; do
    shasum -a 256 "$file"
  done >SHA256SUMS
)

cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'gh %s\n' "$*" >>"$MOCK_LOG"
case "$1 $2" in
  'api user') printf 'chrisgeo\n' ;;
  'api repos/full-chaos/dev-health-acr/actions/runs/123') printf 'Release\tpush\tsuccess\tv1.2.3\taaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\t.github/workflows/release.yml\n' ;;
  'api orgs/full-chaos/packages/container/dev-health-acr%2Facr-api') printf 'private\n' ;;
  'auth token') printf 'registry-secret\n' ;;
  'repo view') printf 'full-chaos/dev-health-acr\ttrue\n' ;;
  'repo clone') mkdir -p "$4" ;;
  'run download')
    while (($#)); do
      if [[ "$1" == --dir ]]; then
        mkdir -p "$2"
        cp "$MOCK_FIXTURE"/* "$2/"
        exit 0
      fi
      shift
    done
    exit 1
    ;;
  'variable get') printf 'chrisgeo\n' ;;
  *) exit 1 ;;
esac
EOF

cat >"$tmp/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'git %s\n' "$*" >>"$MOCK_LOG"
[[ "$1" == -C ]]
case "$3" in
  fetch) exit 0 ;;
  verify-tag) printf '[GNUPG:] VALIDSIG %s fixture\n' "$MOCK_FINGERPRINT" ;;
  cat-file) printf 'tag\n' ;;
  rev-list) printf '%s\n' "$MOCK_COMMIT" ;;
  rev-parse)
    if [[ "$4" == refs/tags/* ]]; then printf '%s\n' "$MOCK_TAG_OBJECT"; else printf '%s\n' "$MOCK_COMMIT"; fi
    ;;
  merge-base) exit 0 ;;
  ls-remote) printf '%s\t%s\n%s\t%s\n' "$MOCK_TAG_OBJECT" "$5" "$MOCK_COMMIT" "$6" ;;
  *) exit 1 ;;
esac
EOF

cat >"$tmp/bin/gpg" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'gpg %s\n' "$*" >>"$MOCK_LOG"
if [[ " $* " == *' --import '* ]]; then exit 0; fi
if [[ " $* " == *' --with-colons --fingerprint '* ]]; then
  printf 'fpr:::::::::%s:\n' "$MOCK_FINGERPRINT"
  exit 0
fi
exit 1
EOF

cat >"$tmp/bin/skopeo" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'skopeo %s\n' "$*" >>"$MOCK_LOG"
if [[ "$1" == --version ]]; then printf 'skopeo version 1.23.0\n'; exit 0; fi
if [[ "$1" == login ]]; then
  read -r token
  test "$token" = registry-secret
  exit 0
fi
if [[ "$1" == inspect ]]; then
  [[ " $* " == *' --no-creds '* ]] && exit 1
  printf '%s' "$MOCK_REMOTE_RAW"
  exit 0
fi
if [[ "$1" == copy ]]; then
  digest_file=''
  while (($#)); do
    if [[ "$1" == --digestfile ]]; then digest_file="$2"; shift 2; continue; fi
    shift
  done
  test -n "$digest_file"
  printf '%s\n' "$MOCK_DIGEST" >"$digest_file"
  touch "$MOCK_STATE"
  exit 0
fi
exit 1
EOF

cat >"$tmp/bin/cosign" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'cosign %s\n' "$*" >>"$MOCK_LOG"
if [[ "$1" == version ]]; then printf 'GitVersion:    v3.1.1\n'; fi
EOF
chmod 755 "$tmp/bin"/*
touch "$tmp/home/.config/acr/release/cosign.key" "$tmp/cosign.pub"

PATH="$tmp/bin:$PATH" \
HOME="$tmp/home" \
COSIGN_PASSWORD=test \
MOCK_LOG="$tmp/commands.log" \
MOCK_FIXTURE="$tmp/fixture" \
MOCK_STATE="$tmp/published" \
MOCK_REMOTE_RAW="$remote_raw" \
MOCK_DIGEST="$digest" \
MOCK_FINGERPRINT=9DCD0E7D385C8247E2F5E7FC2C43EBC02D8C8781 \
MOCK_TAG_OBJECT=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
MOCK_COMMIT="$commit" \
  "$root/scripts/release/publish-private-image.sh" \
    --digest "$digest" \
    "$product" "$tag" "$run_id"

grep -F 'skopeo copy --all --preserve-digests' "$tmp/commands.log" >/dev/null
grep -F 'oci-archive:' "$tmp/commands.log" >/dev/null
if grep -F "docker://${repository}:${tag}" "$tmp/commands.log"; then exit 1; fi
grep -F 'cosign sign' "$tmp/commands.log" | grep -F "${repository}@${digest}" >/dev/null
grep -F 'cosign verify' "$tmp/commands.log" | grep -F "${repository}@${digest}" >/dev/null
grep -F 'verify-tag --raw v1.2.3' "$tmp/commands.log" >/dev/null
grep -F 'merge-base --is-ancestor' "$tmp/commands.log" >/dev/null
grep -F 'ls-remote origin' "$tmp/commands.log" >/dev/null
if grep -F 'registry-secret' "$tmp/commands.log"; then exit 1; fi
digest_copy_line="$(grep -nF "docker://${repository}@${digest}" "$tmp/commands.log" | grep -F 'skopeo copy' | cut -d: -f1 | sort -n | awk 'NR == 1 { print }')"
sign_line="$(grep -nF "${repository}@${digest}" "$tmp/commands.log" | grep -F 'cosign sign' | cut -d: -f1)"
test "$digest_copy_line" -lt "$sign_line"

wrong_digest=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
if PATH="$tmp/bin:$PATH" \
  HOME="$tmp/home" \
  COSIGN_PASSWORD=test \
  MOCK_LOG="$tmp/mismatch.log" \
  MOCK_FIXTURE="$tmp/fixture" \
  MOCK_STATE="$tmp/published" \
  MOCK_REMOTE_RAW="$remote_raw" \
  MOCK_DIGEST="$wrong_digest" \
  MOCK_FINGERPRINT=9DCD0E7D385C8247E2F5E7FC2C43EBC02D8C8781 \
  MOCK_TAG_OBJECT=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  MOCK_COMMIT="$commit" \
  "$root/scripts/release/publish-private-image.sh" \
    --digest "$wrong_digest" \
    "$product" "$tag" "$run_id"; then
  exit 1
fi
if grep -F 'skopeo copy' "$tmp/mismatch.log"; then exit 1; fi

printf 'private image publisher fixture passed\n'
