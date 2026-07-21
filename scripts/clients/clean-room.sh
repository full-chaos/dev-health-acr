#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
release_dir=""
clients=""

usage() {
  printf '%s\n' 'Usage: clean-room.sh --release-dir DIR --clients opencode,claude-code,codex,cursor'
}

while (($#)); do
  case "$1" in
    --release-dir)
      [[ $# -ge 2 && -n "$2" ]] || { usage >&2; exit 2; }
      release_dir="$2"
      shift 2
      ;;
    --clients)
      [[ $# -ge 2 && -n "$2" ]] || { usage >&2; exit 2; }
      clients="$2"
      shift 2
      ;;
    --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done

[[ -n "$release_dir" && -d "$release_dir" ]] || { usage >&2; exit 2; }
[[ "$clients" == 'opencode,claude-code,codex,cursor' ]] || { usage >&2; exit 2; }

temporary_root="$(mktemp -d)"
cleanup() {
  chmod -R u+w "$temporary_root" 2>/dev/null || true
  rm -rf "$temporary_root"
}
trap cleanup EXIT

release_input="$temporary_root/release-input"
cp -R "$release_dir" "$release_input"
go -C "$repo_root" run ./cmd/releasebuild verify --dir "$release_dir" >/dev/null
if ! grep -Fq '  release-manifest.json' "$release_input/SHA256SUMS"; then
  manifest_sha256="$(shasum -a 256 "$release_input/release-manifest.json" | awk '{print $1}')"
  printf '%s  release-manifest.json\n' "$manifest_sha256" >>"$release_input/SHA256SUMS"
fi
extracted="$temporary_root/release"
receipt="$(go -C "$repo_root" run ./cmd/releasebuild consume --dir "$release_input" --dest "$extracted")"
goos="$(go env GOHOSTOS)"
goarch="$(go env GOHOSTARCH)"
python3 - "$receipt" "$goos" "$goarch" <<'PY'
import json
import re
import sys

receipt = json.loads(sys.argv[1])
if receipt != {
    "archive_sha256": receipt.get("archive_sha256"),
    "client_bundle_sha256": receipt.get("client_bundle_sha256"),
    "product": "acr-mcp",
    "goos": sys.argv[2],
    "goarch": sys.argv[3],
} or not all(re.fullmatch(r"[0-9a-f]{64}", receipt[key]) for key in ("archive_sha256", "client_bundle_sha256")):
    raise SystemExit("clean-room: invalid release consume receipt")
PY

packages="$extracted/clients"
[[ -x "$extracted/acr-mcp" || -x "$extracted/acr-mcp.exe" ]] || exit 1
[[ -f "$packages/conformance/client-bundle.v1.json" ]] || exit 1
bash "$repo_root/scripts/clients/verify-packages.sh" \
  --contract "$packages/conformance/client-bundle.v1.json" \
  --root "$extracted"

for client in opencode claude-code codex; do
  command_name="$client"
  [[ "$client" != claude-code ]] || command_name=claude
  command -v "$command_name" >/dev/null 2>&1 || {
    printf 'clean-room: required client not installed: %s\n' "$command_name" >&2
    exit 1
  }
done

bash "$repo_root/scripts/clients/test-opencode.sh" \
  --package "$packages/opencode" --scenario lifecycle
bash "$repo_root/scripts/clients/test-claude-code.sh" \
  --package "$packages/claude-code" --scenario lifecycle
bash "$repo_root/scripts/clients/test-codex.sh" \
  --package "$packages/codex"

cursor_state=not_installed
cursor_args=()
if command -v agent >/dev/null 2>&1; then
  cursor_state=installed
  cursor_args+=(--real-client-if-installed)
fi
bash "$repo_root/scripts/clients/test-cursor.sh" \
  --package "$packages/cursor" --scenario lifecycle "${cursor_args[@]}"

printf 'CLIENT_CLEAN_ROOM_OK clients=%s cursor_client=%s release_receipt=%s host_config=untouched\n' \
  "$clients" "$cursor_state" "$receipt"
