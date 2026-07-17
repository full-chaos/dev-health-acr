#!/usr/bin/env bash
# Verify the private ACR Helm chart against the pinned Todo 18 Kind/TLS fixture.
#
# This script owns only its generated Helm namespace and local OCI archives. The
# Kind fixture remains caller-owned and is never created or destroyed here.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
FIXTURE_SCRIPT="${SCRIPT_DIR}/kind-fixture.sh"
STATE_ROOT="${ACR_E2E_STATE_ROOT:-${REPO_ROOT}/.tmp/e2e}"
CHART="${REPO_ROOT}/deploy/helm/acr"
EVIDENCE_FILE="${REPO_ROOT}/.omo/evidence/task-19-acr-project-completion.txt"

cluster=""
scenario="lifecycle"
previous_image=""
run_id="${ACR_E2E_RUN_ID:-helm-${RANDOM}-${RANDOM}-$$}"
namespace=""
release=""
run_dir=""
fixture_id=""
owned_namespace=0
namespace_labelled=0
cleanup_started=0
port_forward_pids=()
queued_evidence_result=""
queued_evidence_detail=""
imported_image_refs=()
kind_image_refs_before=""
remote_archives=()
source_hash=""
git_commit_sha=""
git_head_tree_sha=""
git_index_tree_sha=""
git_provenance_captured_at_utc=""
built_image_ref=""
published_image_ref=""
expected_migration_failure_marker=""
TIMEOUT_TERM_GRACE_SECONDS="${ACR_E2E_TIMEOUT_TERM_GRACE_SECONDS:-5}"

log() { printf '[kind-helm] %s\n' "$*" >&2; }
fail() { printf '[kind-helm] FAIL: %s\n' "$*" >&2; }
die() { fail "$*"; exit 2; }

usage() {
  cat >&2 <<'EOF'
Usage: kind-helm.sh --cluster <kind-cluster> --scenario <name> [--previous-image <immutable-ref>]

Scenarios:
  lifecycle, bad-migration, missing-secret, missing-image-pull-secret,
  unprogrammed-gateway, denied-egress, app-rollback, static

The app-rollback scenario requires an explicitly preloaded --previous-image
immutable digest. Rollback changes only the application image; migration
history must stay unchanged and is reported as such (it never claims a schema
rollback). The comparison does not establish compatibility across source
revisions unless the caller supplies an image built from that prior revision.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cluster) cluster="${2:-}"; shift 2 ;;
    --cluster=*) cluster="${1#*=}"; shift ;;
    --scenario) scenario="${2:-}"; shift 2 ;;
    --scenario=*) scenario="${1#*=}"; shift ;;
    --previous-image) previous_image="${2:-}"; shift 2 ;;
    --previous-image=*) previous_image="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

case "${scenario}" in
  lifecycle|bad-migration|missing-secret|missing-image-pull-secret|unprogrammed-gateway|denied-egress|app-rollback|static) ;;
  *) die "unknown scenario: ${scenario}" ;;
esac

validate_identifier() {
  [[ "$1" =~ ^[a-z0-9][a-z0-9-]{1,48}$ ]] || die "invalid identifier: $1"
}

kube() {
  if [[ -n "${source_hash}" ]]; then
    assert_source_guard
  fi
  kubectl --context "kind-${cluster}" --request-timeout=10s "$@"
}

kube_cleanup() { kubectl --context "kind-${cluster}" --request-timeout=10s "$@"; }

write_evidence() {
  local result="$1" detail="$2"
  if [[ -n "${source_hash}" ]]; then
    assert_source_guard
    assert_clean_git_provenance
  fi
  mkdir -p "$(dirname "${EVIDENCE_FILE}")"
  umask 077
  {
    printf 'scenario=%s\n' "${scenario}"
    printf 'result=%s\n' "${result}"
    printf 'cluster=%s\n' "${cluster:-static}"
    printf 'namespace=%s\n' "${namespace:-none}"
    printf 'release=%s\n' "${release:-none}"
    printf 'fixture_id=%s\n' "${fixture_id:-none}"
    printf 'commit_sha=%s\n' "${git_commit_sha:-none}"
    printf 'head_tree_sha=%s\n' "${git_head_tree_sha:-none}"
    printf 'index_tree_sha=%s\n' "${git_index_tree_sha:-none}"
    printf 'working_tree_clean=%s\n' "$( [[ -n "${source_hash}" ]] && printf true || printf false )"
    printf 'index_clean=%s\n' "$( [[ -n "${source_hash}" ]] && printf true || printf false )"
    printf 'git_provenance_captured_at_utc=%s\n' "${git_provenance_captured_at_utc:-none}"
    printf 'source_hash=%s\n' "${source_hash:-none}"
    printf 'detail=%s\n' "${detail}"
    printf 'recorded_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '%s\n' '---'
  } >>"${EVIDENCE_FILE}"
}

queue_evidence() {
  queued_evidence_result="$1"
  queued_evidence_detail="$2"
}

stop_port_forwards() {
  local pid status=0 wait_status was_running
  for pid in "${port_forward_pids[@]}"; do
    was_running=0
    if kill -0 "${pid}" 2>/dev/null; then
      was_running=1
      kill "${pid}" 2>/dev/null || status=1
      if ! wait_for_process_exit "${pid}" 10; then
        kill -KILL "${pid}" 2>/dev/null || status=1
        wait_for_process_exit "${pid}" 5 || status=1
      fi
    fi
    if wait "${pid}" 2>/dev/null; then
      :
    else
      wait_status=$?
      if [[ "${was_running}" -ne 1 || ( "${wait_status}" -ne 143 && "${wait_status}" -ne 137 ) ]]; then
        status=1
      fi
    fi
  done
  port_forward_pids=()
  return "${status}"
}

wait_for_process_exit() {
  local pid="$1" seconds="$2" attempt
  for ((attempt = 0; attempt < seconds * 4; attempt++)); do
    kill -0 "${pid}" 2>/dev/null || return 0
    sleep 0.25
  done
  return 1
}

cleanup_kind_images() {
  local node ref archive refs attempt status=0 remaining=0
  [[ -n "${cluster}" ]] || return 0
  node="${cluster}-control-plane"
  for attempt in 1 2 3; do
    for archive in "${remote_archives[@]}"; do
      docker exec "${node}" rm -f "${archive}" >/dev/null 2>&1 || [[ "${attempt}" -lt 3 ]] || status=1
    done
    refs="$(docker exec "${node}" ctr -n k8s.io images list -q)" || continue
    while IFS= read -r ref || [[ -n "${ref}" ]]; do
      is_cleanup_kind_image_ref "${ref}" || continue
      docker exec "${node}" ctr -n k8s.io images rm "${ref}" >/dev/null 2>&1 || :
    done <<<"${refs}"
  done
  refs="$(docker exec "${node}" ctr -n k8s.io images list -q)" || return 1
  while IFS= read -r ref || [[ -n "${ref}" ]]; do
    if is_cleanup_kind_image_ref "${ref}"; then
      fail "Kind image cleanup left owned reference: ${ref}"
      remaining=1
    elif is_untracked_kind_image_delta "${ref}"; then
      fail "Kind image cleanup left untracked created reference: ${ref}"
      remaining=1
    fi
  done <<<"${refs}"
  [[ "${remaining}" -eq 0 ]] || return 1
  return "${status}"
}

assert_kind_images_absent() {
  local ref
  shift
  for ref in "$@"; do
    grep -Fxq "${ref}" <<<"${kind_image_refs_before}" && die "refusing to reuse a pre-existing Kind image reference: ${ref}"
  done
  return 0
}

preflight_cleanup_ownership() {
  local node ref repo
  node="${cluster}-control-plane"
  repo="acr-e2e.local/acr-api-${run_id}"
  kind_image_refs_before="$(docker exec "${node}" ctr -n k8s.io images list -q)" || \
    die "could not inspect Kind image ownership before arming cleanup"
  while IFS= read -r ref || [[ -n "${ref}" ]]; do
    [[ "${ref}" == "${repo}:"* || "${ref}" == "${repo}@"* ]] || continue
    die "refusing reused Kind image alias: ${ref}"
  done <<<"${kind_image_refs_before}"
}

record_imported_image_refs() {
  local repo="$1" node ref refs
  node="${cluster}-control-plane"
  refs="$(docker exec "${node}" ctr -n k8s.io images list -q)" || return 1
  while IFS= read -r ref || [[ -n "${ref}" ]]; do
    [[ "${ref}" == "${repo}:"* || "${ref}" == "${repo}@"* ]] || continue
    grep -Fxq "${ref}" <<<"${kind_image_refs_before}" && continue
    register_imported_image_ref "${ref}"
  done <<<"${refs}"
}

