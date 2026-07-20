#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
contract=""
fixture=""
while (($#)); do
  case "$1" in
    --contract) contract="$2"; shift 2 ;;
    --fixture) fixture="$2"; shift 2 ;;
    *) exit 2 ;;
  esac
done
if [[ -n "$fixture" ]]; then contract="$fixture/client-bundle.v1.json"; fi
[[ -n "$contract" ]] || exit 2
if [[ "$contract" != /* ]]; then contract="$root/$contract"; fi
MCP_CLIENT_BUNDLE_PATH="$contract" go test ./internal/mcpclientfixtures -run '^TestClientBundle_validates_shared_contract$' -count=1
if [[ -z "$fixture" ]]; then
  for name in invalid-bare-acr-mcp invalid-direct-api invalid-writeback-default invalid-preplan-default invalid-semver invalid-unsupported-command invalid-missing-clients invalid-credential-storage invalid-codegraph-command invalid-client-fork invalid-mutable-installer invalid-out-of-namespace; do
    if MCP_CLIENT_BUNDLE_PATH="$root/clients/conformance/fixtures/$name/client-bundle.v1.json" go test ./internal/mcpclientfixtures -run '^TestClientBundle_validates_shared_contract$' -count=1 >/dev/null 2>&1; then exit 1; fi
  done
fi
