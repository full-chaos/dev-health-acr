#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HARNESS="${ROOT}/scripts/e2e/kind-kustomize.sh"
LIBRARY="${ROOT}/scripts/e2e/kind-kustomize-lib.sh"
FIXTURE="${ROOT}/scripts/e2e/kind-fixture.sh"
APPLY="${ROOT}/deploy/kubernetes/acr/scripts/apply.sh"
DEPLOYMENT="${ROOT}/deploy/kubernetes/acr/base/deployment.yaml"
MIGRATION="${ROOT}/deploy/kubernetes/acr/base/migration-job.yaml"

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
  require_output "${output}" 'ok: stale migration status is rejected after replacement UID freshness'
  require_output "${output}" 'ok: denied migration egress is causally proven by the NetworkPolicy'
  require_output "${output}" 'ok: mutable production image is rejected before cluster access'
  require_output "${output}" 'ok: secret rotation changes bytes, content checksum, and consuming pod'
  require_output "${output}" 'ok: application rollback renders only the previous immutable Deployment image'
}

test_hardening_seams_are_present() {
  local expected_delete
  expected_delete='kubectl --namespace "$'
  expected_delete+='namespace" delete job/acr-migrate --ignore-not-found --wait=false'
  grep -Fq 'commit_sha=' "${FIXTURE}" || fail 'verification evidence does not record the exact commit SHA'
  grep -Fq "$expected_delete" "${APPLY}" || fail 'migration replacement delete is unbounded'
  if ! grep -Fq 'emptyDir:' "${DEPLOYMENT}" || ! grep -Fq 'medium: Memory' "${DEPLOYMENT}"; then fail 'deployment copied secret storage is not memory-backed'; fi
  if ! grep -Fq 'emptyDir:' "${MIGRATION}" || ! grep -Fq 'medium: Memory' "${MIGRATION}"; then fail 'migration writable storage is not memory-backed'; fi
  grep -Fq 'rotation_content' "${LIBRARY}" || fail 'secret rotation does not change fixture credential bytes'
  grep -Fq 'replacement_uid' "${HARNESS}" || fail 'stale migration scenario does not prove replacement UID freshness'
  grep -Fq 'old_complete' "${HARNESS}" || fail 'stale migration scenario does not observe stale completion'
  if ! grep -Fq 'NetworkPolicy' "${HARNESS}" || ! grep -Fq 'transport' "${HARNESS}"; then fail 'denied egress scenario does not prove transport causality'; fi
}

test_preexisting_namespace_is_not_deleted() {
  local state_root fake_bin log output status
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  log="${state_root}/kubectl.log"
  mkdir -p "${state_root}/fixture" "${fake_bin}"
  : >"${state_root}/fixture/ca.crt"
  cat >"${state_root}/fixture/exports.env" <<EOF
ACR_KIND_CONTEXT="fake-context"
ACR_E2E_DEPS_NAMESPACE="acr-deps"
ACR_E2E_GATEWAY_NAMESPACE="gateway-system"
ACR_E2E_GATEWAY_NAME="gateway"
ACR_E2E_GATEWAY_HOSTNAME="acr.example.test"
ACR_E2E_POSTGRES_HOST="postgres.example.test"
ACR_E2E_CLICKHOUSE_HOST="clickhouse.example.test"
ACR_E2E_OPS_ENTITLEMENT_HOST="ops.example.test"
ACR_E2E_CA_CERT="${state_root}/fixture/ca.crt"
ACR_E2E_REGISTRY_ENDPOINT="registry.example.test"
EOF
  cat >"${fake_bin}/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${KUBECTL_LOG}"
case " $* " in
  *" get namespace acr-deps "*) exit 0 ;;
  *" create namespace "*) exit 1 ;;
  *" delete namespace "*) exit 0 ;;
  *" wait --for=delete namespace/"*) exit 0 ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "${fake_bin}/kubectl"

  set +e
  output="$(PATH="${fake_bin}:${PATH}" KUBECTL_LOG="${log}" ACR_E2E_STATE_ROOT="${state_root}" bash "${HARNESS}" --cluster fixture --namespace acr-kustomize-owned-test 2>&1)"
  status=$?
  set -e

  [[ ${status} -ne 0 ]] || fail 'pre-existing namespace unexpectedly succeeded'
  grep -Fq 'namespace already exists' <<<"${output}" || fail 'pre-existing namespace failure was not reported'
  if grep -Fq 'delete namespace acr-kustomize-owned-test' "${log}"; then
    rm -rf "${state_root}"
    fail 'pre-existing namespace was deleted by cleanup'
  fi
  rm -rf "${state_root}"
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
test_hardening_seams_are_present
test_preexisting_namespace_is_not_deleted
printf 'RESULT: Kind Kustomize harness contract tests passed\n'
