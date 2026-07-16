#!/usr/bin/env bash

set -euo pipefail

KUSTOMIZE_E2E_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KUSTOMIZE_SOURCE_ROOT="${KUSTOMIZE_E2E_ROOT}/deploy/kubernetes/acr"
KUSTOMIZE_FIXTURE_ROOT="${ACR_E2E_STATE_ROOT:-${KUSTOMIZE_E2E_ROOT}/.tmp/e2e}"
KUSTOMIZE_E2E_WORK=""
KUSTOMIZE_E2E_CONTEXT=""
KUSTOMIZE_E2E_NAMESPACE=""
KUSTOMIZE_E2E_CA=""
KUSTOMIZE_E2E_DEPS_NAMESPACE=""
KUSTOMIZE_E2E_GATEWAY_NAMESPACE=""
KUSTOMIZE_E2E_GATEWAY_NAME=""
KUSTOMIZE_E2E_GATEWAY_HOSTNAME=""
KUSTOMIZE_E2E_POSTGRES_HOST=""
KUSTOMIZE_E2E_CLICKHOUSE_HOST=""
KUSTOMIZE_E2E_OPS_HOST=""
KUSTOMIZE_E2E_REGISTRY=""

e2e_die() {
  printf 'kind-kustomize: %s\n' "$*" >&2
  exit 1
}

e2e_log() {
  printf 'kind-kustomize: %s\n' "$*" >&2
}

e2e_is_digest() {
  [[ "$1" =~ ^[a-zA-Z0-9._/-]+@sha256:[a-f0-9]{64}$ ]]
}

e2e_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum | awk '{print $1}';
  else shasum -a 256 | awk '{print $1}'; fi
}

e2e_fixture_value() {
  local file="$1" key="$2" line value matches
  matches="$(grep -cE "^${key}=\"[^\"]*\"$" "$file" || true)"
  [[ "$matches" == "1" ]] || e2e_die "invalid fixture export: ${key}"
  line="$(grep -E "^${key}=\"[^\"]*\"$" "$file")"
  value="${line#*=\"}"
  value="${value%\"}"
  ! printf '%s' "$value" | LC_ALL=C grep -q '[\\$`]' || e2e_die "unsafe fixture export: ${key}"
  printf '%s' "$value"
}

e2e_load_fixture() {
  local cluster="$1" file
  [[ "$cluster" =~ ^[a-z0-9][a-z0-9-]{1,40}$ ]] || e2e_die "invalid cluster name"
  file="${KUSTOMIZE_FIXTURE_ROOT}/${cluster}/exports.env"
  [[ -f "$file" ]] || e2e_die "fixture exports not found for cluster ${cluster}"
  KUSTOMIZE_E2E_CONTEXT="$(e2e_fixture_value "$file" ACR_KIND_CONTEXT)"
  KUSTOMIZE_E2E_DEPS_NAMESPACE="$(e2e_fixture_value "$file" ACR_E2E_DEPS_NAMESPACE)"
  KUSTOMIZE_E2E_GATEWAY_NAMESPACE="$(e2e_fixture_value "$file" ACR_E2E_GATEWAY_NAMESPACE)"
  KUSTOMIZE_E2E_GATEWAY_NAME="$(e2e_fixture_value "$file" ACR_E2E_GATEWAY_NAME)"
  KUSTOMIZE_E2E_GATEWAY_HOSTNAME="$(e2e_fixture_value "$file" ACR_E2E_GATEWAY_HOSTNAME)"
  KUSTOMIZE_E2E_POSTGRES_HOST="$(e2e_fixture_value "$file" ACR_E2E_POSTGRES_HOST)"
  KUSTOMIZE_E2E_CLICKHOUSE_HOST="$(e2e_fixture_value "$file" ACR_E2E_CLICKHOUSE_HOST)"
  KUSTOMIZE_E2E_OPS_HOST="$(e2e_fixture_value "$file" ACR_E2E_OPS_ENTITLEMENT_HOST)"
  KUSTOMIZE_E2E_CA="$(e2e_fixture_value "$file" ACR_E2E_CA_CERT)"
  KUSTOMIZE_E2E_REGISTRY="$(e2e_fixture_value "$file" ACR_E2E_REGISTRY_ENDPOINT)"
  [[ -f "$KUSTOMIZE_E2E_CA" ]] || e2e_die "fixture CA is unavailable"
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" get namespace "$KUSTOMIZE_E2E_DEPS_NAMESPACE" >/dev/null \
    || e2e_die "fixture dependency namespace is unavailable"
}