register_imported_image_ref() {
  local ref="$1" recorded
  for recorded in "${imported_image_refs[@]}"; do
    [[ "${recorded}" == "${ref}" ]] && return 0
  done
  imported_image_refs+=("${ref}")
}

is_cleanup_kind_image_ref() {
  local ref="$1" recorded
  for recorded in "${imported_image_refs[@]}"; do
    [[ "${recorded}" == "${ref}" ]] && return 0
  done
  return 1
}

is_untracked_kind_image_delta() {
  local ref="$1"
  [[ "${ref}" == "acr-e2e.local/acr-api-${run_id}:"* || "${ref}" == "acr-e2e.local/acr-api-${run_id}@"* ]] || return 1
  ! grep -Fxq "${ref}" <<<"${kind_image_refs_before}"
}

cleanup_namespace() {
  [[ "${owned_namespace}" -eq 1 ]] || return 0
  [[ "${cleanup_started}" -eq 0 ]] || return 0
  cleanup_started=1

  local actual attempt
  actual="$(kube_cleanup get namespace "${namespace}" -o jsonpath='{.metadata.labels.acr-e2e\.fullchaos\.dev/run-id}' 2>/dev/null || true)"
  if [[ "${actual}" != "${run_id}" && "${namespace_labelled}" -eq 1 ]]; then
    fail "refusing to delete namespace ${namespace}: exact run ownership label is absent"
    return 1
  fi
  if [[ "${actual}" != "${run_id}" ]]; then
    log "deleting namespace created before its ownership label was recorded"
  fi

  for attempt in 1 2; do
    if kube_cleanup delete namespace "${namespace}" --wait=false >/dev/null && \
      kube_cleanup wait --for=delete "namespace/${namespace}" --timeout=180s >/dev/null; then
      owned_namespace=0
      log "owned namespace ${namespace} deleted (attempt ${attempt})"
      return 0
    fi
    fail "owned namespace cleanup attempt ${attempt} failed for ${namespace}"
  done
  return 1
}

on_exit() {
  local original_status="$1" cleanup_status=0
  trap - EXIT
  if ! stop_port_forwards; then cleanup_status=1; fi
  if ! cleanup_namespace; then cleanup_status=1; fi
  if ! cleanup_kind_images; then cleanup_status=1; fi
  if [[ -n "${run_dir:-}" ]] && ! rm -rf "${run_dir}"; then cleanup_status=1; fi
  if [[ -n "${queued_evidence_result}" ]]; then
    if [[ "${cleanup_status}" -ne 0 ]]; then
      queued_evidence_result="failed"
      queued_evidence_detail="${queued_evidence_detail}; owned resource cleanup failed"
    fi
    if ! write_evidence "${queued_evidence_result}" "${queued_evidence_detail}"; then
      cleanup_status=1
      fail "could not record scenario evidence"
    fi
  fi
  if [[ "${original_status}" -ne 0 ]]; then exit "${original_status}"; fi
  exit "${cleanup_status}"
}

queue_expected_failure() {
  local boundary="$1" detail="$2"
  queue_evidence expected-failure "expected_failure_proven=true boundary=${boundary}; ${detail}"
}

