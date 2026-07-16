#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HARNESS="${ROOT}/scripts/e2e/kind-kustomize.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

require_output() {
  local output="$1" expected="$2"
  grep -Fqx -- "$expected" <<<"${output}" || fail "missing self-test result: ${expected}"
}

test_self_test_covers_required_seams() {
  local output
  output="$(bash "${HARNESS}" --self-test)"

  require_output "${output}" 'ok: parity compares critical Helm and Kustomize fields'
  require_output "${output}" 'ok: stale migration status is rejected by current Job UID'
  require_output "${output}" 'ok: denied migration egress blocks rollout'
  require_output "${output}" 'ok: mutable production image is rejected before cluster access'
  require_output "${output}" 'ok: secret rotation changes the pod-template checksum'
  require_output "${output}" 'ok: application rollback renders only the previous immutable Deployment image'
}

test_mutable_image_is_rejected_before_cluster_access() {
  local output status
  set +e
  output="$(bash "${HARNESS}" --cluster missing-fixture --scenario mutable-production-image --image registry.invalid/acr-api:latest 2>&1)"
  status=$?
  set -e

  [[ ${status} -ne 0 ]] || fail 'mutable production image unexpectedly succeeded'
  grep -Fq 'mutable-production-image' <<<"${output}" || fail 'mutable production image rejection was not named'
  if grep -Fq 'cluster not found' <<<"${output}"; then
    fail 'mutable production image checked cluster before rejecting image'
  fi
}

test_self_test_covers_required_seams
test_mutable_image_is_rejected_before_cluster_access
printf 'RESULT: Kind Kustomize harness contract tests passed\n'
