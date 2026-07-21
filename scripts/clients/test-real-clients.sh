#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
mode=""
required=""
cursor_if_installed=0
while (($#)); do
  case "$1" in
    --self-test|--self-test-leaked-home) mode="$1"; shift ;;
    --require) required="$2"; shift 2 ;;
    --cursor-if-installed) cursor_if_installed=1; shift ;;
    *) exit 2 ;;
  esac
done
[[ -n "$mode" || -n "$required" ]] || exit 2

temporary_root="$(mktemp -d)"
cleanup() { chmod -R u+w "$temporary_root" 2>/dev/null || true; rm -rf "$temporary_root"; }
trap cleanup EXIT

assert_no_owned_state() {
  local home="$1"
  ! grep -R -Fq --exclude-dir=cache 'context-fabric' "$home" 2>/dev/null
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

bash "$repo_root/scripts/clients/verify-packages.sh" --contract clients/conformance/client-bundle.v1.json

run_if_available() {
  local executable="$1" script="$2"; shift 2
  if command -v "$executable" >/dev/null 2>&1; then
    bash "$repo_root/scripts/clients/$script" "$@"
    printf 'REAL_CLIENT_AVAILABLE client=%s lifecycle=passed\n' "$executable"
  else
    printf 'REAL_CLIENT_UNAVAILABLE client=%s\n' "$executable"
    return 1
  fi
}

if [[ -n "$required" ]]; then
  [[ "$required" == "opencode,claude-code,codex" ]] || exit 2
  run_if_available opencode test-opencode.sh --package clients/opencode --scenario lifecycle
  run_if_available claude test-claude-code.sh --scenario lifecycle
  run_if_available codex test-codex.sh
  if (( cursor_if_installed )); then
    bash "$repo_root/scripts/clients/test-cursor.sh" --package clients/cursor --scenario lifecycle --real-client-if-installed
  fi
  printf 'REAL_CLIENT_REQUIRED_OK clients=%s cursor_if_installed=%d\n' "$required" "$cursor_if_installed"
  exit 0
fi

# --self-test validates the conformance harness wiring deterministically,
# independent of whether any native client is installed or of its exact
# version -- native per-client lifecycles are exercised by the --require path
# (F3). It records which native clients are available so callers see the
# environment honestly.
go -C "$repo_root" test -race -count=1 -run 'TestClientConformance|TestClientServeCommand' ./internal/mcpclientfixtures
available=""
unavailable=""
for probe in opencode:opencode claude-code:claude codex:codex cursor:agent; do
  client="${probe%%:*}"
  binary="${probe##*:}"
  if command -v "$binary" >/dev/null 2>&1; then
    available="${available:+$available,}$client"
  else
    unavailable="${unavailable:+$unavailable,}$client"
  fi
done
printf 'REAL_CLIENT_SELF_TEST_OK available=%s unavailable=%s registration=acr-mcp_serve\n' "${available:-none}" "${unavailable:-none}"
