#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

overlay=""
timeout="10m"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --overlay) overlay="${2:-}"; shift 2 ;;
    --timeout) timeout="${2:-}"; shift 2 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_overlay "$overlay"
command -v kubectl >/dev/null 2>&1 || fail "kubectl is required"
namespace="$(overlay_namespace "$overlay")"

kubectl --namespace "$namespace" rollout status deployment/acr-api --timeout="$timeout"
kubectl --namespace "$namespace" wait --for=condition=available deployment/acr-api --timeout="$timeout"