static_check() {
  local failures=0
  check_static() {
    local expression="$1" description="$2" file="$3"
    if grep -Eq -- "${expression}" "${file}"; then
      log "static ok: ${description}"
    else
      fail "static: ${description}"
      failures=$((failures + 1))
    fi
  }
  function_block() {
    local function_name="$1"
    awk -v name="${function_name}" '
      $0 == (name "() {") { inside = 1 }
      inside { print }
      inside && $0 == "}" { exit }
    ' "${BASH_SOURCE[0]}"
  }
  assert_helm_context() {
    local function_name block literal_dollar='$' context_token
    context_token="--kube-context \"kind-${literal_dollar}{cluster}\""
    for function_name in run_helm run_helm_failure assert_migration_completed; do
      block="$(function_block "${function_name}")"
      if ! grep -Fq -- "${context_token}" <<<"${block}"; then
        fail "static: ${function_name} must pin Helm to kind-${cluster}"
        failures=$((failures + 1))
      fi
    done
  }
  assert_missing_image_pull_secret_boundary() {
    local block token literal_dollar='$'
    block="$(function_block assert_missing_image_pull_secret)"
    for token in "get \"job/${literal_dollar}{job}\"" "assert_no_deployment_ready" "FailedToRetrieveImagePullSecret" "involvedObject.name=${literal_dollar}{pod}" "assert_anonymous_registry_pull_denied"; do
      if ! grep -Fq -- "${token}" <<<"${block}"; then
        fail "static: missing imagePullSecret must assert a Kubernetes retrieval warning on a ready local image"
        failures=$((failures + 1))
        return
      fi
    done
  }
  assert_unprogrammed_gateway_boundary() {
    local block token
    block="$(function_block wait_gateway_programmed_false)"
    for token in 'observedGeneration' 'seq 1 60' 'unprogrammed Gateway did not reconcile Programmed=False'; do
      if ! grep -Fq -- "${token}" <<<"${block}"; then
        fail "static: unprogrammed Gateway must wait for a reconciled Programmed=False condition"
        failures=$((failures + 1))
        return
      fi
    done
    block="$(function_block run_unprogrammed_gateway)"
    for token in "gatewayClassName: \${ACR_E2E_GATEWAY_CLASS}" 'certificateRefs:' 'protocol: HTTPS' '"https"' 'wait_gateway_programmed_false'; do
      if ! grep -Fq -- "${token}" <<<"${block}"; then
        fail "static: unprogrammed Gateway must deploy a valid absent class before polling"
        failures=$((failures + 1))
        return
      fi
    done
    if grep -Fq 'observedGeneration' <<<"${block}"; then
      fail "static: unprogrammed Gateway reconciliation must not require an immediate condition"
      failures=$((failures + 1))
    fi
  }
  assert_fixture_entitlement_port_contract() {
    local fixture_block values_block literal_dollar='$'
    fixture_block="$(function_block create_fixture_references)"
    values_block="$(function_block write_values)"
    grep -Fq -- "entitlement_url=\"https://${literal_dollar}{ACR_E2E_OPS_ENTITLEMENT_HOST}:${literal_dollar}{ACR_E2E_OPS_ENTITLEMENT_PORT}\"" <<<"${fixture_block}" || {
      fail 'static: entitlement URL must use the fixture host and port exports'
      failures=$((failures + 1))
    }
    grep -Fq -- "entitlementPort: ${literal_dollar}{ACR_E2E_OPS_ENTITLEMENT_PORT}" <<<"${values_block}" || {
      fail 'static: API NetworkPolicy must allow the fixture entitlement port'
      failures=$((failures + 1))
    }
  }
  assert_denied_egress_failure_marker() {
    local block dsn marker literal_dollar='$'
    block="$(function_block run_denied_egress)"
    dsn="expected_migration_failure_dsn=\"postgres://postgres:acr-e2e-pass@postgres.${literal_dollar}{ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt\""
    marker='expected_migration_failure_marker="PostgreSQL is unavailable"'
    for token in "${dsn}" "${marker}" assert_failed_migration_hook; do
      grep -Fq -- "${token}" <<<"${block}" || {
        fail 'static: denied-egress must initialize its exact causal failure marker before asserting the failed migration hook'
        failures=$((failures + 1))
        return
      }
    done
  }
  assert_kind_image_cleanup_aliases() {
    local block literal_dollar='$'
    block="$(function_block build_local_image)"
    if ! grep -Fq "register_imported_image_ref \"${literal_dollar}{tag}\"" <<<"${block}" || \
      ! grep -Fq "register_imported_image_ref \"${literal_dollar}{repo}@${literal_dollar}{digest}\"" <<<"${block}" || \
      ! grep -Fq "if ! docker exec \"${literal_dollar}{node}\" ctr -n k8s.io images import" <<<"${block}" || \
      ! grep -Fq "if ! docker exec \"${literal_dollar}{node}\" ctr -n k8s.io images tag" <<<"${block}"; then
      fail 'static: Kind aliases must be registered before each mutable import/tag operation'
      failures=$((failures + 1))
    fi
    if [[ "$(grep -Fc "record_imported_image_refs \"${literal_dollar}{repo}\"" <<<"${block}")" -lt 3 ]]; then
      fail 'static: each import/tag outcome must reconcile every new exact Kind image alias for cleanup'
      failures=$((failures + 1))
    fi
  }
  check_static 'ctr -n k8s.io images import --base-name.*--digests' 'imports a local OCI archive into Kind under its exact digest' "${BASH_SOURCE[0]}"
  check_static 'imagePullSecrets' 'asserts existing imagePullSecret use' "${BASH_SOURCE[0]}"
  check_static 'schema_migrations' 'records migration state without a schema rollback claim' "${BASH_SOURCE[0]}"
  check_static 'acr-mcp' 'checks namespace inventory has no MCP workload' "${BASH_SOURCE[0]}"
  check_static 'cleanup_namespace' 'cleanup is owned-namespace-only and retried' "${BASH_SOURCE[0]}"
  check_static 'run_helm_failure' 'failure scenarios retain failed workloads for boundary assertions before namespace cleanup' "${BASH_SOURCE[0]}"
  check_static 'stop_port_forwards' 'port-forwards are tracked and stopped during cleanup' "${BASH_SOURCE[0]}"
  check_static 'queue_evidence' 'live evidence is queued until cleanup completes' "${BASH_SOURCE[0]}"
  check_static 'cleanup_kind_images' 'only run-owned imported Kind image references and remote archives are removed' "${BASH_SOURCE[0]}"
  check_static 'pod-security\.kubernetes\.io/enforce=restricted' 'creates a Restricted Pod Security Admission test namespace' "${BASH_SOURCE[0]}"
  check_static 'cannot inspect TLS or restrict a' 'documents the port-only NetworkPolicy TLS-destination limitation' "${CHART}/templates/networkpolicy.yaml"
  assert_helm_context
  assert_missing_image_pull_secret_boundary
  assert_unprogrammed_gateway_boundary
  assert_fixture_entitlement_port_contract
  assert_denied_egress_failure_marker
  assert_kind_image_cleanup_aliases
  check_static 'postgresCaBundle' 'chart mounts the PostgreSQL CA referenced by verified DSNs' "${CHART}/templates/deployment.yaml"
  check_static 'tcp_port_secure' 'fixture exposes TLS ClickHouse native transport' "${FIXTURE_SCRIPT}"
  check_static 'acr-e2e\.fullchaos\.dev/fixture-id' 'fixture permits only explicitly labelled consumer namespaces' "${FIXTURE_SCRIPT}"
  if [[ "${failures}" -ne 0 ]]; then
    write_evidence failed "static checks failed (${failures})"
    return 1
  fi
  write_evidence passed 'static chart/fixture lifecycle guards present'
}

require_tools() {
  local tool
  for tool in docker helm jq kind kubectl tar shasum; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required"
  done
}

source_tree_hash() {
  (
    cd "${REPO_ROOT}"
    {
      printf '%s\0' Dockerfile .dockerignore go.mod go.sum scripts/e2e/kind-helm.sh scripts/e2e/kind-fixture.sh scripts/e2e/pins.env
      find cmd -type f -name '*.go' -print0
      find internal -type f \( -name '*.go' -o -name '*.json' \) -print0
      find migrations -type f \( -name '*.go' -o -name '*.sql' \) -print0
      find deploy/helm scripts/container -type f -print0
    } | LC_ALL=C sort -z | while IFS= read -r -d '' path; do
      [[ -f "${path}" && ! -L "${path}" ]] || exit 1
      digest="$(shasum -a 256 -- "${path}")" || exit 1
      printf '%s\0%s\0' "${path}" "${digest%% *}"
    done | shasum -a 256 | awk '{print $1}'
  )
}

capture_clean_git_provenance() {
  local status
  git -C "${REPO_ROOT}" diff --quiet -- || die "live scenario refuses dirty working tree attribution"
  git -C "${REPO_ROOT}" diff --cached --quiet -- || die "live scenario refuses dirty index attribution"
  status="$(git -C "${REPO_ROOT}" status --porcelain=v1 --untracked-files=all)"
  [[ -z "${status}" ]] || die "live scenario refuses untracked working tree attribution"
  git_commit_sha="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
  git_head_tree_sha="$(git -C "${REPO_ROOT}" rev-parse "HEAD^{tree}")"
  git_index_tree_sha="$(git -C "${REPO_ROOT}" write-tree)"
  [[ "${git_commit_sha}" =~ ^[a-f0-9]{40}$ && "${git_head_tree_sha}" =~ ^[a-f0-9]{40}$ && "${git_index_tree_sha}" == "${git_head_tree_sha}" ]] || die "live scenario cannot establish exact clean Git provenance"
  git_provenance_captured_at_utc="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

assert_clean_git_provenance() {
  local status current_commit current_head_tree current_index_tree
  current_commit="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
  current_head_tree="$(git -C "${REPO_ROOT}" rev-parse "HEAD^{tree}")"
  current_index_tree="$(git -C "${REPO_ROOT}" write-tree)"
  [[ "${current_commit}" == "${git_commit_sha}" && "${current_head_tree}" == "${git_head_tree_sha}" && "${current_index_tree}" == "${git_index_tree_sha}" ]] || die "live scenario Git provenance changed after source guard establishment"
  git -C "${REPO_ROOT}" diff --quiet -- || die "live scenario working tree became dirty"
  git -C "${REPO_ROOT}" diff --cached --quiet -- || die "live scenario index became dirty"
  status="$(git -C "${REPO_ROOT}" status --porcelain=v1 --untracked-files=all)"
  [[ -z "${status}" ]] || die "live scenario acquired untracked source attribution"
}

establish_source_guard() {
  local baseline rechecked
  capture_clean_git_provenance
  baseline="$(source_tree_hash)" || die "could not hash tracked source before the live scenario"
  sleep 60
  rechecked="$(source_tree_hash)" || die "source tree changed during the 60-second quiescence window"
  [[ "${rechecked}" == "${baseline}" ]] || die "source tree hash changed during the 60-second quiescence window"
  source_hash="${baseline}"
  log "source quiescence established (sha256=${source_hash})"
}

assert_source_guard() {
  local current
  [[ -n "${source_hash}" ]] || die "source guard was not established"
  current="$(source_tree_hash)" || die "source tree changed after source guard establishment"
  [[ "${current}" == "${source_hash}" ]] || die "source tree hash changed after source guard establishment"
}

load_fixture_exports() {
  local file key value line
  file="${STATE_ROOT}/${cluster}/exports.env"
  [[ -f "${file}" ]] || die "fixture exports missing: ${file}; create the Todo 18 fixture first"
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -z "${line}" || "${line}" == \#* ]] && continue
    [[ "${line}" == *=* ]] || die "invalid fixture export line"
    key="${line%%=*}"
    value="${line#*=}"
    [[ "${value}" == '"'*'"' ]] || die "fixture export values must be double-quoted"
    value="${value:1:${#value}-2}"
    case "${key}" in
      ACR_KIND_CLUSTER|ACR_KIND_CONTEXT|ACR_E2E_DEPS_NAMESPACE|ACR_E2E_GATEWAY_NAMESPACE|ACR_E2E_GATEWAY_NAME|ACR_E2E_GATEWAY_CLASS|ACR_E2E_GATEWAY_HOSTNAME|ACR_E2E_GATEWAY_TLS_SECRET|ACR_E2E_POSTGRES_HOST|ACR_E2E_POSTGRES_PORT|ACR_E2E_POSTGRES_DB|ACR_E2E_CLICKHOUSE_HOST|ACR_E2E_CLICKHOUSE_HTTP_PORT|ACR_E2E_CLICKHOUSE_NATIVE_PORT|ACR_E2E_OPS_ENTITLEMENT_HOST|ACR_E2E_OPS_ENTITLEMENT_PORT|ACR_E2E_CA_CERT|ACR_E2E_IMAGE_PULL_SECRET|ACR_E2E_DOCKER_NETWORK|ACR_E2E_REGISTRY_NAME|ACR_E2E_REGISTRY_ENDPOINT)
        [[ "${value}" =~ ^[-A-Za-z0-9._/:@=]+$ ]] || die "unsafe fixture export value for ${key}"
        printf -v "${key}" '%s' "${value}"
        ;;
      *) die "unexpected fixture export key: ${key}" ;;
    esac
  done <"${file}"
  [[ "${ACR_KIND_CLUSTER:-}" == "${cluster}" ]] || die "fixture export cluster does not match --cluster"
  [[ -n "${ACR_E2E_DEPS_NAMESPACE:-}" && -n "${ACR_E2E_GATEWAY_NAMESPACE:-}" && -n "${ACR_E2E_CA_CERT:-}" ]] || die "fixture exports are incomplete"
  [[ -f "${ACR_E2E_CA_CERT}" ]] || die "fixture CA certificate is missing"
  fixture_id="$(awk -F= '$1 == "fixture_id" { print $2 }' "${STATE_ROOT}/${cluster}/fixture-identity.env" 2>/dev/null || true)"
  [[ "${fixture_id}" =~ ^[a-f0-9]{32}$ ]] || die "fixture identity is invalid"
}

