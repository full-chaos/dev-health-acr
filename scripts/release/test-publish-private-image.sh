#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/fixture" "$tmp/gnupg" "$tmp/home/.config/acr/release"
chmod 700 "$tmp/gnupg"

tag=v1.2.3
version=1.2.3
run_id=123
commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
product=acr-api
archive="${product}_${version}_linux_multiarch.oci.tar"
remote_raw='{"manifests":[{"platform":{"architecture":"amd64","os":"linux"}},{"platform":{"architecture":"arm64","os":"linux"}}],"schemaVersion":2}'
digest="sha256:$(printf '%s' "$remote_raw" | shasum -a 256 | awk '{print $1}')"
repository="ghcr.io/full-chaos/dev-health-acr/$product"
target="oci-image:${repository}@${digest}"

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

gpg --batch --homedir "$tmp/gnupg" --pinentry-mode loopback --passphrase '' \
  --quick-generate-key 'ACR image approval test <approval@example.invalid>' ed25519 sign 1d >/dev/null 2>&1
fingerprint="$(gpg --batch --homedir "$tmp/gnupg" --with-colons --list-keys | awk -F: '$1 == "fpr" {print $10; exit}')"
gpg --batch --homedir "$tmp/gnupg" --armor --export "$fingerprint" >"$tmp/approval-key.asc"
now="$(date -u +%s)"
iso_time() { date -u -r "$1" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d "@$1" '+%Y-%m-%dT%H:%M:%SZ'; }
receipt="$(jq -cnS \
  --arg target "$target" \
  --arg version "$version" \
  --arg digest "$digest" \
  --arg issued "$(iso_time "$now")" \
  --arg expires "$(iso_time "$((now + 600))")" \
  '{schema_version:"approval_receipt.v1",action:"publish_private_image",repository:"full-chaos/dev-health-acr",target:$target,version:$version,digest:$digest,visibility:"private",decision:"approve",issued_at:$issued,expires_at:$expires,nonce:"00000000000000000000000000000001"}')"
printf '%s' "$receipt" >"$tmp/receipt.json"
gpg --batch --homedir "$tmp/gnupg" --armor --detach-sign --output "$tmp/receipt.json.asc" "$tmp/receipt.json"

PATH="$tmp/bin:$PATH" \
HOME="$tmp/home" \
COSIGN_PASSWORD=test \
ACR_COSIGN_PUBLIC_KEY="$tmp/cosign.pub" \
ACR_APPROVAL_LEDGER="$tmp/ledger" \
ACR_APPROVAL_VERIFICATION_KEY="$tmp/approval-key.asc" \
ACR_APPROVAL_FINGERPRINT="$fingerprint" \
MOCK_LOG="$tmp/commands.log" \
MOCK_FIXTURE="$tmp/fixture" \
MOCK_STATE="$tmp/published" \
MOCK_REMOTE_RAW="$remote_raw" \
MOCK_DIGEST="$digest" \
  "$root/scripts/release/publish-private-image.sh" \
    --approval-receipt "$tmp/receipt.json" \
    --digest "$digest" \
    "$product" "$tag" "$run_id"

grep -F 'skopeo copy --all --preserve-digests' "$tmp/commands.log" >/dev/null
grep -F 'oci-archive:' "$tmp/commands.log" >/dev/null
if grep -F "docker://${repository}:${tag}" "$tmp/commands.log"; then exit 1; fi
grep -F 'cosign sign' "$tmp/commands.log" | grep -F "${repository}@${digest}" >/dev/null
grep -F 'cosign verify' "$tmp/commands.log" | grep -F "${repository}@${digest}" >/dev/null
if grep -F 'registry-secret' "$tmp/commands.log"; then exit 1; fi
digest_copy_line="$(grep -nF "docker://${repository}@${digest}" "$tmp/commands.log" | grep -F 'skopeo copy' | cut -d: -f1 | sort -n | awk 'NR == 1 { print }')"
sign_line="$(grep -nF "${repository}@${digest}" "$tmp/commands.log" | grep -F 'cosign sign' | cut -d: -f1)"
test "$digest_copy_line" -lt "$sign_line"

printf 'private image publisher fixture passed\n'
