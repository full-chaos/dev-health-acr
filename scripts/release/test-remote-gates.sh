#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
mode=""
fixtures=""
while (($#)); do
  case "$1" in
    --mode) mode="${2:?}"; shift 2 ;;
    --fixtures) fixtures="${2:?}"; shift 2 ;;
    *) exit 1 ;;
  esac
done
[[ "$mode" == dry-run || "$mode" == reject-invalid ]]
[[ -d "$fixtures" && -f "$fixtures/README.md" ]]

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/gnupg" "$tmp/other-gnupg" "$tmp/ledger"
chmod 700 "$tmp/gnupg" "$tmp/other-gnupg"
for command in gh curl wget git docker cosign; do
  cat > "$tmp/bin/$command" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$0 $*" >> "$ACR_NETWORK_LOG"
exit 97
EOF
  chmod 755 "$tmp/bin/$command"
done

gpg --batch --homedir "$tmp/gnupg" --pinentry-mode loopback --passphrase '' --quick-generate-key 'ACR approval test <approval@example.invalid>' ed25519 sign 1d >/dev/null 2>&1
gpg --batch --homedir "$tmp/other-gnupg" --pinentry-mode loopback --passphrase '' --quick-generate-key 'ACR wrong signer <wrong@example.invalid>' ed25519 sign 1d >/dev/null 2>&1
fingerprint="$(gpg --batch --homedir "$tmp/gnupg" --with-colons --list-keys | awk -F: '$1 == "fpr" {print $10; exit}')"
gpg --batch --homedir "$tmp/gnupg" --armor --export "$fingerprint" > "$tmp/approval-test-key.asc"
jq -cn '{schema_version:"release_manifest.v1",version:"0.0.0-dev.1",commit:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",date:"2026-07-23T00:00:00Z",artifacts:[
  {name:"acr-api_0.0.0-dev.1_linux_amd64.tar.gz",product:"acr-api",goos:"linux",goarch:"amd64",sha256:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
  {name:"acr-mcp_0.0.0-dev.1_darwin_amd64.tar.gz",product:"acr-mcp",goos:"darwin",goarch:"amd64",sha256:"1111111111111111111111111111111111111111111111111111111111111111"},
  {name:"acr-mcp_0.0.0-dev.1_darwin_arm64.tar.gz",product:"acr-mcp",goos:"darwin",goarch:"arm64",sha256:"2222222222222222222222222222222222222222222222222222222222222222"},
  {name:"acr-mcp_0.0.0-dev.1_linux_amd64.tar.gz",product:"acr-mcp",goos:"linux",goarch:"amd64",sha256:"3333333333333333333333333333333333333333333333333333333333333333"},
  {name:"acr-mcp_0.0.0-dev.1_linux_arm64.tar.gz",product:"acr-mcp",goos:"linux",goarch:"arm64",sha256:"4444444444444444444444444444444444444444444444444444444444444444"},
  {name:"acr-mcp_0.0.0-dev.1_windows_amd64.zip",product:"acr-mcp",goos:"windows",goarch:"amd64",sha256:"5555555555555555555555555555555555555555555555555555555555555555"}
]}' >"$tmp/release-manifest.json"
mcp_artifacts="$(jq -cS '[.artifacts[] | select(.product == "acr-mcp") | {name, product, goos, goarch, sha256}] | sort_by(.name)' "$tmp/release-manifest.json")"
if command -v sha256sum >/dev/null; then
  digest="sha256:$(printf '%s' "$mcp_artifacts" | sha256sum | awk '{print $1}')"
else
  digest="sha256:$(printf '%s' "$mcp_artifacts" | shasum -a 256 | awk '{print $1}')"
fi

iso_time() {
  date -u -r "$1" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date -u -d "@$1" '+%Y-%m-%dT%H:%M:%SZ'
}