assert_fixture_ready() {
  "${FIXTURE_SCRIPT}" verify --name "${cluster}"
  kube -n "${ACR_E2E_GATEWAY_NAMESPACE}" get gateway "${ACR_E2E_GATEWAY_NAME}" >/dev/null
}

prepare_run() {
  validate_identifier "${cluster}"
  validate_identifier "${run_id}"
  require_tools
  establish_source_guard
  kind get clusters | grep -Fxq "${cluster}" || die "Kind cluster not found: ${cluster}"
  load_fixture_exports
  assert_fixture_ready
  namespace="acr-${run_id}"
  release="acr-${run_id}"
  [[ "${#namespace}" -le 63 && "${#release}" -le 53 ]] || die "generated namespace or release name is too long"
  kube get namespace "${namespace}" >/dev/null 2>&1 && die "refusing reused namespace: ${namespace}"
  preflight_cleanup_ownership
  run_dir="$(mktemp -d "${STATE_ROOT}/kind-helm.${run_id}.XXXXXX")"
  trap 'on_exit "$?"' EXIT
  trap 'exit 130' INT TERM
  kube create namespace "${namespace}" >/dev/null
  owned_namespace=1
  kube label namespace "${namespace}" \
    "acr-e2e.fullchaos.dev/run-id=${run_id}" \
    "acr-e2e.fullchaos.dev/consumer-fixture-id=${fixture_id}" \
    "pod-security.kubernetes.io/enforce=restricted" \
    "pod-security.kubernetes.io/audit=restricted" \
    "pod-security.kubernetes.io/warn=restricted" --overwrite >/dev/null
  namespace_labelled=1
  assert_restricted_psa_enforced
  queue_evidence failed 'scenario ended before recording a successful result'
}

assert_restricted_psa_enforced() {
  local output
  output="$(kube -n "${namespace}" apply -f - 2>&1 <<'EOF' || true
apiVersion: v1
kind: Pod
metadata:
  name: restricted-psa-rejection
spec:
  restartPolicy: Never
  containers:
    - name: rejected
      image: busybox
      command: ["true"]
      securityContext:
        runAsUser: 0
EOF
)"
  grep -qi 'violates PodSecurity.*restricted' <<<"${output}" || {
    fail "Restricted Pod Security Admission did not reject a root Pod"; return 1;
  }
  log "Restricted Pod Security Admission enforcement is active in the owned namespace"
}

image_digest_from_oci() {
  local archive="$1" digest
  digest="$(tar -xOf "${archive}" index.json | jq -r '.manifests[0].digest // empty')"
  [[ "${digest}" =~ ^sha256:[a-f0-9]{64}$ ]] || die "OCI archive has no immutable manifest digest"
  printf '%s\n' "${digest}"
}

build_local_image() {
  local version="$1" archive repo tag digest node remote_archive
  archive="${run_dir}/acr-api-${version}.oci.tar"
  repo="acr-e2e.local/acr-api-${run_id}"
  tag="${repo}:${version}"
  assert_source_guard
  CONTAINER_ALLOW_DIRTY=1 \
    CONTAINER_OUTPUT=oci \
    CONTAINER_OCI_OUTPUT="${archive}" \
    CONTAINER_PLATFORMS="linux/$(docker version --format '{{.Server.Arch}}' | sed 's/aarch64/arm64/; s/x86_64/amd64/')" \
    CONTAINER_IMAGE="${tag}" \
    CONTAINER_VERSION="kind-${version}" \
    CONTAINER_BUILD_CACHE_ID="kind-helm-${run_id}-${version}" \
    "${REPO_ROOT}/scripts/container/build.sh" acr-api >/dev/null
  assert_source_guard
  digest="$(image_digest_from_oci "${archive}")"
  node="${cluster}-control-plane"
  remote_archive="/var/lib/acr-e2e/$(basename "${archive}")"
  assert_kind_images_absent "${node}" "${tag}" "${repo}@${digest}"
  register_imported_image_ref "${tag}"
  register_imported_image_ref "${repo}@${digest}"
  docker exec "${node}" mkdir -p /var/lib/acr-e2e
  remote_archives+=("${remote_archive}")
  docker cp "${archive}" "${node}:/var/lib/acr-e2e/"
  if ! docker exec "${node}" ctr -n k8s.io images import --base-name "${repo}" --digests "${remote_archive}" >/dev/null; then
    record_imported_image_refs "${repo}" || :
    die "could not import local OCI image into Kind"
  fi
  record_imported_image_refs "${repo}" || die "could not reconcile imported Kind image aliases"
  if ! docker exec "${node}" ctr -n k8s.io images tag "${tag}" "${repo}@${digest}" >/dev/null; then
    record_imported_image_refs "${repo}" || :
    die "could not tag imported Kind image"
  fi
  record_imported_image_refs "${repo}" || die "could not record imported Kind image references"
  docker exec "${node}" rm -f "${remote_archive}" >/dev/null
  docker exec "${node}" ctr -n k8s.io images list -q >"${run_dir}/kind-images-${version}.txt"
  grep -Fxq "${repo}@${digest}" "${run_dir}/kind-images-${version}.txt" || die "Kind node did not retain the exact local OCI image digest"
  built_image_ref="${repo}@${digest}"
}

create_fixture_references() {
  local runtime_dsn migration_dsn clickhouse_dsn entitlement_url
  assert_source_guard
  runtime_dsn="postgres://postgres:acr-e2e-pass@postgres.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
  migration_dsn="${runtime_dsn}"
  clickhouse_dsn="clickhouse://readonly@clickhouse.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local:${ACR_E2E_CLICKHOUSE_NATIVE_PORT}/default?secure=true"
  entitlement_url="https://${ACR_E2E_OPS_ENTITLEMENT_HOST}:${ACR_E2E_OPS_ENTITLEMENT_PORT}"

  kube -n "${namespace}" create secret generic acr-postgres-ca --from-file=ca.crt="${ACR_E2E_CA_CERT}" >/dev/null
  kube -n "${namespace}" create secret generic acr-clickhouse-ca --from-file=ca.crt="${ACR_E2E_CA_CERT}" >/dev/null
  kube -n "${namespace}" create secret generic acr-entitlement-ca --from-file=ca.crt="${ACR_E2E_CA_CERT}" >/dev/null
  kube -n "${namespace}" create secret generic acr-runtime \
    --from-literal=ACR_POSTGRES_DSN="${runtime_dsn}" \
    --from-literal=ACR_CLICKHOUSE_DSN="${clickhouse_dsn}" \
    --from-literal=ACR_EVIDENCE_ID_ACTIVE_KID=current \
    --from-literal=ACR_EVIDENCE_ID_KEYS='current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=' >/dev/null
  kube -n "${namespace}" create secret generic acr-migration \
    --from-literal=ACR_POSTGRES_MIGRATION_DSN="${migration_dsn}" >/dev/null
  kube -n "${namespace}" create secret generic acr-entitlement-token --from-literal=token=acr-e2e-ops-token-initial >/dev/null
  kube -n "${namespace}" create secret docker-registry "${ACR_E2E_IMAGE_PULL_SECRET}" \
    --docker-server="${ACR_E2E_REGISTRY_ENDPOINT}" --docker-username=fixture --docker-password=fixture >/dev/null
  printf '%s\n' "${entitlement_url}"
}

validate_immutable_image_ref() {
  [[ "$1" =~ ^[^[:space:]]+@sha256:[a-f0-9]{64}$ ]] || die "image must be an immutable @sha256 reference"
}

run_with_timeout() {
  local seconds="$1" pid attempt wait_status
  shift
  "$@" &
  pid=$!
  for ((attempt = 0; attempt < seconds; attempt++)); do
    if ! kill -0 "${pid}" 2>/dev/null; then
      if wait "${pid}"; then
        return 0
      else
        wait_status=$?
        return "${wait_status}"
      fi
    fi
    sleep 1
  done
  kill -TERM "${pid}" 2>/dev/null || true
  if ! wait_for_process_exit "${pid}" "${TIMEOUT_TERM_GRACE_SECONDS}"; then
    kill -KILL "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
    return 124
  fi
  wait "${pid}" 2>/dev/null || true
  return 124
}

