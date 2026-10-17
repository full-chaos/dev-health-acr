#!/usr/bin/env bash
# Renders the ACR Helm chart offline: default values (with only the required
# references supplied), the development values through the harness happy path,
# and every negative scenario of scripts/deploy/test-helm.sh.
#
# Runs only when the chart or the harness changed relative to the base ref;
# set HELM_CHECK_FORCE=1 to run unconditionally. A push event has no base, so
# the caller passes none and the check always runs.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
cd "$repo_root"

base_ref="${1:-}"
if [[ -n "$base_ref" && "${HELM_CHECK_FORCE:-0}" != 1 ]]; then
  changed="$(git diff --name-only "$base_ref"...HEAD -- deploy/helm scripts/deploy/test-helm.sh scripts/ci/helm-chart-check.sh)"
  if [[ -z "$changed" ]]; then
    echo "helm-chart-check: no chart or harness change against $base_ref; nothing to render"
    exit 0
  fi
fi

image="registry.example/acr-api@sha256:$(printf 'a%.0s' {1..64})"
chart=deploy/helm/acr

echo "== default values"
helm template acr-test "$chart" --namespace acr-test \
  --set-string "image.reference=$image" \
  --set-string "credentials.runtime.existingSecret=acr-runtime" \
  --set-string "credentials.migration.existingSecret=acr-migration" \
  --set-string "config.deviceVerificationUrl=https://acr.example/device" \
  --set-string "config.entitlement.url=https://ops.dev-health.internal" \
  --set-string "credentials.entitlementToken.existingSecret=acr-entitlement-token" >/dev/null

echo "== development values (harness happy path)"
bash scripts/deploy/test-helm.sh --values "$chart/values-development.yaml" --image "$image"

# shellcheck disable=SC2016  # the sed pattern is literal text, not a shell expansion
mapfile -t scenarios < <(sed -nE '/^case "\$scenario" in$/,/^esac$/ s/^  ([a-z0-9-]+)\)$/\1/p' scripts/deploy/test-helm.sh | grep -vx happy)
[[ "${#scenarios[@]}" -gt 0 ]] || { echo "helm-chart-check: found no scenarios in test-helm.sh" >&2; exit 1; }
for scenario in "${scenarios[@]}"; do
  bash scripts/deploy/test-helm.sh --values "$chart/values-development.yaml" --image "$image" --scenario "$scenario"
done
echo "helm-chart-check: default, development and ${#scenarios[@]} negative scenarios passed"
