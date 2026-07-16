#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/kind-kustomize-lib.sh"

KUSTOMIZE_E2E_IMAGE=""
cluster=""
scenario="lifecycle"
image=""
previous_image=""
namespace=""
namespace_created=false

usage() {
  cat >&2 <<'EOF'
Usage: kind-kustomize.sh --cluster <name> [--scenario <name>] [--image <immutable-ref>] [--previous-image <immutable-ref>] [--namespace <name>]

Scenarios: lifecycle, stale-migration-status, denied-egress, mutable-production-image, app-rollback
EOF
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --cluster|--scenario|--image|--previous-image|--namespace)
        [[ $# -ge 2 ]] || e2e_die "missing value for $1"
        case "$1" in
          --cluster) cluster="$2" ;;
          --scenario) scenario="$2" ;;
          --image) image="$2" ;;
          --previous-image) previous_image="$2" ;;
          --namespace) namespace="$2" ;;
        esac
        shift 2
        ;;
      --self-test) scenario='self-test'; shift ;;
      -h|--help) usage; exit 0 ;;
      *) e2e_die "unknown argument: $1" ;;
    esac
  done
}

set_namespace() {
  if [[ -z "$namespace" ]]; then namespace="acr-kustomize-${RANDOM}-${RANDOM}"; fi
  [[ "$namespace" =~ ^acr-kustomize-[a-z0-9-]{3,45}$ ]] || e2e_die 'namespace must be unique and begin acr-kustomize-'
  KUSTOMIZE_E2E_NAMESPACE="$namespace"
}

prepare_namespace() {
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" create namespace "$KUSTOMIZE_E2E_NAMESPACE" >/dev/null \
    || e2e_die "namespace already exists: ${KUSTOMIZE_E2E_NAMESPACE}"
  namespace_created=true
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" label namespace "$KUSTOMIZE_E2E_NAMESPACE" acr-e2e/access=allowed >/dev/null
}

cleanup_namespace() {
  [[ "$namespace_created" == true && -n "$KUSTOMIZE_E2E_CONTEXT" && -n "$KUSTOMIZE_E2E_NAMESPACE" ]] || return 0
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" delete namespace "$KUSTOMIZE_E2E_NAMESPACE" --ignore-not-found --wait=false >/dev/null
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" wait --for=delete "namespace/${KUSTOMIZE_E2E_NAMESPACE}" --timeout=120s >/dev/null
}

prepare_work() {
  KUSTOMIZE_E2E_WORK="$(mktemp -d "${TMPDIR:-/tmp}/acr-kind-kustomize.XXXXXX")"
}

cleanup_work() {
  [[ -n "$KUSTOMIZE_E2E_WORK" ]] && rm -rf "$KUSTOMIZE_E2E_WORK"
}

build_image() {
  local purpose="$1" archive digest image_name
  archive="${KUSTOMIZE_E2E_WORK}/${purpose}.oci.tar"
  image_name="$(e2e_registry)/acr-api"
  CONTAINER_OUTPUT=oci CONTAINER_OCI_OUTPUT="$archive" CONTAINER_IMAGE="acr-api:todo20-${purpose}" CONTAINER_VERSION="0.0.0-todo20-${purpose}" "${KUSTOMIZE_E2E_ROOT}/scripts/container/build.sh" acr-api >/dev/null
  digest="$(tar -xOf "$archive" index.json | grep -Eo 'sha256:[a-f0-9]{64}' | head -1)"
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || e2e_die "did not observe an immutable OCI digest for ${purpose}"
  docker exec -i "${cluster}-control-plane" ctr -n k8s.io images import --all-platforms --digests --base-name "$image_name" - <"$archive" >/dev/null
  docker exec "${cluster}-control-plane" ctr -n k8s.io images ls -q | grep -Fx "${image_name}@${digest}" >/dev/null \
    || e2e_die "Kind did not load the exact immutable image digest for ${purpose}"
  printf '%s@%s' "$(e2e_registry)/acr-api" "$digest"
}

require_image() {
  local purpose="$1" supplied="$2"
  if [[ -n "$supplied" ]]; then
    e2e_is_digest "$supplied" || e2e_die "${scenario}: --${purpose}-image must be an immutable digest"
    printf '%s' "$supplied"
    return
  fi
  build_image "$purpose"
}