e2e_kube() {
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" --namespace "$KUSTOMIZE_E2E_NAMESPACE" "$@"
}

e2e_registry() {
  printf '%s' "$KUSTOMIZE_E2E_REGISTRY"
}

e2e_select_kinds() {
  local wanted="$1"
  awk -v wanted=" $wanted " '
    function emit(  count,lines,line_index,kind) {
      count=split(document,lines,"\n")
      for (line_index=1;line_index<=count;line_index++) {
        if (lines[line_index] ~ /^kind: /) {
          kind=lines[line_index]
          sub(/^kind: /,"",kind)
          if (index(wanted," " kind " ")) printf "%s---\n",document
          return
        }
      }
    }
    /^---[[:space:]]*$/ { emit(); document=""; next }
    { document=document $0 "\n" }
    END { emit() }
  '
}

e2e_render() {
  local image="$1" revision="$2" deny_egress="$3" directory
  directory="${KUSTOMIZE_E2E_WORK}/acr/overlays/development"
  cp -R "$KUSTOMIZE_SOURCE_ROOT" "${KUSTOMIZE_E2E_WORK}/acr"
  cat >"${directory}/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: ${KUSTOMIZE_E2E_NAMESPACE}
labels:
  - pairs:
      acr-e2e/access: allowed
    includeSelectors: false
    includeTemplates: true
resources:
  - ../../base
  - httproute.yaml
patches:
  - target:
      group: apps
      version: v1
      kind: Deployment
      name: acr-api
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/image
        value: ${image}
      - op: replace
        path: /spec/template/metadata/annotations/acr.fullchaos.dev~1credentials-revision
        value: ${revision}
      - op: replace
        path: /spec/template/spec/imagePullSecrets/0/name
        value: acr-e2e-regcred
  - target:
      group: batch
      version: v1
      kind: Job
      name: acr-migrate
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/image
        value: ${image}
      - op: replace
        path: /spec/template/metadata/annotations/acr.fullchaos.dev~1credentials-revision
        value: ${revision}
      - op: replace
        path: /spec/template/spec/imagePullSecrets/0/name
        value: acr-e2e-regcred
  - target:
      version: v1
      kind: ConfigMap
      name: acr-config
    patch: |-
      - op: replace
        path: /data/ACR_DEV_HEALTH_ENTITLEMENT_URL
        value: https://${KUSTOMIZE_E2E_OPS_HOST}:8443
  - target:
      group: networking.k8s.io
      version: v1
      kind: NetworkPolicy
      name: acr-api
    patch: |-
      - op: replace
        path: /spec/ingress/0/from
        value:
          - namespaceSelector:
              matchLabels:
                kubernetes.io/metadata.name: envoy-gateway-system
EOF
  if [[ "$deny_egress" == true ]]; then
    cat >>"${directory}/kustomization.yaml" <<'EOF'
  - target:
      group: networking.k8s.io
      version: v1
      kind: NetworkPolicy
      name: acr-migrate
    patch: |-
      - op: replace
        path: /spec/egress
        value: []
  - target:
      group: batch
      version: v1
      kind: Job
      name: acr-migrate
    patch: |-
      - op: replace
        path: /spec/backoffLimit
        value: 0
      - op: replace
        path: /spec/activeDeadlineSeconds
        value: 45
EOF
  fi
  cat >"${directory}/httproute.yaml" <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: acr-api
spec:
  parentRefs:
    - name: ${KUSTOMIZE_E2E_GATEWAY_NAME}
      namespace: ${KUSTOMIZE_E2E_GATEWAY_NAMESPACE}
      sectionName: https
  hostnames:
    - ${KUSTOMIZE_E2E_GATEWAY_HOSTNAME}
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: acr-api
          port: 8080
EOF
  if command -v kustomize >/dev/null 2>&1; then
    kustomize build "$directory" >"${KUSTOMIZE_E2E_WORK}/rendered.yaml"
  else
    kubectl kustomize "$directory" >"${KUSTOMIZE_E2E_WORK}/rendered.yaml"
  fi
}