write_receipt() {
  local file="$1" action="$2" target="$3" version="$4" visibility="$5" nonce="$6" issued="$7" expires="$8" home="${9:-$tmp/gnupg}"
  local canonical

  canonical="$(jq -cn \
    --arg action "$action" \
    --arg target "$target" \
    --arg version "$version" \
    --arg digest "$digest" \
    --arg visibility "$visibility" \
    --arg nonce "$nonce" \
    --arg issued "$issued" \
    --arg expires "$expires" \
    '{schema_version:"approval_receipt.v1",action:$action,repository:"full-chaos/dev-health-acr",target:$target,version:$version,digest:$digest,visibility:$visibility,decision:"approve",issued_at:$issued,expires_at:$expires,nonce:$nonce}' | jq -cS .)"
  printf '%s' "$canonical" > "$file"
  gpg --batch --homedir "$home" --armor --detach-sign --output "${file}.asc" "$file"
}

run_gate() {
  local script="$1" receipt="$2" target="$3" version="$4"
  shift 4
  env \
    PATH="$tmp/bin:$PATH" \
    ACR_NETWORK_LOG="$tmp/network.log" \
    ACR_APPROVAL_LEDGER="$tmp/ledger" \
    ACR_APPROVAL_VERIFICATION_KEY="$tmp/approval-test-key.asc" \
    ACR_APPROVAL_FINGERPRINT="$fingerprint" \
    bash "$root/scripts/release/$script" --approval-receipt "$receipt" --digest "$digest" --dry-run "$@"
}

run_real_gate() {
  local script="$1" receipt="$2" target="$3" version="$4"
  shift 4
  env \
    PATH="$tmp/bin:$PATH" \
    ACR_NETWORK_LOG="$tmp/network.log" \
    ACR_APPROVAL_LEDGER="$tmp/ledger" \
    ACR_APPROVAL_VERIFICATION_KEY="$tmp/approval-test-key.asc" \
    ACR_APPROVAL_FINGERPRINT="$fingerprint" \
    bash "$root/scripts/release/$script" --approval-receipt "$receipt" --digest "$digest" "$@"
}

now="$(date -u +%s)"
issued="$(iso_time "$now")"
expires="$(iso_time "$((now + 600))")"
mcp_target="mcp-binaries:full-chaos/dev-health-acr:v0.0.0-dev.1"

if [[ "$mode" == dry-run ]]; then
  write_receipt "$tmp/mcp.json" approve_mcp_binaries "$mcp_target" 0.0.0-dev.1 private 00000000000000000000000000000001 "$issued" "$expires"
  run_gate verify-mcp-binary-approval.sh "$tmp/mcp.json" "$mcp_target" 0.0.0-dev.1 "$tmp/release-manifest.json" v0.0.0-dev.1
  run_real_gate verify-mcp-binary-approval.sh "$tmp/mcp.json" "$mcp_target" 0.0.0-dev.1 "$tmp/release-manifest.json" v0.0.0-dev.1
  if run_real_gate verify-mcp-binary-approval.sh "$tmp/mcp.json" "$mcp_target" 0.0.0-dev.1 "$tmp/release-manifest.json" v0.0.0-dev.1; then
    exit 1
  fi