publish_image_to_fixture_registry() {
  local source="$1" digest repository target node
  validate_immutable_image_ref "${source}"
  digest="${source##*@}"
  repository="${source%%@*}"
  target="${ACR_E2E_REGISTRY_ENDPOINT}/${repository##*/}@${digest}"
  node="${cluster}-control-plane"
  imported_image_refs+=("${target}")
  docker exec "${node}" ctr -n k8s.io images tag "${source}" "${target}" >/dev/null
  if ! run_with_timeout 120 docker exec "${node}" ctr -n k8s.io images push --plain-http --user fixture:fixture "${target}" >/dev/null; then
    fail "timed out or failed while pushing ${target} to the fixture registry"
    return 1
  fi
  docker exec "${node}" ctr -n k8s.io images rm "${target}" >/dev/null
  published_image_ref="${target}"
}

write_values() {
  local image="$1" entitlement_url="$2" gateway_name="${3:-${ACR_E2E_GATEWAY_NAME}}" gateway_namespace="${4:-${ACR_E2E_GATEWAY_NAMESPACE}}" gateway_section_name="${5:-https}" target="${run_dir}/values.yaml"
  assert_source_guard
  cat >"${target}" <<EOF
image:
  reference: ${image}
  pullPolicy: IfNotPresent
imagePullSecrets:
  - name: ${ACR_E2E_IMAGE_PULL_SECRET}
config:
  environment: development
  logLevel: info
  requireBackingStores: true
  postgresConnectionKind: direct
  postgresCaBundle:
    existingSecret: acr-postgres-ca
    key: ca.crt
  clickhouseCaBundle:
    existingSecret: acr-clickhouse-ca
    key: ca.crt
  entitlementCaBundle:
    existingSecret: acr-entitlement-ca
    key: ca.crt
  entitlement:
    url: ${entitlement_url}
    timeout: 5s
credentials:
  runtime:
    existingSecret: acr-runtime
    postgresDsnKey: ACR_POSTGRES_DSN
    clickhouseDsnKey: ACR_CLICKHOUSE_DSN
    evidenceIdActiveKidKey: ACR_EVIDENCE_ID_ACTIVE_KID
    evidenceIdKeysKey: ACR_EVIDENCE_ID_KEYS
  migration:
    existingSecret: acr-migration
    postgresDsnKey: ACR_POSTGRES_MIGRATION_DSN
  entitlementToken:
    existingSecret: acr-entitlement-token
    key: token
deployment:
  replicaCount: 1
  podLabels:
    acr-e2e.fullchaos.dev/fixture-id: ${fixture_id}
serviceAccount:
  automountServiceAccountToken: false
gateway:
  enabled: true
  httpRoute:
    parentRefs:
      - name: ${gateway_name}
        namespace: ${gateway_namespace}
        sectionName: ${gateway_section_name}
    hostnames:
      - ${ACR_E2E_GATEWAY_HOSTNAME}
autoscaling:
  enabled: false
podDisruptionBudget:
  enabled: false
networkPolicy:
  enabled: true
  ingressNamespaceSelectors:
    - matchLabels:
        kubernetes.io/metadata.name: envoy-gateway-system
  egress:
    dns: true
    postgresPort: 5432
    clickhousePort: ${ACR_E2E_CLICKHOUSE_NATIVE_PORT}
    entitlementPort: ${ACR_E2E_OPS_ENTITLEMENT_PORT}
EOF
  printf '%s\n' "${target}"
}

run_helm() {
  local values="$1"; shift
  assert_source_guard
  helm upgrade --install "${release}" "${CHART}" --kube-context "kind-${cluster}" --namespace "${namespace}" \
    --values "${values}" --wait --wait-for-jobs --atomic --cleanup-on-fail --timeout 240s "$@"
}

run_helm_failure() {
  local values="$1"; shift
  assert_source_guard
  helm upgrade --install "${release}" "${CHART}" --kube-context "kind-${cluster}" --namespace "${namespace}" \
    --values "${values}" --wait --wait-for-jobs --timeout 90s "$@"
}

deployment_name() { printf '%s\n' "${release}"; }

wait_api_ready() {
  local deploy; deploy="$(deployment_name)"
  kube -n "${namespace}" rollout status "deployment/${deploy}" --timeout=180s >/dev/null
  kube -n "${namespace}" wait --for=condition=Available "deployment/${deploy}" --timeout=30s >/dev/null
}

migration_count() {
  kube -n "${ACR_E2E_DEPS_NAMESPACE}" exec deployment/postgres -- \
    psql -U postgres -d acr -Atqc 'SELECT count(*) FROM acr.schema_migrations' 2>/dev/null
}

migration_history_fingerprint() {
  kube -n "${ACR_E2E_DEPS_NAMESPACE}" exec deployment/postgres -- \
    psql -U postgres -d acr -Atqc "SELECT md5(string_agg(version::text || ':' || name || ':' || coalesce(checksum, ''), ',' ORDER BY version)) FROM acr.schema_migrations" 2>/dev/null
}

assert_migration_completed() {
  local count hooks
  count="$(migration_count)"
  [[ "${count}" =~ ^[1-9][0-9]*$ ]] || { fail "migration history was not created"; return 1; }
  hooks="$(helm --kube-context "kind-${cluster}" get hooks "${release}" --namespace "${namespace}")"
  grep -q 'pre-install,pre-upgrade' <<<"${hooks}" || { fail "Helm did not record migration hook ordering"; return 1; }
  log "migration hook completed before ready Deployment (schema versions=${count})"
}

assert_service_and_route() {
  local deploy route_status gateway_service local_port response ca
  deploy="$(deployment_name)"
  kube -n "${namespace}" get "service/${deploy}" >/dev/null
  kube -n "${namespace}" get "httproute/${deploy}" >/dev/null
  route_status="$(kube -n "${namespace}" get "httproute/${deploy}" -o jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}')"
  [[ "${route_status}" == "True" ]] || { fail "HTTPRoute is not Accepted=True"; return 1; }
  gateway_service="$(kube -n envoy-gateway-system get service -l "gateway.envoyproxy.io/owning-gateway-name=${ACR_E2E_GATEWAY_NAME},gateway.envoyproxy.io/owning-gateway-namespace=${ACR_E2E_GATEWAY_NAMESPACE}" -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "${gateway_service}" ]] || { fail "fixture Envoy Gateway Service is unavailable"; return 1; }
  local_port=$((20000 + RANDOM % 20000))
  kubectl --context "kind-${cluster}" --request-timeout=10s -n envoy-gateway-system port-forward "service/${gateway_service}" "${local_port}:443" >/dev/null 2>&1 &
  local port_forward_pid=$!
  port_forward_pids+=("${port_forward_pid}")
  local ready=0 attempts=0
  while [[ "${attempts}" -lt 40 ]]; do
    if (exec 3<>"/dev/tcp/127.0.0.1/${local_port}") 2>/dev/null; then ready=1; break; fi
    sleep 0.25
    attempts=$((attempts + 1))
  done
  if [[ "${ready}" -ne 1 ]]; then
    stop_port_forwards || fail "gateway port-forward cleanup failed after readiness timeout"
    fail "gateway port-forward did not become ready"
    return 1
  fi
  ca="${ACR_E2E_CA_CERT}"
  response="$(curl --noproxy '*' --silent --show-error --max-time 10 --cacert "${ca}" \
    --resolve "${ACR_E2E_GATEWAY_HOSTNAME}:${local_port}:127.0.0.1" \
    -o /dev/null -w '%{http_code}' "https://${ACR_E2E_GATEWAY_HOSTNAME}:${local_port}/api/v1/agent-context/capabilities" 2>&1 || true)"
  stop_port_forwards || { fail "gateway port-forward cleanup failed"; return 1; }
  [[ "${response}" == "401" || "${response}" == "403" ]] || { fail "Gateway route did not reach ACR's protected API (HTTP ${response})"; return 1; }
  log "Service and Programmed Gateway/HTTPRoute reached protected API over fixture TLS"
}

assert_image_and_inventory() {
  local deploy configured inventory
  deploy="$(deployment_name)"
  configured="$(kube -n "${namespace}" get "deployment/${deploy}" -o jsonpath='{.spec.template.spec.containers[?(@.name=="acr-api")].image}')"
  [[ "${configured}" == *'@sha256:'* ]] || { fail "Deployment is not configured with an immutable image digest"; return 1; }
  inventory="$(kube -n "${namespace}" get all,configmap,serviceaccount,networkpolicy,httproute -o name)"
  ! grep -qi 'acr-mcp' <<<"${inventory}" || { fail "MCP object exists in Helm namespace"; return 1; }
  kube -n "${namespace}" get secret "${ACR_E2E_IMAGE_PULL_SECRET}" >/dev/null
  [[ "$(kube -n "${namespace}" get "deployment/${deploy}" -o jsonpath='{.spec.template.spec.imagePullSecrets[0].name}')" == "${ACR_E2E_IMAGE_PULL_SECRET}" ]] || { fail "Deployment does not use the fixture imagePullSecret"; return 1; }
  log "exact application digest, existing imagePullSecret, and zero MCP inventory confirmed"
}