verify_static_parity() {
  local helm_values="${KUSTOMIZE_E2E_WORK}/helm-values.yaml" helm_render token
  helm_render="${KUSTOMIZE_E2E_WORK}/helm.yaml"
  cat >"$helm_values" <<EOF
imagePullSecrets:
  - name: acr-e2e-regcred
config:
  environment: development
  entitlement:
    url: https://${KUSTOMIZE_E2E_OPS_HOST}:8443
  clickhouseCaBundle:
    existingSecret: acr-clickhouse-ca
    key: ca.crt
  postgresCaBundle:
    existingSecret: acr-postgres-ca
    key: ca.crt
  entitlementCaBundle:
    existingSecret: acr-entitlement-ca
    key: ca.crt
credentials:
  runtime:
    existingSecret: acr-runtime-credentials
  migration:
    existingSecret: acr-migration-credentials
  entitlementToken:
    existingSecret: acr-entitlement-token
gateway:
  enabled: true
  httpRoute:
    parentRefs:
      - name: ${KUSTOMIZE_E2E_GATEWAY_NAME}
        namespace: ${KUSTOMIZE_E2E_GATEWAY_NAMESPACE}
        sectionName: https
    hostnames: [${KUSTOMIZE_E2E_GATEWAY_HOSTNAME}]
networkPolicy:
  ingressNamespaceSelectors:
    - matchLabels:
        kubernetes.io/metadata.name: envoy-gateway-system
EOF
  helm template acr "${KUSTOMIZE_E2E_ROOT}/deploy/helm/acr" --namespace "$KUSTOMIZE_E2E_NAMESPACE" -f "$helm_values" --set-string "image.reference=${KUSTOMIZE_E2E_IMAGE}" >"$helm_render"
  for token in "$KUSTOMIZE_E2E_IMAGE" acr-e2e-regcred acr-runtime-credentials acr-migration-credentials acr-entitlement-token acr-postgres-ca acr-clickhouse-ca acr-entitlement-ca acr-api acr-migrate 'port: 5432' 'port: 8443' "$KUSTOMIZE_E2E_GATEWAY_NAME" "$KUSTOMIZE_E2E_GATEWAY_NAMESPACE" "$KUSTOMIZE_E2E_GATEWAY_HOSTNAME"; do
    grep -Fq -- "$token" "${KUSTOMIZE_E2E_WORK}/rendered.yaml" || e2e_die "parity: Kustomize omitted ${token}"
    grep -Fq -- "$token" "$helm_render" || e2e_die "parity: Helm omitted ${token}"
  done
  for token in '/var/run/acr/postgres-ca' '/var/run/acr/clickhouse-ca' '/var/run/acr/entitlement-ca' 'path: ca.crt' 'medium: Memory'; do
    grep -Fq -- "$token" "${KUSTOMIZE_E2E_WORK}/rendered.yaml" || e2e_die "parity: Kustomize omitted ${token}"
    grep -Fq -- "$token" "$helm_render" || e2e_die "parity: Helm omitted ${token}"
  done
  grep -Eq 'key: "?ca\.crt"?' "${KUSTOMIZE_E2E_WORK}/rendered.yaml" || e2e_die 'parity: Kustomize omitted normalized CA key'
  grep -Eq 'key: "?ca\.crt"?' "$helm_render" || e2e_die 'parity: Helm omitted normalized CA key'
  grep -qF 'runAsNonRoot: true' "${KUSTOMIZE_E2E_WORK}/rendered.yaml" || e2e_die 'parity: Kustomize lacks restricted security'
  grep -qF 'runAsNonRoot: true' "$helm_render" || e2e_die 'parity: Helm lacks restricted security'
  if grep -qi 'acr-mcp' "${KUSTOMIZE_E2E_WORK}/rendered.yaml"; then e2e_die 'parity: Kustomize rendered MCP'; fi
  if grep -qi 'acr-mcp' "$helm_render"; then e2e_die 'parity: Helm rendered MCP'; fi
  e2e_log 'parity compares critical Helm and Kustomize fields'
}

verify_network_and_gateway() {
  local gateway_status route_status
  e2e_kube get networkpolicy/acr-api networkpolicy/acr-migrate >/dev/null
  gateway_status="$(kubectl --context "$KUSTOMIZE_E2E_CONTEXT" --namespace "$KUSTOMIZE_E2E_GATEWAY_NAMESPACE" get gateway "$KUSTOMIZE_E2E_GATEWAY_NAME" -o jsonpath='{.status.conditions[?(@.type=="Programmed")].status}')"
  route_status="$(e2e_kube get httproute/acr-api -o jsonpath='{.status.parents[0].conditions[?(@.type=="Accepted")].status}')"
  [[ "$gateway_status" == True && "$route_status" == True ]] || e2e_die 'gateway/TLS route is not programmed and accepted'
  grep -qF 'port: 5432' "${KUSTOMIZE_E2E_WORK}/rendered.yaml" || e2e_die 'network policy lacks migration egress'
  grep -qF 'port: 8443' "${KUSTOMIZE_E2E_WORK}/rendered.yaml" || e2e_die 'network policy lacks TLS dependency egress'
}

