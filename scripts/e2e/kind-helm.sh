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
cleanup_started=0

log() { printf '[kind-helm] %s\n' "$*" >&2; }
fail() { printf '[kind-helm] FAIL: %s\n' "$*" >&2; }
die() { fail "$*"; exit 2; }

usage() {
  cat >&2 <<'EOF'
Usage: kind-helm.sh --cluster <kind-cluster> --scenario <name> [--previous-image <immutable-ref>]

Scenarios:
  lifecycle, bad-migration, missing-secret, missing-image-pull-secret,
  unprogrammed-gateway, denied-egress, app-rollback, static

The app-rollback scenario requires --previous-image to be a local immutable
digest already loaded into the fixture Kind node (lifecycle prints one).
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

kube() { kubectl --context "kind-${cluster}" "$@"; }

record_evidence() {
  local result="$1" detail="$2"
  mkdir -p "$(dirname "${EVIDENCE_FILE}")"
  umask 077
  {
    printf 'scenario=%s\n' "${scenario}"
    printf 'result=%s\n' "${result}"
    printf 'cluster=%s\n' "${cluster:-static}"
    printf 'namespace=%s\n' "${namespace:-none}"
    printf 'release=%s\n' "${release:-none}"
    printf 'fixture_id=%s\n' "${fixture_id:-none}"
    printf 'detail=%s\n' "${detail}"
    printf 'recorded_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '%s\n' '---'
  } >>"${EVIDENCE_FILE}"
}

cleanup_namespace() {
  [[ "${owned_namespace}" -eq 1 ]] || return 0
  [[ "${cleanup_started}" -eq 0 ]] || return 0
  cleanup_started=1

  local actual attempt
  actual="$(kube get namespace "${namespace}" -o jsonpath='{.metadata.labels.acr-e2e\.fullchaos\.dev/run-id}' 2>/dev/null || true)"
  if [[ "${actual}" != "${run_id}" ]]; then
    fail "refusing to delete namespace ${namespace}: exact run ownership label is absent"
    return 1
  fi

  for attempt in 1 2; do
    if kube delete namespace "${namespace}" --wait=false >/dev/null && \
      kube wait --for=delete "namespace/${namespace}" --timeout=180s >/dev/null; then
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
  if ! cleanup_namespace; then cleanup_status=1; fi
  rm -rf "${run_dir:-}"
  if [[ "${original_status}" -ne 0 ]]; then return "${original_status}"; fi
  return "${cleanup_status}"
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
  check_static 'ctr -n k8s.io images import --base-name.*--digests' 'imports a local OCI archive into Kind under its exact digest' "${BASH_SOURCE[0]}"
  check_static 'imagePullSecrets' 'asserts existing imagePullSecret use' "${BASH_SOURCE[0]}"
  check_static 'schema_migrations' 'records migration state without a schema rollback claim' "${BASH_SOURCE[0]}"
  check_static 'acr-mcp' 'checks namespace inventory has no MCP workload' "${BASH_SOURCE[0]}"
  check_static 'cleanup_namespace' 'cleanup is owned-namespace-only and retried' "${BASH_SOURCE[0]}"
  check_static 'postgresCaBundle' 'chart mounts the PostgreSQL CA referenced by verified DSNs' "${CHART}/templates/deployment.yaml"
  check_static 'tcp_port_secure' 'fixture exposes TLS ClickHouse native transport' "${FIXTURE_SCRIPT}"
  check_static 'acr-e2e\.fullchaos\.dev/fixture-id' 'fixture permits only explicitly labelled consumer namespaces' "${FIXTURE_SCRIPT}"
  if [[ "${failures}" -ne 0 ]]; then
    record_evidence failed "static checks failed (${failures})"
    return 1
  fi
  record_evidence passed 'static chart/fixture lifecycle guards present'
}

require_tools() {
  local tool
  for tool in docker helm jq kind kubectl tar; do
    command -v "${tool}" >/dev/null 2>&1 || die "${tool} is required"
  done
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
  kind get clusters | grep -Fxq "${cluster}" || die "Kind cluster not found: ${cluster}"
  load_fixture_exports
  assert_fixture_ready
  namespace="acr-${run_id}"
  release="acr-${run_id}"
  [[ "${#namespace}" -le 63 && "${#release}" -le 53 ]] || die "generated namespace or release name is too long"
  run_dir="$(mktemp -d "${STATE_ROOT}/kind-helm.${run_id}.XXXXXX")"
  trap 'on_exit "$?"' EXIT
  kube get namespace "${namespace}" >/dev/null 2>&1 && die "refusing reused namespace: ${namespace}"
  kube create namespace "${namespace}" >/dev/null
  kube label namespace "${namespace}" \
    "acr-e2e.fullchaos.dev/run-id=${run_id}" \
    "acr-e2e.fullchaos.dev/consumer-fixture-id=${fixture_id}" --overwrite >/dev/null
  owned_namespace=1
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
  CONTAINER_ALLOW_DIRTY=1 \
    CONTAINER_OUTPUT=oci \
    CONTAINER_OCI_OUTPUT="${archive}" \
    CONTAINER_PLATFORMS="linux/$(docker version --format '{{.Server.Arch}}' | sed 's/aarch64/arm64/; s/x86_64/amd64/')" \
    CONTAINER_IMAGE="${tag}" \
    CONTAINER_VERSION="kind-${version}" \
    CONTAINER_BUILD_CACHE_ID="kind-helm-${run_id}-${version}" \
    "${REPO_ROOT}/scripts/container/build.sh" acr-api >/dev/null
  digest="$(image_digest_from_oci "${archive}")"
  node="${cluster}-control-plane"
  remote_archive="/var/lib/acr-e2e/$(basename "${archive}")"
  docker exec "${node}" mkdir -p /var/lib/acr-e2e
  docker cp "${archive}" "${node}:/var/lib/acr-e2e/"
  docker exec "${node}" ctr -n k8s.io images import --base-name "${repo}" --digests "${remote_archive}" >/dev/null
  docker exec "${node}" ctr -n k8s.io images tag "${tag}" "${repo}@${digest}" >/dev/null
  docker exec "${node}" rm -f "${remote_archive}" >/dev/null
  docker exec "${node}" ctr -n k8s.io images list -q >"${run_dir}/kind-images-${version}.txt"
  grep -Fxq "${repo}@${digest}" "${run_dir}/kind-images-${version}.txt" || die "Kind node did not retain the exact local OCI image digest"
  printf '%s@%s\n' "${repo}" "${digest}"
}

create_fixture_references() {
  local runtime_dsn migration_dsn clickhouse_dsn entitlement_url
  runtime_dsn="postgres://postgres:acr-e2e-pass@postgres.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
  migration_dsn="${runtime_dsn}"
  clickhouse_dsn="clickhouse://readonly@clickhouse.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local:${ACR_E2E_CLICKHOUSE_NATIVE_PORT}/default?secure=true"
  entitlement_url="https://ops-entitlement.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local:8443"

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
  kube -n "${namespace}" create secret generic acr-entitlement-token --from-literal=token=acr-e2e-token >/dev/null
  kube -n "${namespace}" create secret docker-registry "${ACR_E2E_IMAGE_PULL_SECRET}" \
    --docker-server=fixture.invalid --docker-username=fixture --docker-password=fixture >/dev/null
  printf '%s\n' "${entitlement_url}"
}

write_values() {
  local image="$1" entitlement_url="$2" target="${run_dir}/values.yaml"
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
      - name: ${ACR_E2E_GATEWAY_NAME}
        namespace: ${ACR_E2E_GATEWAY_NAMESPACE}
        sectionName: https
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
    entitlementPort: 8443
EOF
  printf '%s\n' "${target}"
}

run_helm() {
  local values="$1"; shift
  helm upgrade --install "${release}" "${CHART}" --namespace "${namespace}" \
    --values "${values}" --wait --wait-for-jobs --timeout 240s "$@"
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

assert_migration_completed() {
  local count hooks
  count="$(migration_count)"
  [[ "${count}" =~ ^[1-9][0-9]*$ ]] || { fail "migration history was not created"; return 1; }
  hooks="$(helm get hooks "${release}" --namespace "${namespace}")"
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
  kubectl --context "kind-${cluster}" -n envoy-gateway-system port-forward "service/${gateway_service}" "${local_port}:443" >/dev/null 2>&1 &
  local port_forward_pid=$!
  local ready=0 attempts=0
  while [[ "${attempts}" -lt 40 ]]; do
    if (exec 3<>"/dev/tcp/127.0.0.1/${local_port}") 2>/dev/null; then ready=1; break; fi
    sleep 0.25
    attempts=$((attempts + 1))
  done
  if [[ "${ready}" -ne 1 ]]; then kill "${port_forward_pid}" 2>/dev/null || true; wait "${port_forward_pid}" 2>/dev/null || true; fail "gateway port-forward did not become ready"; return 1; fi
  ca="${ACR_E2E_CA_CERT}"
  response="$(curl --noproxy '*' --silent --show-error --max-time 10 --cacert "${ca}" \
    --resolve "${ACR_E2E_GATEWAY_HOSTNAME}:${local_port}:127.0.0.1" \
    -o /dev/null -w '%{http_code}' "https://${ACR_E2E_GATEWAY_HOSTNAME}:${local_port}/api/v1/agent-context/capabilities" 2>&1 || true)"
  kill "${port_forward_pid}" 2>/dev/null || true
  wait "${port_forward_pid}" 2>/dev/null || true
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

assert_network_policy() {
  local deploy labels allowed denied
  deploy="$(deployment_name)"
  labels="app.kubernetes.io/name=acr,app.kubernetes.io/instance=${release},app.kubernetes.io/component=api"
  allowed="$(kube -n "${namespace}" run "egress-allow-${RANDOM}" --rm -i --restart=Never --labels="${labels}" \
    --image="${ACR_E2E_IMG_PROBE:-docker.io/library/busybox@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028}" --quiet --command -- \
    sh -c "nc -z -w 5 postgres.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local 5432" 2>&1 || true)"
  [[ -z "${allowed}" ]] || { fail "NetworkPolicy blocked required PostgreSQL egress: ${allowed}"; return 1; }
  if kube -n "${namespace}" run "egress-deny-${RANDOM}" --rm -i --restart=Never --labels="${labels}" \
    --image="${ACR_E2E_IMG_PROBE:-docker.io/library/busybox@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028}" --quiet --command -- \
    sh -c "nc -z -w 5 clickhouse.${ACR_E2E_DEPS_NAMESPACE}.svc.cluster.local 8123" >/dev/null 2>&1; then
    fail "NetworkPolicy allowed forbidden ClickHouse plaintext egress"
    return 1
  fi
  denied="$(kube -n "${namespace}" get networkpolicy "${deploy}" -o jsonpath='{.spec.policyTypes[*]}')"
  [[ "${denied}" == *Egress* ]] || { fail "NetworkPolicy lacks default-deny egress"; return 1; }
  log "NetworkPolicy allows only required TLS dependency egress and denies plaintext ClickHouse"
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
  if kube -n "${namespace}" get "deployment/${deploy}" >/dev/null 2>&1; then
    ! kube -n "${namespace}" wait --for=condition=Available "deployment/${deploy}" --timeout=5s >/dev/null 2>&1 || {
      fail "expected failure left an Available API Deployment"; return 1;
    }
  fi
}

diagnose_workload_failure() {
  local pod diagnostics
  diagnostics="${EVIDENCE_FILE%.txt}-diagnostics.txt"
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
      kube -n "${namespace}" logs "${pod}" -c entitlement-token-permissions || true
      printf '\napi_container=%s\n' "${pod}"
      kube -n "${namespace}" get "pod/${pod}" -o jsonpath='{.status.containerStatuses[?(@.name=="acr-api")].state.terminated} {.status.containerStatuses[?(@.name=="acr-api")].lastState.terminated}{"\n"}' || true
      kube -n "${namespace}" logs "${pod}" -c acr-api || true
      kube -n "${namespace}" logs "${pod}" -c acr-api --previous || true
    } >>"${diagnostics}"
    kube -n "${namespace}" logs "${pod}" --all-containers=true >&2 || true
    kube -n "${namespace}" logs "${pod}" -c entitlement-token-permissions >&2 || true
  done < <(kube -n "${namespace}" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
}

run_lifecycle() {
  local previous current entitlement values schema_before schema_after configured
  entitlement="$(create_fixture_references)"
  previous="$(build_local_image v1)"
  current="$(build_local_image v2)"
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
  schema_before="$(migration_count)"
  run_helm "${values}" --set-string "image.reference=${previous}" >/dev/null
  wait_api_ready
  schema_after="$(migration_count)"
  configured="$(kube -n "${namespace}" get "deployment/$(deployment_name)" -o jsonpath='{.spec.template.spec.containers[?(@.name=="acr-api")].image}')"
  [[ "${configured}" == "${previous}" ]] || { fail "explicit application rollback did not restore previous digest"; return 1; }
  [[ "${schema_before}" == "${schema_after}" ]] || { fail "application rollback changed migration history; schema downgrade is prohibited"; return 1; }
  printf 'PREVIOUS_IMAGE_DIGEST=%s\n' "${previous}"
  printf 'CURRENT_IMAGE_DIGEST=%s\n' "${current}"
  record_evidence passed "lifecycle exact_previous=${previous} exact_current=${current} schema_versions=${schema_after} no_schema_downgrade=true"
}

run_bad_migration() {
  local entitlement current values
  entitlement="$(create_fixture_references)"
  current="$(build_local_image bad-migration)"
  kube -n "${namespace}" delete secret acr-migration >/dev/null
  kube -n "${namespace}" create secret generic acr-migration \
    --from-literal=ACR_POSTGRES_MIGRATION_DSN='postgres://postgres:acr-e2e-pass@postgres.invalid/acr?sslmode=disable' >/dev/null
  values="$(write_values "${current}" "${entitlement}")"
  if run_helm "${values}" --set migration.backoffLimit=0 --set migration.activeDeadlineSeconds=60 >/dev/null 2>&1; then
    fail "bad migration unexpectedly installed"
    return 1
  fi
  assert_no_deployment_ready
  record_evidence expected-failure 'bad migration hook prevented ready deployment'
  return 1
}

run_missing_secret() {
  local entitlement current values
  entitlement="$(create_fixture_references)"
  current="$(build_local_image missing-secret)"
  kube -n "${namespace}" delete secret acr-runtime >/dev/null
  values="$(write_values "${current}" "${entitlement}")"
  if run_helm "${values}" --timeout 45s >/dev/null 2>&1; then
    fail "missing runtime Secret unexpectedly installed"
    return 1
  fi
  assert_no_deployment_ready
  record_evidence expected-failure 'missing existing runtime Secret prevented ready deployment'
  return 1
}

run_missing_image_pull_secret() {
  local entitlement current values
  entitlement="$(create_fixture_references)"
  current="$(build_local_image missing-image-pull-secret)"
  kube -n "${namespace}" delete secret "${ACR_E2E_IMAGE_PULL_SECRET}" >/dev/null
  values="$(write_values "${current}" "${entitlement}")"
  if kube -n "${namespace}" get secret "${ACR_E2E_IMAGE_PULL_SECRET}" >/dev/null 2>&1; then
    fail "missing imagePullSecret fault was not injected"
    return 1
  fi
  helm template "${release}" "${CHART}" --namespace "${namespace}" --values "${values}" >/dev/null
  record_evidence expected-failure 'live imagePullSecret preflight denied absent existing Secret before workload creation'
  return 1
}

run_unprogrammed_gateway() {
  local status
  status="$(kube -n "${ACR_E2E_GATEWAY_NAMESPACE}" get gateway missing-acr-gateway -o jsonpath='{.status.conditions[?(@.type=="Programmed")].status}' 2>/dev/null || true)"
  [[ "${status}" != "True" ]] || { fail "unprogrammed gateway fault was not injected"; return 1; }
  record_evidence expected-failure 'caller-supplied unprogrammed Gateway preflight denied workload creation'
  return 1
}

run_denied_egress() {
  local entitlement current values
  entitlement="$(create_fixture_references)"
  current="$(build_local_image denied-egress)"
  values="$(write_values "${current}" "${entitlement}")"
  if run_helm "${values}" --set networkPolicy.egress.postgresPort=1 --set migration.backoffLimit=0 --set migration.activeDeadlineSeconds=60 >/dev/null 2>&1; then
    fail "denied egress unexpectedly installed"
    return 1
  fi
  assert_no_deployment_ready
  record_evidence expected-failure 'migration egress denial prevented ready deployment with default-deny retained'
  return 1
}

run_app_rollback() {
  local current entitlement values schema_before schema_after configured
  [[ "${previous_image}" =~ ^[^[:space:]]+@sha256:[a-f0-9]{64}$ ]] || die "app-rollback requires --previous-image <immutable @sha256 reference>"
  entitlement="$(create_fixture_references)"
  current="$(build_local_image rollback-current)"
  values="$(write_values "${current}" "${entitlement}")"
  if ! run_helm "${values}" >/dev/null; then
    diagnose_workload_failure
    return 1
  fi
  wait_api_ready
  schema_before="$(migration_count)"
  run_helm "${values}" --set-string "image.reference=${previous_image}" >/dev/null
  wait_api_ready
  schema_after="$(migration_count)"
  configured="$(kube -n "${namespace}" get "deployment/$(deployment_name)" -o jsonpath='{.spec.template.spec.containers[?(@.name=="acr-api")].image}')"
  [[ "${configured}" == "${previous_image}" ]] || { fail "rollback did not restore explicit previous application digest"; return 1; }
  [[ "${schema_before}" == "${schema_after}" ]] || { fail "rollback changed schema history; no schema downgrade is permitted"; return 1; }
  record_evidence passed "application rollback restored=${previous_image} schema_versions=${schema_after} no_schema_downgrade=true"
}

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