network_policy_probe() {
  local name="$1" command="$2" expected_exit="$3" component="${4:-api}" phase="" exit_code="" reason="" attempt image
  name="network-probe-${name}-${RANDOM}"
  image="${ACR_E2E_IMG_PROBE:-docker.io/library/busybox@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028}"
  validate_immutable_image_ref "${image}"
  kube -n "${namespace}" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  labels:
    app.kubernetes.io/name: acr
    app.kubernetes.io/instance: ${release}
    app.kubernetes.io/component: ${component}
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: probe
      image: ${image}
      command: ["sh", "-ec"]
      args:
        - >-
          ${command}
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: ["ALL"]
        readOnlyRootFilesystem: true
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile:
          type: RuntimeDefault
EOF
  for attempt in $(seq 1 30); do
    phase="$(kube -n "${namespace}" get "pod/${name}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    [[ "${phase}" == "Succeeded" || "${phase}" == "Failed" ]] && break
    sleep 1
  done
  exit_code="$(kube -n "${namespace}" get "pod/${name}" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}' 2>/dev/null || true)"
  reason="$(kube -n "${namespace}" get "pod/${name}" -o jsonpath='{.status.containerStatuses[0].state.terminated.reason}' 2>/dev/null || true)"
  kube -n "${namespace}" delete "pod/${name}" --wait=true >/dev/null
  if [[ "${expected_exit}" == "0" ]]; then
    [[ "${reason}" == "Completed" && "${exit_code}" == "0" ]]
  else
    [[ "${reason}" == "Error" && "${exit_code}" == "${expected_exit}" ]]
  fi
}

assert_network_policy() {
  local deploy denied
  deploy="$(deployment_name)"
  if ! network_policy_probe allow "nc -z -w 5 postgres.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local 5432" 0; then
    fail "NetworkPolicy blocked required PostgreSQL egress under Restricted Pod Security Admission"
    return 1
  fi
  if ! network_policy_probe deny "nc -z -w 5 clickhouse.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local 8123" 1; then
    fail "NetworkPolicy did not prove a completed forbidden ClickHouse plaintext connection failure"
    return 1
  fi
  denied="$(kube -n "${namespace}" get networkpolicy "${deploy}" -o jsonpath='{.spec.policyTypes[*]}')"
  [[ "${denied}" == *Egress* ]] || { fail "NetworkPolicy lacks default-deny egress"; return 1; }
  log "NetworkPolicy allows required TLS-native ports and denies plaintext ClickHouse; Kubernetes policy is port-only and cannot verify a TLS destination hostname"
}

upgrade_for_checksum_rollouts() {
  local values="$1" deploy config_before config_after credentials_before credentials_after
  deploy="$(deployment_name)"
  config_before="$(kube -n "${namespace}" get "deployment/${deploy}" -o jsonpath='{.spec.template.metadata.annotations.checksum/config}')"
  run_helm "${values}" --set-string config.logLevel=warn >/dev/null
  wait_api_ready
  config_after="$(kube -n "${namespace}" get "deployment/${deploy}" -o jsonpath='{.spec.template.metadata.annotations.checksum/config}')"
  [[ -n "${config_before}" && "${config_before}" != "${config_after}" ]] || { fail "ConfigMap checksum did not change during upgrade"; return 1; }
  credentials_before="$(kube -n "${namespace}" get "deployment/${deploy}" -o jsonpath='{.spec.template.metadata.annotations.checksum/credentials}')"
  kube -n "${namespace}" patch secret acr-runtime --type merge -p '{"stringData":{"rotation-marker":"rotated"}}' >/dev/null
  run_helm "${values}" --set-string credentials.rotationRevision=rotation-2 >/dev/null
  wait_api_ready
  credentials_after="$(kube -n "${namespace}" get "deployment/${deploy}" -o jsonpath='{.spec.template.metadata.annotations.checksum/credentials}')"
  [[ -n "${credentials_before}" && "${credentials_before}" != "${credentials_after}" ]] || { fail "Secret checksum did not change after explicit rotation revision"; return 1; }
  log "config and external-Secret checksum rollouts completed"
}

assert_no_deployment_ready() {
  local deploy; deploy="$(deployment_name)"
  ! kube -n "${namespace}" wait --for=condition=Available "deployment/${deploy}" --timeout=5s >/dev/null 2>&1 || {
    fail "expected failure left an Available API Deployment"; return 1;
  }
}

assert_failed_migration_hook() {
  local job pod exit_code hook migration_dsn command failure_output
  job="$(deployment_name)-migrate"
  kube -n "${namespace}" wait --for=condition=failed "job/${job}" --timeout=90s >/dev/null || {
    fail "migration failure did not leave a failed hook Job"; return 1;
  }
  hook="$(kube -n "${namespace}" get "job/${job}" -o jsonpath='{.metadata.annotations.helm\.sh/hook}')"
  [[ "${hook}" == "pre-install,pre-upgrade" ]] || { fail "failed Job is not the Helm migration hook"; return 1; }
  pod="$(kube -n "${namespace}" get pods -l "job-name=${job}" -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "${pod}" ]] || { fail "failed migration hook has no Pod"; return 1; }
  command="$(kube -n "${namespace}" get "pod/${pod}" -o jsonpath='{.spec.containers[?(@.name=="acr-migrate")].command[*]} {.spec.containers[?(@.name=="acr-migrate")].args[*]}')"
  [[ "${command}" == *'/usr/local/bin/acr-migrate up'* ]] || { fail "failed hook Pod does not execute acr-migrate up"; return 1; }
  migration_dsn="$(kube -n "${namespace}" get secret acr-migration -o jsonpath='{.data.ACR_POSTGRES_MIGRATION_DSN}' | base64 -d)"
  [[ "${migration_dsn}" == "${expected_migration_failure_dsn}" ]] || { fail "migration Secret does not contain the exact injected failure configuration"; return 1; }
  exit_code="$(kube -n "${namespace}" get "pod/${pod}" -o jsonpath='{.status.containerStatuses[?(@.name=="acr-migrate")].state.terminated.exitCode}')"
  [[ "${exit_code}" =~ ^[1-9][0-9]*$ ]] || { fail "failed migration hook did not terminate acr-migrate nonzero"; return 1; }
  failure_output="$(kube -n "${namespace}" logs "${pod}" -c acr-migrate --tail=50 2>&1 || true)"
  if grep -Fq -- "${expected_migration_failure_dsn}" <<<"${failure_output}" || grep -Fq -- 'acr-e2e-pass' <<<"${failure_output}" || grep -Fq -- 'postgres.invalid' <<<"${failure_output}"; then
    fail "migration hook exposed injected connection details in logs"
    return 1
  fi
  grep -Fq -- "${expected_migration_failure_marker}" <<<"${failure_output}" || { fail "migration hook did not report the injected fixture failure"; return 1; }
  assert_no_deployment_ready
}

assert_missing_runtime_secret() {
  local deploy pod waiting_reason events secret_name
  deploy="$(deployment_name)"
  secret_name="acr-runtime"
  kube -n "${namespace}" get "deployment/${deploy}" >/dev/null || { fail "missing runtime Secret did not create the API Deployment"; return 1; }
  assert_no_deployment_ready
  pod="$(kube -n "${namespace}" get pods -l "app.kubernetes.io/name=acr,app.kubernetes.io/instance=${release},app.kubernetes.io/component=api" -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "${pod}" ]] || { fail "missing runtime Secret has no API Pod"; return 1; }
  waiting_reason="$(kube -n "${namespace}" get "pod/${pod}" -o jsonpath='{.status.containerStatuses[?(@.name=="acr-api")].state.waiting.reason}' 2>/dev/null || true)"
  [[ "${waiting_reason}" == "CreateContainerConfigError" ]] || { fail "API Pod did not report CreateContainerConfigError for missing runtime Secret"; return 1; }
  events="$(kube -n "${namespace}" get events --field-selector "involvedObject.kind=Pod,involvedObject.name=${pod}" --sort-by=.metadata.creationTimestamp 2>/dev/null || true)"
  grep -Fqi "secret \"${secret_name}\" not found" <<<"${events}" || {
    fail "API Pod did not report missing Secret ${secret_name}"; return 1;
  }
}