else
  reject() {
    local receipt="$1"
    if run_gate verify-mcp-binary-approval.sh "$receipt" "$mcp_target" 0.0.0-dev.1 "$tmp/release-manifest.json" v0.0.0-dev.1; then
      return 1
    fi
  }

  write_receipt "$tmp/missing.json" approve_mcp_binaries "$mcp_target" 0.0.0-dev.1 private 00000000000000000000000000000011 "$issued" "$expires"
  rm "$tmp/missing.json.asc"
  reject "$tmp/missing.json"
  write_receipt "$tmp/wrong.json" approve_mcp_binaries "$mcp_target" 0.0.0-dev.1 private 00000000000000000000000000000012 "$issued" "$expires" "$tmp/other-gnupg"
  reject "$tmp/wrong.json"
  write_receipt "$tmp/wrong-target.json" approve_mcp_binaries 'mcp-binaries:full-chaos/dev-health-acr:v9.9.9' 0.0.0-dev.1 private 00000000000000000000000000000018 "$issued" "$expires"
  reject "$tmp/wrong-target.json"
  write_receipt "$tmp/noncanonical.json" approve_mcp_binaries "$mcp_target" 0.0.0-dev.1 private 00000000000000000000000000000013 "$issued" "$expires"
  printf ' %s' "$(<"$tmp/noncanonical.json")" > "$tmp/noncanonical.json.tmp"
  mv "$tmp/noncanonical.json.tmp" "$tmp/noncanonical.json"
  rm "$tmp/noncanonical.json.asc"
  gpg --batch --homedir "$tmp/gnupg" --armor --detach-sign --output "$tmp/noncanonical.json.asc" "$tmp/noncanonical.json"
  reject "$tmp/noncanonical.json"
  write_receipt "$tmp/expired.json" approve_mcp_binaries "$mcp_target" 0.0.0-dev.1 private 00000000000000000000000000000014 "$(iso_time "$((now - 1200))")" "$(iso_time "$((now - 600))")"
  reject "$tmp/expired.json"
  write_receipt "$tmp/mutated.json" approve_mcp_binaries "$mcp_target" 0.0.0-dev.1 private 00000000000000000000000000000016 "$issued" "$expires"
  jq --arg target 'mcp-binaries:full-chaos/dev-health-acr:v9.9.9' '.target = $target' "$tmp/mutated.json" | jq -cS . | tr -d '\n' > "$tmp/mutated.json.tmp"
  mv "$tmp/mutated.json.tmp" "$tmp/mutated.json"
  reject "$tmp/mutated.json"
  write_receipt "$tmp/public.json" approve_mcp_binaries "$mcp_target" 0.0.0-dev.1 public 00000000000000000000000000000017 "$issued" "$expires"
  reject "$tmp/public.json"
  printf '{' > "$tmp/malformed.json"
  gpg --batch --homedir "$tmp/gnupg" --armor --detach-sign --output "$tmp/malformed.json.asc" "$tmp/malformed.json"
  reject "$tmp/malformed.json"
  (
    life_ledger="$tmp/ledger-lifecycle"
    life_nonce=00000000000000000000000000000015
    export ACR_APPROVAL_LEDGER="$life_ledger"
    export ACR_APPROVAL_VERIFICATION_KEY="$tmp/approval-test-key.asc"
    export ACR_APPROVAL_FINGERPRINT="$fingerprint"
    source "$root/scripts/release/approval-receipt.sh"
    write_receipt "$tmp/replay.json" approve_mcp_binaries "$mcp_target" 0.0.0-dev.1 private "$life_nonce" "$issued" "$expires"
    export APPROVAL_DRY_RUN=true
    approval_verify "$tmp/replay.json" approve_mcp_binaries full-chaos/dev-health-acr "$mcp_target" 0.0.0-dev.1 "$digest"
    approval_verify "$tmp/replay.json" approve_mcp_binaries full-chaos/dev-health-acr "$mcp_target" 0.0.0-dev.1 "$digest"
    [[ ! -e "$life_ledger/$life_nonce" ]]
    export APPROVAL_DRY_RUN=false
    approval_verify "$tmp/replay.json" approve_mcp_binaries full-chaos/dev-health-acr "$mcp_target" 0.0.0-dev.1 "$digest"
    [[ -e "$life_ledger/$life_nonce" ]]
    if approval_verify "$tmp/replay.json" approve_mcp_binaries full-chaos/dev-health-acr "$mcp_target" 0.0.0-dev.1 "$digest"; then
      exit 1
    fi
  )
fi

[[ ! -s "$tmp/network.log" ]]
printf 'remote approval gates completed without network calls: %s\n' "$mode"
