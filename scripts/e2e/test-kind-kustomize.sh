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

test_self_test_requires_a_fixture_and_runs_scenarios() {
  local output status scenario
  set +e
  output="$(bash "${HARNESS}" --self-test 2>&1)"
  status=$?
  set -e

  [[ ${status} -ne 0 ]] || fail 'self-test accepted no fixture cluster'
  grep -Fq -- '--cluster is required' <<<"${output}" || fail 'self-test did not require a fixture cluster'
  grep -Fq 'run_behavioral_scenario' "${HARNESS}" || fail 'self-test does not execute child scenarios'
  for scenario in lifecycle stale-migration-status denied-egress mutable-production-image app-rollback; do
    grep -Fq "${scenario}" "${HARNESS}" || fail "self-test does not cover ${scenario}"
  done
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
  grep -Fq 'e2e_set_ops_entitlement_token' "${HARNESS}" || fail 'secret rotation does not rotate the accepted Ops token'
  grep -Fq 'e2e_application_readiness' "${HARNESS}" || fail 'secret rotation does not prove application-boundary token use'
  # shellcheck disable=SC2016 # Static assertion intentionally matches a literal shell expression.
  grep -Fq 'if (\$http_authorization != "Bearer ${token}")' "${LIBRARY}" || fail 'rotated Ops fixture emits an invalid authorization predicate'
  grep -Fq 'replacement_uid' "${HARNESS}" || fail 'stale migration scenario does not prove replacement UID freshness'
  grep -Fq 'old_complete' "${HARNESS}" || fail 'stale migration scenario does not observe stale completion'
  if ! grep -Fq 'NetworkPolicy' "${HARNESS}" || ! grep -Fq 'transport' "${HARNESS}"; then fail 'denied egress scenario does not prove transport causality'; fi
}

test_parity_keeps_dependency_port_meanings_separate() {
  local parity_tokens api_policy literal_dollar='$' fixture_policy_port helm_policy_port
  parity_tokens="$(grep 'for token in .*acr-migrate' "${HARNESS}")"
  api_policy="${ROOT}/deploy/kubernetes/acr/base/networkpolicy-api.yaml"
  [[ "${parity_tokens}" != *"port:"* ]] || fail 'port parity is coupled to untyped shared tokens'
  grep -Fq "verify_semantic_port_parity 'ClickHouse native TLS' 9440 acr-api" "${HARNESS}" || fail 'parity does not require ClickHouse native TLS port 9440 from the API policy'
  grep -Fq "verify_semantic_port_parity 'entitlement HTTPS' \"\${KUSTOMIZE_E2E_OPS_PORT}\" acr-api" "${HARNESS}" || fail 'parity does not derive entitlement HTTPS port from the fixture export'
  grep -Fq 'network_policy_tcp_ports' "${HARNESS}" || fail 'parity does not scope dependency ports to NetworkPolicy documents'
  grep -Fq "KUSTOMIZE_E2E_OPS_PORT=\"${literal_dollar}(e2e_fixture_value \"${literal_dollar}file\" ACR_E2E_OPS_ENTITLEMENT_PORT)\"" "${LIBRARY}" || fail 'Kustomize does not load the entitlement port from the fixture export'
  grep -Fq "https://${literal_dollar}{KUSTOMIZE_E2E_OPS_HOST}:${literal_dollar}{KUSTOMIZE_E2E_OPS_PORT}" "${LIBRARY}" || fail 'Kustomize entitlement URL does not use its semantic fixture port'
  grep -Fq 'port: 443' "${api_policy}" || fail 'API policy does not allow the base entitlement HTTPS port'
  grep -Fq 'path: /spec/egress/3/ports/0/port' "${LIBRARY}" || fail 'fixture does not patch the API entitlement policy port'
  fixture_policy_port="value: ${literal_dollar}{KUSTOMIZE_E2E_OPS_PORT}"
  helm_policy_port="entitlementPort: ${literal_dollar}{KUSTOMIZE_E2E_OPS_PORT}"
  grep -Fq "${fixture_policy_port}" "${LIBRARY}" || fail 'fixture policy does not source the entitlement port export'
  grep -Fq "${helm_policy_port}" "${HARNESS}" || fail 'Helm values do not source the entitlement port export'
}