assert_missing_image_pull_secret() {
  local deploy job pod events secret_name waiting_reason image
  deploy="$(deployment_name)"
  job="${deploy}-migrate"
  secret_name="${ACR_E2E_IMAGE_PULL_SECRET}"
  kube -n "${namespace}" get "job/${job}" >/dev/null || { fail "missing imagePullSecret did not create the migration hook Job"; return 1; }
  assert_no_deployment_ready
  pod="$(kube -n "${namespace}" get pods -l "job-name=${job}" -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "${pod}" ]] || { fail "missing imagePullSecret migration Job has no Pod"; return 1; }
  image="$(kube -n "${namespace}" get "pod/${pod}" -o jsonpath='{.spec.containers[?(@.name=="acr-migrate")].image}')"
  [[ "${image}" == "${ACR_E2E_REGISTRY_ENDPOINT}/"* ]] || { fail "migration Pod does not use the authenticated fixture registry"; return 1; }
  waiting_reason="$(kube -n "${namespace}" get "pod/${pod}" -o jsonpath='{.status.containerStatuses[?(@.name=="acr-migrate")].state.waiting.reason}' 2>/dev/null || true)"
  events="$(kube -n "${namespace}" get events --field-selector "involvedObject.kind=Pod,involvedObject.name=${pod}" --sort-by=.metadata.creationTimestamp 2>/dev/null || true)"
  if ! grep -Fqi 'FailedToRetrieveImagePullSecret' <<<"${events}" || ! grep -Fqi -- "${secret_name}" <<<"${events}"; then
    fail "missing imagePullSecret did not produce a FailedToRetrieveImagePullSecret event for ${secret_name}"; return 1;
  fi
  grep -Eq 'CreateContainerConfigError|ImagePullBackOff|ErrImagePull|FailedToRetrieveImagePullSecret' <<<"${waiting_reason}${events}" || {
    fail "migration Pod did not record a pull-secret boundary for acr-migrate"; return 1;
  }
  assert_anonymous_registry_pull_denied "${image}"
}

assert_anonymous_registry_pull_denied() {
  local image="$1" node status
  node="${cluster}-control-plane"
  status="$(docker exec "${node}" curl --noproxy '*' --silent --show-error --output /dev/null --write-out '%{http_code}' "http://${image%%/*}/v2/" 2>/dev/null || true)"
  [[ "${status}" == "401" ]] || { fail "fixture registry did not reject anonymous pull access for migration image"; return 1; }
}