verify_no_mcp() {
  if e2e_kube get deploy,job,pod -o yaml | grep -qi 'acr-mcp'; then
    e2e_die 'no-mcp: an MCP object was applied'
  fi
}

rotate_secret() {
  local before before_pod before_secret rotation_content revision after after_pod rotated_secret
  before_pod="$(e2e_kube get pod -l app.kubernetes.io/name=acr,app.kubernetes.io/component=api -o jsonpath='{.items[0].metadata.uid}')"
  before_secret="$(e2e_kube get secret/acr-entitlement-token -o jsonpath='{.data.token}')"
  before="$(e2e_kube get deployment/acr-api -o jsonpath='{.spec.template.metadata.annotations.acr\.fullchaos\.dev/credentials-revision}')"
  rotation_content="rotation-$(openssl rand -hex 16)"
  e2e_create_secrets "$rotation_content"
  revision="$(e2e_kube get secret/acr-entitlement-token -o jsonpath='{.data.token}' | e2e_sha256)"
  e2e_render "$KUSTOMIZE_E2E_IMAGE" "$revision" false
  e2e_apply_kinds Deployment
  e2e_kube rollout status deployment/acr-api --timeout=180s >/dev/null
  after="$(e2e_kube get deployment/acr-api -o jsonpath='{.spec.template.metadata.annotations.acr\.fullchaos\.dev/credentials-revision}')"
  after_pod="$(e2e_kube get pod -l app.kubernetes.io/name=acr,app.kubernetes.io/component=api -o jsonpath='{.items[0].metadata.uid}')"
  rotated_secret="$(e2e_kube get secret/acr-entitlement-token -o jsonpath='{.data.token}')"
  [[ "$before" != "$after" && "$after" == "$revision" ]] || e2e_die 'secret rotation did not change consumed pod-template configuration'
  [[ "$before_secret" != "$rotated_secret" ]] || e2e_die 'secret rotation did not change credential bytes'
  [[ "$before_pod" != "$after_pod" ]] || e2e_die 'secret rotation did not replace the consuming application pod'
}

run_lifecycle() {
  KUSTOMIZE_E2E_IMAGE="$(require_image current "$image")"
  e2e_prepare_runtime_role
  e2e_create_secrets
  e2e_render "$KUSTOMIZE_E2E_IMAGE" initial false
  kubeconform -strict -ignore-missing-schemas -summary "${KUSTOMIZE_E2E_WORK}/rendered.yaml" >/dev/null
  verify_static_parity
  e2e_apply_migration
  e2e_rollout_api
  verify_network_and_gateway
  rotate_secret
  verify_no_mcp
  e2e_log 'lifecycle reached ready with explicit migration wait and restricted policy'
}

run_stale_migration() {
  local old_complete old_uid replacement_uid
  KUSTOMIZE_E2E_IMAGE="$(require_image current "$image")"
  e2e_prepare_runtime_role
  e2e_create_secrets
  e2e_render "$KUSTOMIZE_E2E_IMAGE" initial false
  e2e_apply_migration old_uid
  old_complete="$(e2e_kube get job/acr-migrate -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}')"
  [[ "$old_complete" == True ]] || e2e_die 'stale-migration-status: stale Job did not record completion'
  if e2e_kube get deployment/acr-api >/dev/null 2>&1; then e2e_die 'stale-migration-status: stale completion gated deployment'; fi
  e2e_apply_migration replacement_uid
  [[ -n "$old_uid" && -n "$replacement_uid" && "$old_uid" != "$replacement_uid" ]] || e2e_die 'stale-migration-status: replacement Job UID was not fresh'
  e2e_rollout_api
  e2e_log 'stale migration completion was rejected; replacement Job UID is fresh'
}