e2e_apply_kinds() {
  local kinds="$1"
  e2e_select_kinds "$kinds" <"${KUSTOMIZE_E2E_WORK}/rendered.yaml" \
    | e2e_kube apply --server-side --field-manager=acr-kustomize-e2e -f - >/dev/null
}

e2e_create_secrets() {
	local rotation_content="${1:-initial}" migration_dsn runtime_dsn clickhouse_dsn keyring
  migration_dsn="postgres://postgres:acr-e2e-pass@${KUSTOMIZE_E2E_POSTGRES_HOST}:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
  runtime_dsn="postgres://acr_runtime:acr-e2e-runtime-pass@${KUSTOMIZE_E2E_POSTGRES_HOST}:5432/acr?sslmode=verify-full&sslrootcert=/var/run/acr/postgres-ca/ca.crt"
  clickhouse_dsn="clickhouse://default:@${KUSTOMIZE_E2E_CLICKHOUSE_HOST}:8443/default?secure=true&skip_verify=false&tls_server_name=${KUSTOMIZE_E2E_CLICKHOUSE_HOST}"
  keyring='current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE='
  for name in acr-postgres-ca acr-clickhouse-ca acr-entitlement-ca; do
    e2e_kube create secret generic "$name" --from-file=ca.crt="$KUSTOMIZE_E2E_CA" --dry-run=client -o yaml | e2e_kube apply -f - >/dev/null
  done
  e2e_kube create secret generic acr-runtime-credentials \
    --from-literal=ACR_POSTGRES_DSN="$runtime_dsn" --from-literal=ACR_CLICKHOUSE_DSN="$clickhouse_dsn" \
    --from-literal=ACR_EVIDENCE_ID_ACTIVE_KID=current --from-literal=ACR_EVIDENCE_ID_KEYS="$keyring" \
    --dry-run=client -o yaml | e2e_kube apply -f - >/dev/null
  e2e_kube create secret generic acr-migration-credentials --from-literal=ACR_POSTGRES_MIGRATION_DSN="$migration_dsn" --dry-run=client -o yaml | e2e_kube apply -f - >/dev/null
	e2e_kube create secret generic acr-entitlement-token --from-literal="token=acr-e2e-ops-token-${rotation_content}" --dry-run=client -o yaml | e2e_kube apply -f - >/dev/null
  e2e_kube create secret generic acr-e2e-regcred --type=kubernetes.io/dockerconfigjson --from-literal=.dockerconfigjson='{"auths":{}}' --dry-run=client -o yaml | e2e_kube apply -f - >/dev/null
}

e2e_set_ops_entitlement_token() {
  local token="$1" config
  config="$(cat <<EOF
server {
  listen 8443 ssl;
  ssl_certificate /tls/tls.crt;
  ssl_certificate_key /tls/tls.key;
  location = /entitlement {
    if (\$http_authorization != "Bearer ${token}") { return 401; }
    default_type application/json;
    return 200 '{"entitlement":"agent_context_runtime","status":"active","fixture":"acr-e2e"}';
  }
  location = /healthz { return 200 'ok'; }
  location = /api/v1/internal/acr/health {
    if (\$http_authorization != "Bearer ${token}") { return 401; }
    default_type application/json;
    return 200 '{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok"}';
  }
}
EOF
)"
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" --namespace "$KUSTOMIZE_E2E_DEPS_NAMESPACE" create configmap ops-entitlement-conf --from-literal=default.conf="$config" --dry-run=client -o yaml | kubectl --context "$KUSTOMIZE_E2E_CONTEXT" apply -f - >/dev/null
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" --namespace "$KUSTOMIZE_E2E_DEPS_NAMESPACE" rollout restart deployment/ops-entitlement >/dev/null
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" --namespace "$KUSTOMIZE_E2E_DEPS_NAMESPACE" rollout status deployment/ops-entitlement --timeout=120s >/dev/null
}

