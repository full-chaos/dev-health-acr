#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

overlay="development"
image=""
apply=false
timeout="10m"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --overlay) overlay="${2:-}"; shift 2 ;;
    --image) image="${2:-}"; shift 2 ;;
    --apply) apply=true; shift ;;
    --timeout) timeout="${2:-}"; shift 2 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_overlay "$overlay"
require_digest_image "$image"

if [[ "$apply" != true ]]; then
  render_manifest "$overlay" "$image" application | select_kinds "Deployment"
  exit 0
fi

command -v kubectl >/dev/null 2>&1 || fail "kubectl is required"
namespace="$(overlay_namespace "$overlay")"
render_manifest "$overlay" "$image" application \
  | select_kinds "Deployment" \
  | kubectl --namespace "$namespace" apply --server-side --field-manager=acr-kustomize -f -
"$SCRIPT_DIR/wait.sh" --overlay "$overlay" --timeout "$timeout"