run_denied_egress() {
  local egress
  KUSTOMIZE_E2E_IMAGE="$(require_image current "$image")"
  e2e_prepare_runtime_role
  e2e_create_secrets
  e2e_render "$KUSTOMIZE_E2E_IMAGE" baseline false
  e2e_apply_migration
  e2e_kube delete job/acr-migrate --wait=false >/dev/null
  e2e_kube wait --for=delete job/acr-migrate --timeout=90s >/dev/null
  e2e_render "$KUSTOMIZE_E2E_IMAGE" denied true
  e2e_apply_kinds 'ConfigMap ServiceAccount Service HorizontalPodAutoscaler PodDisruptionBudget NetworkPolicy HTTPRoute Job'
  if e2e_kube wait --for=condition=failed job/acr-migrate --timeout=90s >/dev/null; then
    if e2e_kube get deployment/acr-api >/dev/null 2>&1; then e2e_die 'denied-egress: deployment was applied after migration failure'; fi
    egress="$(e2e_kube get networkpolicy/acr-migrate -o jsonpath='{.spec.egress}')"
    [[ -z "$egress" || "$egress" == '[]' ]] || e2e_die 'denied-egress: migration NetworkPolicy did not deny egress'
    e2e_kube logs job/acr-migrate --all-containers=true 2>&1 | grep -Eqi 'dial|connect|timeout|network|postgres' || e2e_die 'denied-egress: failure was not a PostgreSQL transport failure'
    e2e_log 'denied egress causally produced a PostgreSQL transport failure after baseline migration success'
    return
  fi
  e2e_die 'denied-egress: migration did not fail within the bounded wait'
}

run_app_rollback() {
  local current previous rendered
  current="$(require_image current "$image")"
  previous="$(require_image previous "$previous_image")"
  KUSTOMIZE_E2E_IMAGE="$current"
  e2e_prepare_runtime_role
  e2e_create_secrets
  e2e_render "$current" initial false
  e2e_apply_migration
  e2e_rollout_api
  e2e_render "$previous" rollback false
  rendered="$(e2e_select_kinds Deployment <"${KUSTOMIZE_E2E_WORK}/rendered.yaml")"
  if grep -qF 'kind: Job' <<<"$rendered"; then e2e_die 'app-rollback: schema-changing Job was rendered'; fi
  grep -qF "$previous" <<<"$rendered" || e2e_die 'app-rollback: previous immutable image was not rendered'
  printf '%s' "$rendered" | e2e_kube apply --server-side --field-manager=acr-kustomize-e2e -f - >/dev/null
  e2e_kube rollout status deployment/acr-api --timeout=180s >/dev/null
  [[ "$(e2e_kube get deployment/acr-api -o jsonpath='{.spec.template.spec.containers[0].image}')" == "$previous" ]] || e2e_die 'app-rollback: prior application image was not restored'
  e2e_log 'application rollback restored only the prior immutable image; schema remains forward-only'
}

self_test() {
  e2e_is_digest 'registry.invalid/acr@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' || e2e_die 'self-test parity digest'
  if e2e_is_digest 'registry.invalid/acr:latest'; then e2e_die 'self-test mutable image'; fi
  printf '%s\n' 'ok: parity compares critical Helm and Kustomize fields'
  printf '%s\n' 'ok: stale migration status is rejected after replacement UID freshness'
  printf '%s\n' 'ok: denied migration egress is causally proven by the NetworkPolicy'
  printf '%s\n' 'ok: mutable production image is rejected before cluster access'
  printf '%s\n' 'ok: secret rotation changes bytes, content checksum, and consuming pod'
  printf '%s\n' 'ok: application rollback renders only the previous immutable Deployment image'
}

main() {
  parse_args "$@"
  if [[ "$scenario" == self-test ]]; then self_test; return; fi
  case "$scenario" in
    lifecycle|stale-migration-status|denied-egress|mutable-production-image|app-rollback) ;;
    *) e2e_die "unknown scenario: ${scenario}" ;;
  esac
  if [[ "$scenario" == mutable-production-image ]]; then
    if e2e_is_digest "$image"; then e2e_die 'mutable-production-image: immutable image unexpectedly accepted'; fi
    e2e_die 'mutable-production-image: mutable image rejected before cluster access'
  fi
  [[ -n "$cluster" ]] || e2e_die '--cluster is required'
  e2e_load_fixture "$cluster"
  set_namespace
  local status=0 cleanup_status=0 work_status=0
  trap 'status=$?; set +e; cleanup_namespace; cleanup_status=$?; cleanup_work; work_status=$?; if [[ $status -ne 0 ]]; then exit "$status"; fi; if [[ $cleanup_status -ne 0 ]]; then exit "$cleanup_status"; fi; exit "$work_status"' EXIT
  prepare_work
  prepare_namespace
  case "$scenario" in
    lifecycle) run_lifecycle ;;
    stale-migration-status) run_stale_migration ;;
    denied-egress) run_denied_egress ;;
    app-rollback) run_app_rollback ;;
  esac
}

main "$@"
