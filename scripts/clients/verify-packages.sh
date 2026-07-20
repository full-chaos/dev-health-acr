#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
contract=""
fixture=""
package_root=""
while (($#)); do
  case "$1" in
    --contract) contract="$2"; shift 2 ;;
    --fixture) fixture="$2"; shift 2 ;;
    --root) package_root="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
resolve_path() {
  local path="$1"
  if [[ "$path" = /* ]]; then printf '%s\n' "$path"; else printf '%s/%s\n' "$repo_root" "$path"; fi
}
if [[ -n "$fixture" ]]; then fixture="$(resolve_path "$fixture")"; contract="$fixture/client-bundle.v1.json"; fi
[[ -n "$contract" ]] || exit 2
contract="$(resolve_path "$contract")"
package_root="$(resolve_path "${package_root:-$repo_root}")"
if [[ -z "$fixture" ]]; then
  (cd "$repo_root" && MCP_CLIENT_BUNDLE_PATH="$contract" MCP_CLIENT_ROOT="$package_root" go test -v ./internal/mcpclientfixtures -run '^TestClientBundle_validates_shared_contract$' -count=1) | grep -F 'CLIENT_BUNDLE_OK'
fi
fixture_root="${fixture:-$repo_root/clients/conformance/fixtures}"
if [[ ! -d "$fixture_root" ]]; then
  printf '%s\n' 'CLIENT_FIXTURE_ERROR classification=fixture.missing' >&2
  exit 1
fi
(cd "$repo_root" && MCP_CLIENT_FIXTURE_ROOT="$fixture_root" go test -v ./internal/mcpclientfixtures -run '^TestClientFixtureRunner_rejects_exact_classifications$' -count=1) | grep -F 'CLIENT_FIXTURES_OK'
