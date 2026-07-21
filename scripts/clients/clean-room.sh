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

extracted="$temporary_root/release"
receipt="$(go -C "$repo_root" run ./cmd/releasebuild consume --dir "$release_dir" --dest "$extracted")"
goos="$(go env GOHOSTOS)"
goarch="$(go env GOHOSTARCH)"
[[ "$receipt" =~ ^\{"archive_sha256":"[0-9a-f]{64}","client_bundle_sha256":"[0-9a-f]{64}","product":"acr-mcp","goos":"$goos","goarch":"$goarch"\}$ ]] || {
  printf '%s\n' 'clean-room: invalid release consume receipt' >&2
  exit 1
}

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
