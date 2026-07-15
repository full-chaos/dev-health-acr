#!/usr/bin/env bash
set -euo pipefail

scenario="happy"
if [[ $# -gt 0 ]]; then
  if [[ $# -ne 2 || "$1" != "--scenario" ]]; then
    printf 'usage: %s [--scenario SCENARIO]\n' "$0" >&2
    exit 64
  fi
  scenario="$2"
fi

case "$scenario" in
  happy|wrong-issuer|wrong-audience|wrong-alg|wrong-kid|expired|future|overlong|method|path|body|removed-key|foreign-scope|write-route|token-confusion) ;;
  *)
    printf 'unsupported web assertion scenario: %s\n' "$scenario" >&2
    exit 64
    ;;
esac

ACR_WEB_ASSERTION_INTEGRATION=1 ACR_WEB_ASSERTION_SCENARIO="$scenario" \
  go test ./cmd/acr-api -run '^TestWebAssertion_realBinary$' -count=1 -v