test_base_and_fixture_entitlement_ports_render() {
  local state_root base_render fixture_render base_url fixture_url base_ports fixture_ports
  state_root="$(mktemp -d)"
  base_render="${state_root}/base.yaml"
  fixture_render="${state_root}/fixture.yaml"
  if command -v kustomize >/dev/null 2>&1; then
    kustomize build "${ROOT}/deploy/kubernetes/acr/base" >"${base_render}"
  else
    kubectl kustomize "${ROOT}/deploy/kubernetes/acr/base" >"${base_render}"
  fi
  base_url="$(yq -r 'select(.kind == "ConfigMap" and .metadata.name == "acr-config") | .data.ACR_DEV_HEALTH_ENTITLEMENT_URL' "${base_render}")"
  base_ports="$(yq -r 'select(.kind == "NetworkPolicy" and .metadata.name == "acr-api") | .spec.egress[]?.ports[]? | select(.protocol == "TCP") | .port' "${base_render}")"
  [[ "${base_url}" == 'https://ops.dev-health.internal:443' ]] || fail "base entitlement URL = ${base_url}"
  require_output "${base_ports}" 443
  require_output "${base_ports}" 9440
  if grep -Fqx 8443 <<<"${base_ports}"; then rm -rf "${state_root}"; fail 'base policy retains fixture entitlement port'; fi

  # shellcheck source=kind-kustomize-lib.sh
  # shellcheck disable=SC1091
  source "${LIBRARY}"
  export KUSTOMIZE_E2E_WORK="${state_root}/work"
  export KUSTOMIZE_E2E_NAMESPACE=acr-test
  export KUSTOMIZE_E2E_GATEWAY_NAMESPACE=envoy-gateway-system
  export KUSTOMIZE_E2E_OPS_HOST=ops.example.test
  export KUSTOMIZE_E2E_OPS_PORT=8443
  mkdir -p "${KUSTOMIZE_E2E_WORK}"
  e2e_render registry.example.test/acr-api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef test false
  cp "${KUSTOMIZE_E2E_WORK}/rendered.yaml" "${fixture_render}"
  fixture_url="$(yq -r 'select(.kind == "ConfigMap" and .metadata.name == "acr-config") | .data.ACR_DEV_HEALTH_ENTITLEMENT_URL' "${fixture_render}")"
  fixture_ports="$(yq -r 'select(.kind == "NetworkPolicy" and .metadata.name == "acr-api") | .spec.egress[]?.ports[]? | select(.protocol == "TCP") | .port' "${fixture_render}")"
  [[ "${fixture_url}" == 'https://ops.example.test:8443' ]] || fail "fixture entitlement URL = ${fixture_url}"
  require_output "${fixture_ports}" 8443
  require_output "${fixture_ports}" 9440
  if grep -Fqx 443 <<<"${fixture_ports}"; then rm -rf "${state_root}"; fail 'fixture policy retains base entitlement port'; fi
  rm -rf "${state_root}"
}

test_verification_evidence_requires_clean_git_provenance() {
  local token
  for token in verify_clean_git_provenance working_tree_clean=true index_clean=true head_tree_sha index_tree_sha; do
    grep -Fq "${token}" "${FIXTURE}" || fail "verification evidence omits clean Git provenance: ${token}"
  done
}

test_verification_provenance_rejects_dirty_worktree() {
  local state_root fake_bin output status
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/git" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" diff --quiet -- "*) exit 1 ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "${fake_bin}/git"
  set +e
  output="$(PATH="${fake_bin}:${PATH}" ACR_E2E_LIB_ONLY=1 bash -c 'source "$1"; verify_clean_git_provenance' -- "${FIXTURE}" 2>&1)"
  status=$?
  set -e
  rm -rf "${state_root}"

  [[ ${status} -ne 0 ]] || fail 'dirty working tree attribution unexpectedly succeeded'
  grep -Fq 'verification refuses dirty working tree attribution' <<<"${output}" || fail 'dirty working tree attribution did not fail closed'
}

test_verification_provenance_rejects_dirty_index() {
  local state_root fake_bin output status
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/git" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" diff --cached --quiet -- "*) exit 1 ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "${fake_bin}/git"
  set +e
  output="$(PATH="${fake_bin}:${PATH}" ACR_E2E_LIB_ONLY=1 bash -c 'source "$1"; verify_clean_git_provenance' -- "${FIXTURE}" 2>&1)"
  status=$?
  set -e
  rm -rf "${state_root}"

  [[ ${status} -ne 0 ]] || fail 'dirty index attribution unexpectedly succeeded'
  grep -Fq 'verification refuses dirty index attribution' <<<"${output}" || fail 'dirty index attribution did not fail closed'
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
ACR_E2E_CLICKHOUSE_NATIVE_PORT="9440"
ACR_E2E_OPS_ENTITLEMENT_HOST="ops.example.test"
ACR_E2E_OPS_ENTITLEMENT_PORT="8443"
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

test_clickhouse_dsn_uses_fixture_native_port() {
  local state_root fake_bin log
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
ACR_E2E_CLICKHOUSE_NATIVE_PORT="9440"
ACR_E2E_OPS_ENTITLEMENT_HOST="ops.example.test"
ACR_E2E_OPS_ENTITLEMENT_PORT="8443"
ACR_E2E_CA_CERT="${state_root}/fixture/ca.crt"
ACR_E2E_REGISTRY_ENDPOINT="registry.example.test"
EOF
  cat >"${fake_bin}/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${KUBECTL_LOG}"
EOF
  chmod +x "${fake_bin}/kubectl"

  PATH="${fake_bin}:${PATH}" KUBECTL_LOG="${log}" ACR_E2E_STATE_ROOT="${state_root}" \
    ACR_E2E_LIB_ONLY=1 bash -c 'source "$1"; KUSTOMIZE_E2E_NAMESPACE=acr-test; e2e_load_fixture fixture; e2e_create_secrets' -- "${LIBRARY}"
  grep -Fq 'clickhouse://default:@clickhouse.example.test:9440/default?secure=true&skip_verify=false&tls_server_name=clickhouse.example.test' "${log}" \
    || fail 'ClickHouse DSN does not use the fixture native TLS port'
  if grep -Fq 'clickhouse.example.test:8443' "${log}"; then
    rm -rf "${state_root}"
    fail 'ClickHouse DSN incorrectly uses the Ops entitlement port'
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

test_self_test_requires_a_fixture_and_runs_scenarios
test_mutable_image_is_rejected_before_cluster_access
test_hardening_seams_are_present
test_parity_keeps_dependency_port_meanings_separate
test_base_and_fixture_entitlement_ports_render
test_verification_evidence_requires_clean_git_provenance
test_verification_provenance_rejects_dirty_worktree
test_verification_provenance_rejects_dirty_index
test_preexisting_namespace_is_not_deleted
test_clickhouse_dsn_uses_fixture_native_port
printf 'RESULT: Kind Kustomize harness contract tests passed\n'
