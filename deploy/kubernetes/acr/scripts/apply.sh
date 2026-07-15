#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

overlay=""
image=""
timeout="10m"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --overlay) overlay="${2:-}"; shift 2 ;;
    --image) image="${2:-}"; shift 2 ;;
    --timeout) timeout="${2:-}"; shift 2 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_overlay "$overlay"
[[ -n "$image" ]] || fail "--image is required"
require_digest_image "$image"
command -v kubectl >/dev/null 2>&1 || fail "kubectl is required"
namespace="$(overlay_namespace "$overlay")"

render_manifest "$overlay" "$image" all \
  | select_kinds "ConfigMap ServiceAccount Service HorizontalPodAutoscaler PodDisruptionBudget NetworkPolicy HTTPRoute" \
  | kubectl --namespace "$namespace" apply --server-side --field-manager=acr-kustomize -f -

kubectl --namespace "$namespace" delete job/acr-migrate --ignore-not-found
render_manifest "$overlay" "$image" all \
  | select_kinds "Job" \
  | kubectl --namespace "$namespace" apply --server-side --field-manager=acr-kustomize -f -

if ! kubectl --namespace "$namespace" wait --for=condition=complete job/acr-migrate --timeout="$timeout"; then
  kubectl --namespace "$namespace" describe job/acr-migrate >&2 || true
  fail "migration failed; deployment was not applied"
fi

render_manifest "$overlay" "$image" all \
  | select_kinds "Deployment" \
  | kubectl --namespace "$namespace" apply --server-side --field-manager=acr-kustomize -f -

"$SCRIPT_DIR/wait.sh" --overlay "$overlay" --timeout "$timeout"
