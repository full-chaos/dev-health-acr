#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
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
