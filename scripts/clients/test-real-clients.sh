#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
mode=""
required=""
release_dir=""
cursor_if_installed=0

usage() {
  printf '%s\n' 'Usage: test-real-clients.sh (--self-test|--self-test-leaked-home|--self-test-release-dir|--require opencode,claude-code,codex) [--release-dir DIR] [--cursor-if-installed]'
}

need_value() { [[ $# -ge 2 && -n "$2" ]] || { usage >&2; exit 2; }; }
while (($#)); do
  case "$1" in
    --self-test|--self-test-leaked-home|--self-test-release-dir)
      [[ -z "$mode" ]] || { usage >&2; exit 2; }
      mode="$1"; shift ;;
    --require) need_value "$@"; [[ -z "$required" ]] || { usage >&2; exit 2; }; required="$2"; shift 2 ;;
    --release-dir) need_value "$@"; [[ -z "$release_dir" ]] || { usage >&2; exit 2; }; release_dir="$2"; shift 2 ;;
    --cursor-if-installed) ((cursor_if_installed == 0)) || { usage >&2; exit 2; }; cursor_if_installed=1; shift ;;
    --help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
[[ -n "$mode" || -n "$required" ]] || { usage >&2; exit 2; }
[[ -z "$mode" || -z "$required" ]] || { usage >&2; exit 2; }
[[ -z "$release_dir" || "$required" == 'opencode,claude-code,codex' ]] || { usage >&2; exit 2; }
[[ "$cursor_if_installed" == 0 || "$required" == 'opencode,claude-code,codex' ]] || { usage >&2; exit 2; }
[[ -z "$required" || "$required" == 'opencode,claude-code,codex' ]] || { usage >&2; exit 2; }

temporary_root="$(mktemp -d)"
cleanup() { chmod -R u+w "$temporary_root" 2>/dev/null || true; rm -rf "$temporary_root"; }
trap cleanup EXIT

assert_no_owned_state() {
  local home="$1"
  ! grep -R -Fq --exclude-dir=cache 'context-fabric' "$home" 2>/dev/null
}

validate_receipt() {
  local receipt="$1" goos goarch
  goos="$(go env GOHOSTOS)"
  goarch="$(go env GOHOSTARCH)"
  [[ "$receipt" =~ ^\{"archive_sha256":"[0-9a-f]{64}","client_bundle_sha256":"[0-9a-f]{64}","product":"acr-mcp","goos":"$goos","goarch":"$goarch"\}$ ]]
}

consume_release() {
  local release="$1" destination="$2" receipt
  [[ -d "$release" && "$destination" = /* && ! -e "$destination" ]] || return 1
  receipt="$(go -C "$repo_root" run ./cmd/releasebuild consume --dir "$release" --dest "$destination")" || return 1
  validate_receipt "$receipt"
  [[ -x "$destination/acr-mcp" || -x "$destination/acr-mcp.exe" ]] || return 1
  [[ -f "$destination/clients/conformance/client-bundle.v1.json" ]] || return 1
  for client in opencode claude-code codex cursor; do
    [[ -f "$destination/clients/$client/package.v1.json" ]] || return 1
  done
}

adapter_roots() {
  local root="$1"
  mkdir -p "$root/home" "$root/config" "$root/work" "$root/bin" "$root/records"
  : >"$root/bin/acr-mcp"
  chmod 700 "$root/bin/acr-mcp"
}

run_adapter() {
  local client="$1" binary="$2" root="$3"
  go -C "$repo_root" run ./cmd/native-client-adapter \
    --client "$client" --binary "$binary" --home "$root/home" --config "$root/config" \
    --work "$root/work" --sidecar "$root/bin/acr-mcp" --record-dir "$root/records"
}

run_recording_selftest() {
  local root="$temporary_root/native-adapters"
  adapter_roots "$root"
  go -C "$repo_root" build -o "$root/bin/recording-stub" ./cmd/native-client-recording-stub
  for client in opencode claude-code codex cursor; do
    local executable="$client"
    [[ "$client" != cursor ]] || executable=agent
    cat >"$root/bin/$executable" <<EOF
#!/usr/bin/env bash
exec "$root/bin/recording-stub" "$client" "\$@"
EOF
    chmod 700 "$root/bin/$executable"
    run_adapter "$client" "$root/bin/$executable" "$root" | grep -Fqx "NATIVE_CLIENT_ADAPTER_OK client=$client result=validated"
    grep -Fq '"config"' "$root/records/$client.json"
    grep -Fq '"args"' "$root/records/$client.json"
  done
  if go -C "$repo_root" run ./cmd/native-client-adapter --client codex --binary codex --home "$root/home" --config "$root/config" --work "$root/work" --sidecar "$root/bin/acr-mcp" >/dev/null 2>&1; then
    return 1
  fi
  printf '%s\n' 'NATIVE_ADAPTER_RECORDING_SELF_TEST_OK clients=opencode,claude-code,codex,cursor parser=exact runner=bounded redaction=passed'
}

run_lifecycle() {
  local packages="$1"
  bash "$repo_root/scripts/clients/test-opencode.sh" --package "$packages/opencode" --scenario lifecycle >/dev/null 2>&1
  bash "$repo_root/scripts/clients/test-claude-code.sh" --package "$packages/claude-code" --scenario lifecycle >/dev/null 2>&1
  bash "$repo_root/scripts/clients/test-codex.sh" --package "$packages/codex" >/dev/null 2>&1
  bash "$repo_root/scripts/clients/test-cursor.sh" --package "$packages/cursor" --scenario lifecycle --real-client-if-installed >/dev/null 2>&1
}

if [[ "$mode" == --self-test-leaked-home ]]; then
  home="$temporary_root/leaked-home"
  mkdir -p "$home/.claude/plugins" "$home/.codex/plugins"
  printf '%s\n' context-fabric >"$home/.claude/plugins/leaked"
  printf '%s\n' context-fabric >"$home/.codex/plugins/leaked"
  if assert_no_owned_state "$home"; then exit 1; fi
  rm -f "$home/.claude/plugins/leaked" "$home/.codex/plugins/leaked"
  assert_no_owned_state "$home"
  printf '%s\n' 'REAL_CLIENT_LEAKED_HOME_OK detected=1 cleaned=1'
  exit 0
fi

if [[ "$mode" == --self-test-release-dir ]]; then
  go -C "$repo_root" test -race -count=1 ./internal/releasebuild ./cmd/releasebuild >/dev/null 2>&1
  printf '%s\n' 'REAL_CLIENT_RELEASE_DIR_SELF_TEST_OK consumer=contract-tested extracted_provenance=passed'
  exit 0
fi

if [[ -n "$release_dir" ]]; then
  extracted_release="$temporary_root/extracted-release"
  consume_release "$release_dir" "$extracted_release"
  package_prefix="$extracted_release/clients"
else
  package_prefix="$repo_root/clients"
fi
bash "$repo_root/scripts/clients/verify-packages.sh" --contract "$package_prefix/conformance/client-bundle.v1.json" --root "${package_prefix%/clients}" >/dev/null 2>&1

if [[ -n "$required" ]]; then
  adapter_root="$temporary_root/required-adapters"
  adapter_roots "$adapter_root"
  for pair in opencode:opencode claude-code:claude codex:codex; do
    client="${pair%%:*}"; executable="${pair##*:}"
    binary="$(command -v "$executable")"
    run_adapter "$client" "$binary" "$adapter_root"
  done
  if ((cursor_if_installed)); then
    if command -v agent >/dev/null 2>&1; then
      run_adapter cursor "$(command -v agent)" "$adapter_root"
      cursor_state=installed
    else
      cursor_state=not_installed
    fi
  else
    cursor_state=not_requested
  fi
  run_lifecycle "$package_prefix"
  printf 'REAL_CLIENT_REQUIRED_OK clients=%s cursor=%s\n' "$required" "$cursor_state"
  exit 0
fi

go -C "$repo_root" test -race -count=1 -run 'TestClientConformance|TestClientServeCommand|TestParsePerClientGoldenAndFailures|TestRunDeadlineOutputLimitAndRedaction|TestRecordingStubAcceptsAndCapturesInvocation' ./internal/mcpclientfixtures ./internal/nativeadapters >/dev/null 2>&1
run_recording_selftest
available=""; unavailable=""
for probe in opencode:opencode claude-code:claude codex:codex cursor:agent; do
  client="${probe%%:*}"; binary="${probe##*:}"
  if command -v "$binary" >/dev/null 2>&1; then available="${available:+$available,}$client"; else unavailable="${unavailable:+$unavailable,}$client"; fi
done
printf 'REAL_CLIENT_SELF_TEST_OK available=%s unavailable=%s registration=acr-mcp_serve\n' "${available:-none}" "${unavailable:-none}"
