#!/usr/bin/env bash
approval_parse_options() {
  APPROVAL_RECEIPT=""
  APPROVAL_DRY_RUN=false
  export APPROVAL_DRY_RUN
  APPROVAL_DIGEST=""
  APPROVAL_ARGS=()

  while (($#)); do
    case "$1" in
      --approval-receipt)
        (($# >= 2)) || return 1
        APPROVAL_RECEIPT="$2"
        shift 2
        ;;
      --dry-run)
        APPROVAL_DRY_RUN=true
        shift
        ;;
      --digest)
        (($# >= 2)) || return 1
        APPROVAL_DIGEST="$2"
        shift 2
        ;;
      --)
        shift
        APPROVAL_ARGS=("$@")
        break
        ;;
      -*)
        return 1
        ;;
      *)
        APPROVAL_ARGS+=("$1")
        shift
        ;;
    esac
  done

  [[ -n "$APPROVAL_RECEIPT" && -n "$APPROVAL_DIGEST" ]]
}

approval_epoch() {
  local timestamp="$1"

  [[ "$timestamp" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || return 1
  date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$timestamp" +%s 2>/dev/null || date -u -d "$timestamp" +%s 2>/dev/null
}

approval_consume_nonce() {
  local ledger="$1"
  local nonce="$2"
  local receipt="$3"
  local record
  umask 077
  mkdir -p "$ledger"
  record="$(mktemp "$ledger/.receipt.XXXXXX")" || return 1
  if command -v sha256sum >/dev/null; then
    sha256sum "$receipt" >"$record" || { rm -f "$record"; return 1; }
  else
    shasum -a 256 "$receipt" >"$record" || { rm -f "$record"; return 1; }
  fi
  # Atomically claim the single-use nonce: a hard link fails if the nonce file
  # already exists, so concurrent or replayed consumption cannot both succeed.
  # This needs no lock and has no signal-interruption window.
  if ln "$record" "$ledger/$nonce" 2>/dev/null; then
    rm -f "$record"
    return 0
  fi
  rm -f "$record"
  return 1
}

approval_verify() {
  local receipt_in="$1"
  local snapdir rc
  [[ -f "$receipt_in" && -f "${receipt_in}.asc" ]] || return 1
  snapdir="$(mktemp -d)" || return 1
  # Snapshot the receipt and its detached signature once into a private (0700)
  # directory so every check and the nonce consumption operate on identical,
  # attacker-immutable bytes (closes a receipt/signature TOCTOU swap window).
  if ! { cp "$receipt_in" "$snapdir/receipt.json" && cp "${receipt_in}.asc" "$snapdir/receipt.json.asc"; }; then
    rm -rf "$snapdir"
    return 1
  fi
  _approval_verify_impl "$snapdir/receipt.json" "$2" "$3" "$4" "$5" "$6"
  rc=$?
  rm -rf "$snapdir"
  return $rc
}

_approval_verify_impl() {
  local receipt="$1"
  local action="$2"
  local repository="$3"
  local target="$4"
  local version="$5"
  local digest="$6"
  local signature="${receipt}.asc"
  local root key fingerprint gnupg status canonical issued expires now ledger nonce
  local expected_keys='["action","decision","digest","expires_at","issued_at","nonce","repository","schema_version","target","version","visibility"]'

  [[ -f "$receipt" && -f "$signature" ]] || return 1
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
  grep -qx -- '-----BEGIN PGP SIGNATURE-----' "$signature" || return 1
  grep -qx -- '-----END PGP SIGNATURE-----' "$signature" || return 1

  canonical="$(jq -cS . "$receipt")" || return 1
  printf '%s' "$canonical" | cmp -s - "$receipt" || return 1
  jq -e \
    --argjson expected_keys "$expected_keys" \
    --arg action "$action" \
    --arg repository "$repository" \
    --arg target "$target" \
    --arg version "$version" \
    --arg digest "$digest" '
      type == "object" and
      keys == $expected_keys and
      all(.[]; type == "string") and
      .schema_version == "approval_receipt.v1" and
      .action == $action and
      .repository == $repository and
      .target == $target and
      .version == $version and
      .digest == $digest and
      .visibility == "private" and
      .decision == "approve" and
      (.nonce | test("^[0-9a-f]{32}$"))
    ' "$receipt" >/dev/null || return 1

  issued="$(jq -r '.issued_at' "$receipt")"
  expires="$(jq -r '.expires_at' "$receipt")"
  issued="$(approval_epoch "$issued")" || return 1
  expires="$(approval_epoch "$expires")" || return 1
  now="$(date -u +%s)"
  ((issued <= now && expires >= now && expires > issued && expires - issued <= 900)) || return 1

  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
  key="${ACR_APPROVAL_VERIFICATION_KEY:-$root/signing/release-tag-signing-key.asc}"
  fingerprint="${ACR_APPROVAL_FINGERPRINT:-9DCD0E7D385C8247E2F5E7FC2C43EBC02D8C8781}"
  [[ "$fingerprint" =~ ^[0-9A-F]{40}$ && -r "$key" ]] || return 1
  gnupg="$(mktemp -d)"
  chmod 700 "$gnupg"
  gpg --batch --no-options --homedir "$gnupg" --import "$key" >/dev/null 2>&1 || { rm -rf "$gnupg"; return 1; }
  [[ "$(gpg --batch --no-options --homedir "$gnupg" --with-colons --fingerprint "$fingerprint" | awk -F: '$1 == "fpr" {print $10; exit}')" == "$fingerprint" ]] || { rm -rf "$gnupg"; return 1; }
  status="$(gpg --batch --no-options --homedir "$gnupg" --status-fd 1 --verify "$signature" "$receipt" 2>/dev/null)" || { rm -rf "$gnupg"; return 1; }
  grep -F -- "[GNUPG:] VALIDSIG $fingerprint " <<<"$status" >/dev/null || { rm -rf "$gnupg"; return 1; }
  rm -rf "$gnupg"

  # A dry run is a non-mutating preview: it performs every read-only check above
  # but must not consume the single-use nonce, so the same receipt still
  # authorizes the real action. Only the real action consumes (single use).
  if [[ "${APPROVAL_DRY_RUN:-false}" == true ]]; then
    return 0
  fi
  nonce="$(jq -r '.nonce' "$receipt")"
  ledger="${ACR_APPROVAL_LEDGER:-${XDG_STATE_HOME:-$HOME/.local/state}/acr/release-approval-ledger}"
  approval_consume_nonce "$ledger" "$nonce" "$receipt"
}