e2e_application_readiness() {
  local pod port pid body ready=0 attempt
  pod="$(e2e_kube get pod -l app.kubernetes.io/name=acr,app.kubernetes.io/component=api -o jsonpath='{.items[0].metadata.name}')"
  [[ -n "$pod" ]] || e2e_die 'application readiness probe could not select an API pod'
  port=$(( (RANDOM % 20000) + 20000 ))
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" --namespace "$KUSTOMIZE_E2E_NAMESPACE" port-forward "pod/${pod}" "${port}:8080" >/dev/null 2>&1 &
  pid=$!
  for attempt in $(seq 1 40); do
    if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then ready=1; break; fi
    sleep 0.25
  done
  if [[ "$ready" != 1 ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; e2e_die 'application readiness port-forward did not become ready'; fi
  body="$(curl --max-time 10 --silent --show-error "http://127.0.0.1:${port}/readyz" 2>&1 || true)"
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  printf '%s' "$body"
}

e2e_prepare_runtime_role() {
  kubectl --context "$KUSTOMIZE_E2E_CONTEXT" --namespace "$KUSTOMIZE_E2E_DEPS_NAMESPACE" exec deploy/postgres -- \
    psql -v ON_ERROR_STOP=1 -U postgres -d acr -c "DO \$\$ BEGIN CREATE ROLE acr_runtime LOGIN PASSWORD 'acr-e2e-runtime-pass'; EXCEPTION WHEN duplicate_object THEN END \$\$; CREATE SCHEMA IF NOT EXISTS acr; GRANT USAGE ON SCHEMA acr TO acr_runtime; ALTER DEFAULT PRIVILEGES IN SCHEMA acr GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO acr_runtime; ALTER DEFAULT PRIVILEGES IN SCHEMA acr GRANT USAGE, SELECT ON SEQUENCES TO acr_runtime;" >/dev/null
}

e2e_apply_migration() {
	local result_name="${1:-}" uid observed
	if e2e_kube get job/acr-migrate >/dev/null 2>&1; then
		e2e_kube delete job/acr-migrate --wait=false >/dev/null
		e2e_kube wait --for=delete job/acr-migrate --timeout=90s >/dev/null
  fi
  e2e_apply_kinds 'ConfigMap ServiceAccount Service HorizontalPodAutoscaler PodDisruptionBudget NetworkPolicy HTTPRoute'
  e2e_apply_kinds Job
  uid="$(e2e_kube get job/acr-migrate -o jsonpath='{.metadata.uid}')"
  [[ -n "$uid" ]] || e2e_die 'migration did not expose a Job UID'
  if ! e2e_kube wait --for=condition=complete job/acr-migrate --timeout=180s >/dev/null; then
    e2e_kube describe job/acr-migrate >&2 || true
    e2e_kube logs job/acr-migrate --all-containers=true >&2 || true
    e2e_die 'migration failed; deployment was not applied'
  fi
	observed="$(e2e_kube get job/acr-migrate -o jsonpath='{.metadata.uid}')"
	[[ "$observed" == "$uid" ]] || e2e_die 'stale-migration-status: completed Job UID changed'
	if [[ -n "$result_name" ]]; then printf -v "$result_name" '%s' "$uid"; fi
}


e2e_rollout_api() {
  e2e_apply_kinds Deployment
  if ! e2e_kube rollout status deployment/acr-api --timeout=180s >/dev/null; then
    e2e_kube describe deployment/acr-api >&2 || true
    e2e_kube logs deployment/acr-api --all-containers=true >&2 || true
    e2e_die 'application rollout failed after migration'
  fi
  e2e_kube wait --for=condition=available deployment/acr-api --timeout=60s >/dev/null || e2e_die 'application did not become available'
}
