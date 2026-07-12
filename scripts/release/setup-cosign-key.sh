#!/usr/bin/env bash
set -euo pipefail

key_dir="${HOME}/.config/acr/release"
public_key="signing/cosign.pub"
test ! -e "$public_key"
test ! -e "$key_dir/cosign.key"
test ! -e "$key_dir/cosign.pub"
cosign version | awk '$1 == "GitVersion:" && $2 == "v3.1.1" { found = 1 } END { exit !found }'
if [[ -z "${COSIGN_PASSWORD:-}" ]]; then
  read -r -s -p 'Cosign key password: ' COSIGN_PASSWORD
  printf '\n' >&2
fi
test -n "$COSIGN_PASSWORD"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"; unset COSIGN_PASSWORD' EXIT
chmod 700 "$tmp"
COSIGN_PASSWORD="$COSIGN_PASSWORD" cosign generate-key-pair --output-key-prefix "$tmp/cosign" >/dev/null
printf 'acr-cosign-self-test\n' > "$tmp/payload"
COSIGN_PASSWORD="$COSIGN_PASSWORD" cosign sign-blob --yes --key "$tmp/cosign.key" --output-signature "$tmp/payload.sig" --use-signing-config=false --new-bundle-format=false --tlog-upload=false "$tmp/payload"
cosign verify-blob --key "$tmp/cosign.pub" --signature "$tmp/payload.sig" --insecure-ignore-tlog "$tmp/payload"
install -d -m 700 "$key_dir"
install -m 600 "$tmp/cosign.key" "$key_dir/cosign.key"
install -m 644 "$tmp/cosign.pub" "$key_dir/cosign.pub"
install -d -m 755 signing
install -m 644 "$tmp/cosign.pub" "$public_key"
if command -v security >/dev/null; then
  security add-generic-password -U -a "$USER" -s acr-release-cosign -w "$COSIGN_PASSWORD" >/dev/null
fi
unset COSIGN_PASSWORD