assert_denied_migration_egress() {
  local deploy job pod policy policy_json pod_labels migration_dsn
  deploy="$(deployment_name)"
  job="${deploy}-migrate"
  pod="$(kube -n "${namespace}" get pods -l "job-name=${job}" -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "${pod}" ]] || { fail "denied egress has no migration hook Pod"; return 1; }
  policy="${deploy}-deny-postgres"
  policy_json="$(kube -n "${namespace}" get "networkpolicy/${policy}" -o json)" || {
    fail "denied egress has no migration NetworkPolicy"; return 1;
  }
  pod_labels="$(kube -n "${namespace}" get "pod/${pod}" -o json | jq -c '.metadata.labels')"
  jq -e --argjson labels "${pod_labels}" '
    .spec.podSelector.matchLabels | to_entries |
    all(. as $entry | $labels[$entry.key] == $entry.value)
  ' <<<"${policy_json}" >/dev/null || {
    fail "denied egress NetworkPolicy does not select the failed migration Pod"; return 1;
  }
  jq -e '
    ([.spec.policyTypes[]?] | index("Egress")) and
    ([.spec.egress[]?.ports[]? | select(.protocol == "TCP") | .port] | index(1) != null and index(5432) == null) and
    ([.spec.egress[]? | select((.ports // []) | any(.protocol == "TCP" and .port == 5432))] | length == 0)
  ' <<<"${policy_json}" >/dev/null || {
    fail "denied egress NetworkPolicy did not retain default deny while excluding PostgreSQL port 5432"; return 1;
  }
  migration_dsn="$(kube -n "${namespace}" get secret acr-migration -o jsonpath='{.data.ACR_POSTGRES_MIGRATION_DSN}' | base64 -d)"
  [[ "${migration_dsn}" == "postgres://postgres:acr-e2e-pass@postgres.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt" ]] || {
    fail "denied egress changed the verified migration DSN instead of isolating the NetworkPolicy"; return 1;
  }
  if ! network_policy_probe migration-deny "nc -z -w 5 postgres.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local 5432" 1 migration; then
    fail "selected denied-egress policy did not block an equivalent PostgreSQL connection"; return 1
  fi
  log "migration failure is causally supported by a selected default-deny NetworkPolicy: an equivalent PostgreSQL connection failed while the verified DSN remained unchanged"
}

inject_denied_migration_policy() {
  local deploy
  deploy="$(deployment_name)"
  kube -n "${namespace}" apply -f - >/dev/null <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ${deploy}-deny-postgres
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: acr
      app.kubernetes.io/instance: ${release}
      app.kubernetes.io/component: migration
  policyTypes: [Egress]
  egress:
    - ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
        - protocol: TCP
          port: 1
EOF
}

diagnose_workload_failure() {
  local pod diagnostics
  diagnostics="${EVIDENCE_FILE%.txt}-${run_id}-diagnostics.txt"
  mkdir -p "$(dirname "${diagnostics}")"
  umask 077
  {
    printf 'scenario=%s\ncluster=%s\nnamespace=%s\nrecorded_at_utc=%s\n' \
      "${scenario}" "${cluster}" "${namespace}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    kube -n "${namespace}" get pods -o wide || true
    kube -n "${namespace}" get events --sort-by=.metadata.creationTimestamp || true
  } >>"${diagnostics}"
  kube -n "${namespace}" get pods -o wide >&2 || true
  kube -n "${namespace}" get events --sort-by=.metadata.creationTimestamp >&2 || true
  while IFS= read -r pod || [[ -n "${pod}" ]]; do
    [[ -n "${pod}" ]] || continue
    {
      printf '\ninit_container=%s\n' "${pod}"
      kube -n "${namespace}" logs "${pod}" -c prepare-entitlement-token || true
      printf '\napi_container=%s\n' "${pod}"
      kube -n "${namespace}" get "pod/${pod}" -o jsonpath='{.status.containerStatuses[?(@.name=="acr-api")].state.terminated} {.status.containerStatuses[?(@.name=="acr-api")].lastState.terminated}{"\n"}' || true
      kube -n "${namespace}" logs "${pod}" -c acr-api || true
      kube -n "${namespace}" logs "${pod}" -c acr-api --previous || true
    } >>"${diagnostics}"
    kube -n "${namespace}" logs "${pod}" --all-containers=true >&2 || true
    kube -n "${namespace}" logs "${pod}" -c prepare-entitlement-token >&2 || true
  done < <(kube -n "${namespace}" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
}

run_lifecycle() {
  local previous current entitlement values history_before history_after configured
  entitlement="$(create_fixture_references)"
  build_local_image v1
  previous="${built_image_ref}"
  build_local_image v2
  current="${built_image_ref}"
  values="$(write_values "${previous}" "${entitlement}")"
  if ! run_helm "${values}" >/dev/null; then
    diagnose_workload_failure
    return 1
  fi
  wait_api_ready
  assert_migration_completed
  assert_service_and_route
  assert_image_and_inventory
  assert_network_policy
  upgrade_for_checksum_rollouts "${values}"
  run_helm "${values}" --set-string "image.reference=${current}" >/dev/null
  wait_api_ready
  configured="$(kube -n "${namespace}" get "deployment/$(deployment_name)" -o jsonpath='{.spec.template.spec.containers[?(@.name=="acr-api")].image}')"
  [[ "${configured}" == "${current}" ]] || { fail "immutable image upgrade did not reach requested digest"; return 1; }
  history_before="$(migration_history_fingerprint)"
  run_helm "${values}" --set-string "image.reference=${previous}" >/dev/null
  wait_api_ready
  history_after="$(migration_history_fingerprint)"
  configured="$(kube -n "${namespace}" get "deployment/$(deployment_name)" -o jsonpath='{.spec.template.spec.containers[?(@.name=="acr-api")].image}')"
  [[ "${configured}" == "${previous}" ]] || { fail "explicit application rollback did not restore previous digest"; return 1; }
  [[ -n "${history_before}" && "${history_before}" == "${history_after}" ]] || { fail "application rollback changed migration history; schema downgrade is prohibited"; return 1; }
  printf 'PREVIOUS_IMAGE_DIGEST=%s\n' "${previous}"
  printf 'CURRENT_IMAGE_DIGEST=%s\n' "${current}"
  queue_evidence passed "lifecycle exact_previous=${previous} exact_current=${current} migration_history_fingerprint_before=${history_before} migration_history_fingerprint_after=${history_after} migration_history_unchanged=true rollback_scope=application-only"
}

run_bad_migration() {
  local entitlement current values
  entitlement="$(create_fixture_references)"
  build_local_image bad-migration
  current="${built_image_ref}"
  expected_migration_failure_marker="PostgreSQL is unavailable"
  expected_migration_failure_dsn='postgres://postgres:acr-e2e-pass@postgres.invalid:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt'
  kube -n "${namespace}" delete secret acr-migration >/dev/null
  kube -n "${namespace}" create secret generic acr-migration \
    --from-literal="ACR_POSTGRES_MIGRATION_DSN=${expected_migration_failure_dsn}" >/dev/null
  values="$(write_values "${current}" "${entitlement}")"
  if run_helm_failure "${values}" --set migration.backoffLimit=0 --set migration.activeDeadlineSeconds=60 >/dev/null 2>&1; then
    fail "bad migration unexpectedly installed"
    return 1
  fi
  diagnose_workload_failure
  assert_failed_migration_hook
  queue_expected_failure bad-migration 'injected verified-TLS migration endpoint failed as PostgreSQL unavailable before API readiness'
  log 'expected failure proven: bad migration hook failed before application readiness'
  return 0
}

run_missing_secret() {
  local entitlement current values
  entitlement="$(create_fixture_references)"
  build_local_image missing-secret
  current="${built_image_ref}"
  kube -n "${namespace}" delete secret acr-runtime >/dev/null
  values="$(write_values "${current}" "${entitlement}")"
  if run_helm_failure "${values}" --timeout 45s >/dev/null 2>&1; then
    fail "missing runtime Secret unexpectedly installed"
    return 1
  fi
  diagnose_workload_failure
  assert_missing_runtime_secret
  queue_expected_failure missing-runtime-secret 'missing runtime Secret created an unready Deployment with a missing-Secret boundary'
  log 'expected failure proven: missing runtime Secret left the application unready'
  return 0
}

run_missing_image_pull_secret() {
  local entitlement current registry_image values
  entitlement="$(create_fixture_references)"
  build_local_image missing-image-pull-secret
  current="${built_image_ref}"
  publish_image_to_fixture_registry "${current}"
  registry_image="${published_image_ref}"
  kube -n "${namespace}" delete secret "${ACR_E2E_IMAGE_PULL_SECRET}" >/dev/null
  values="$(write_values "${current}" "${entitlement}")"
  if kube -n "${namespace}" get secret "${ACR_E2E_IMAGE_PULL_SECRET}" >/dev/null 2>&1; then
    fail "missing imagePullSecret fault was not injected"
    return 1
  fi
  if run_helm_failure "${values}" --set-string "image.reference=${registry_image}" --set-string image.pullPolicy=Always >/dev/null 2>&1; then
    diagnose_workload_failure
    fail "missing imagePullSecret unexpectedly installed after a forced pull from the fixture registry"
    return 1
  fi
  assert_missing_image_pull_secret
  queue_expected_failure missing-image-pull-secret "forced fixture-registry pull emitted a Kubernetes credential retrieval failure for ${ACR_E2E_IMAGE_PULL_SECRET}; no locally cached registry reference was used"
}

wait_gateway_programmed_false() {
  local gateway_name="$1" programmed observed_generation generation attempt
  for attempt in $(seq 1 60); do
    generation="$(kube -n "${namespace}" get "gateway/${gateway_name}" -o jsonpath='{.metadata.generation}')"
    programmed="$(kube -n "${namespace}" get "gateway/${gateway_name}" -o jsonpath='{.status.conditions[?(@.type=="Programmed")].status}')"
    observed_generation="$(kube -n "${namespace}" get "gateway/${gateway_name}" -o jsonpath='{.status.conditions[?(@.type=="Programmed")].observedGeneration}')"
    if [[ -n "${generation}" && "${programmed}" == "False" && "${observed_generation}" == "${generation}" ]]; then
      return 0
    fi
    sleep 1
  done
  fail "unprogrammed Gateway did not reconcile Programmed=False to its current generation"
  return 1
}

run_unprogrammed_gateway() {
  local entitlement current values gateway_name="unprogrammed-${run_id}"
  entitlement="$(create_fixture_references)"
  build_local_image unprogrammed-gateway
  current="${built_image_ref}"
  kube -n "${namespace}" apply -f - >/dev/null <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: ${gateway_name}
spec:
  gatewayClassName: ${ACR_E2E_GATEWAY_CLASS}
  listeners:
    - name: https
      protocol: HTTPS
      port: 8443
      tls:
        mode: Terminate
        certificateRefs:
          - kind: Secret
            name: missing-gateway-certificate
EOF
  values="$(write_values "${current}" "${entitlement}" "${gateway_name}" "${namespace}" "https")"
  run_helm "${values}" >/dev/null || { fail "unprogrammed Gateway prevented Helm installation before route status could be checked"; return 1; }
  wait_gateway_programmed_false "${gateway_name}"
  queue_expected_failure unprogrammed-gateway 'Gateway reconciled Programmed=False because its certificate reference is absent'
  log 'expected failure proven: reconciled Gateway remained unprogrammed because its certificate reference is absent'
  return 0
}

run_denied_egress() {
  local entitlement current values
  expected_migration_failure_marker="PostgreSQL is unavailable"
  expected_migration_failure_dsn="postgres://postgres:acr-e2e-pass@postgres.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
  entitlement="$(create_fixture_references)"
  build_local_image denied-egress
  current="${built_image_ref}"
  values="$(write_values "${current}" "${entitlement}")"
  inject_denied_migration_policy
  if run_helm_failure "${values}" --set networkPolicy.egress.postgresPort=1 --set migration.backoffLimit=0 --set migration.activeDeadlineSeconds=60 >/dev/null 2>&1; then
    fail "denied egress unexpectedly installed"
    return 1
  fi
  diagnose_workload_failure
  assert_failed_migration_hook
  assert_denied_migration_egress
  queue_expected_failure denied-migration-egress 'migration egress denial failed nonzero because its selected default-deny NetworkPolicy excludes verified PostgreSQL port 5432; an equivalent selected probe also failed; no Available API Deployment'
  log 'expected failure proven: migration egress is denied by its NetworkPolicy, not a changed DSN'
  return 0
}

run_app_rollback() {
  local current entitlement values history_before history_after configured
  [[ -n "${previous_image}" ]] || die "app-rollback requires an explicitly loaded --previous-image immutable digest"
  validate_immutable_image_ref "${previous_image}"
  docker exec "${cluster}-control-plane" ctr -n k8s.io images list -q | grep -Fxq "${previous_image}" \
    || die "app-rollback previous image is not loaded in the Todo 18 Kind node: ${previous_image}"
  entitlement="$(create_fixture_references)"
  build_local_image rollback-current
  current="${built_image_ref}"
  values="$(write_values "${current}" "${entitlement}")"
  if ! run_helm "${values}" >/dev/null; then
    diagnose_workload_failure
    return 1
  fi
  wait_api_ready
  history_before="$(migration_history_fingerprint)"
  if ! run_helm "${values}" --set-string "image.reference=${previous_image}" >/dev/null; then
    diagnose_workload_failure
    return 1
  fi
  wait_api_ready
  history_after="$(migration_history_fingerprint)"
  configured="$(kube -n "${namespace}" get "deployment/$(deployment_name)" -o jsonpath='{.spec.template.spec.containers[?(@.name=="acr-api")].image}')"
  [[ "${configured}" == "${previous_image}" ]] || { fail "rollback did not restore explicit previous application digest"; return 1; }
  [[ -n "${history_before}" && "${history_before}" == "${history_after}" ]] || { fail "rollback changed migration history; no schema downgrade is permitted"; return 1; }
  queue_evidence passed "application rollback restored=${previous_image} migration_history_fingerprint_before=${history_before} migration_history_fingerprint_after=${history_after} migration_history_unchanged=true rollback_scope=application-only same_source_scope=not_established"
}

if [[ "${ACR_E2E_LIB_ONLY:-0}" != 1 ]]; then
  if [[ "${scenario}" == "static" ]]; then
    static_check
    exit $?
  fi

  prepare_run
  case "${scenario}" in
    lifecycle) run_lifecycle ;;
    bad-migration) run_bad_migration ;;
    missing-secret) run_missing_secret ;;
    missing-image-pull-secret) run_missing_image_pull_secret ;;
    unprogrammed-gateway) run_unprogrammed_gateway ;;
    denied-egress) run_denied_egress ;;
    app-rollback) run_app_rollback ;;
  esac
fi
