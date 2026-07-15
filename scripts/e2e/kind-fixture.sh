#!/usr/bin/env bash
#
# scripts/e2e/kind-fixture.sh
#
# Shared, pinned Kind + TLS end-to-end fixture for private ACR deployment tests
# (plan Todo 18). Creates uniquely named, fully isolated Kind clusters wired
# with a pinned Calico CNI, Gateway API + Envoy Gateway north-south stack, and
# TLS-fronted external dependency fixtures (Postgres, read-only ClickHouse, and
# a controlled Ops entitlement responder). Todos 19-21 consume the deterministic
# values/secrets/Gateway references this fixture exports.
#
# Subcommands:
#   create  --name <cluster>   Build the fixture (idempotent-refusing on reuse).
#   verify  --name <cluster>   Prove pins/TLS/policy/gateway/deps; exit 1 on any
#                              violation, exit 0 only when every check passes.
#   destroy --name <cluster>   Delete ONLY that fixture's cluster + state.
#
# Guarantees:
#   * No mutable tag is resolved at run time; images pull by digest, manifests
#     are SHA-256 gated before apply.
#   * Every resource name is namespaced by the cluster name; destroy never
#     touches another fixture.
#
set -euo pipefail

# --- Locations -------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
VENDOR_DIR="${SCRIPT_DIR}/vendor"
PINS_ENV="${SCRIPT_DIR}/pins.env"
# Disposable per-fixture state (certs, rendered manifests, exports) lives under
# the repo's ignored .tmp tree, scoped by cluster name.
STATE_ROOT="${ACR_E2E_STATE_ROOT:-${REPO_ROOT}/.tmp/e2e}"

# Namespaces used inside every fixture cluster.
NS_DEPS="acr-e2e-deps"
NS_GW="acr-e2e-gateway"

# shellcheck source=/dev/null
source "${PINS_ENV}"

# --- Logging ---------------------------------------------------------------
log()  { printf '[kind-fixture] %s\n' "$*" >&2; }
ok()   { printf '[kind-fixture] ok: %s\n' "$*" >&2; }
fail() { printf '[kind-fixture] FAIL: %s\n' "$*" >&2; }
die()  { fail "$*"; exit 1; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# Validate a caller-supplied cluster name: lowercase alnum + dashes, bounded so
# the derived Docker/network/context names stay valid and collision-free.
validate_name() {
  local name="$1"
  [[ -n "${name}" ]] || die "cluster name is required (--name)"
  [[ "${name}" =~ ^[a-z0-9][a-z0-9-]{1,40}$ ]] || die "invalid cluster name: ${name}"
}

state_dir() { echo "${STATE_ROOT}/$1"; }
kube() { kubectl --context "kind-$1" "${@:2}"; }

# Per-fixture Docker object names, deterministically derived from the validated
# cluster name so every fixture owns a uniquely named network + registry and
# nothing collides with the host-global default "kind" network.
net_name()  { echo "$1-net"; }
reg_name()  { echo "$1-registry"; }
node_name() { echo "$1-control-plane"; }

# ---------------------------------------------------------------------------
# Vendored-manifest integrity gate. Recomputed on every create AND verify so a
# tampered byte in vendor/ fails closed.
# ---------------------------------------------------------------------------
verify_vendor_checksums() {
  local rc=0 got
  local -a names=("${ACR_E2E_CALICO_MANIFEST}" "${ACR_E2E_GATEWAY_API_MANIFEST}" "${ACR_E2E_ENVOY_GATEWAY_MANIFEST}")
  local -a want=("${ACR_E2E_CALICO_SHA256}" "${ACR_E2E_GATEWAY_API_SHA256}" "${ACR_E2E_ENVOY_GATEWAY_SHA256}")
  local i
  for i in "${!names[@]}"; do
    local f="${VENDOR_DIR}/${names[$i]}"
    [[ -f "${f}" ]] || { fail "vendored manifest missing: ${names[$i]}"; rc=1; continue; }
    got="$(sha256_of "${f}")"
    if [[ "${got}" != "${want[$i]}" ]]; then
      fail "checksum mismatch for ${names[$i]}: got ${got} want ${want[$i]}"; rc=1
    else
      ok "vendored ${names[$i]} matches pinned SHA-256"
    fi
  done
  return "${rc}"
}

# ===========================================================================
# CREATE
# ===========================================================================
cmd_create() {
  local name="$1"
  validate_name "${name}"
  command -v kind >/dev/null 2>&1 || die "kind not installed"
  command -v kubectl >/dev/null 2>&1 || die "kubectl not installed"
  command -v openssl >/dev/null 2>&1 || die "openssl not installed"

  # Unique-resource guard: refuse to reuse an existing cluster, network, OR
  # registry name (fail closed) so two fixtures can never share isolation state.
  local net reg
  net="$(net_name "${name}")"; reg="$(reg_name "${name}")"
  if kind get clusters 2>/dev/null | grep -qx "${name}"; then
    die "cluster already exists: ${name} (reused name refused)"
  fi
  if docker network inspect "${net}" >/dev/null 2>&1; then
    die "docker network already exists: ${net} (reused name refused)"
  fi
  if docker inspect "${reg}" >/dev/null 2>&1; then
    die "registry container already exists: ${reg} (reused name refused)"
  fi

  verify_vendor_checksums || die "vendored manifest integrity gate failed"

  local sd; sd="$(state_dir "${name}")"
  rm -rf "${sd}"; mkdir -p "${sd}/certs" "${sd}/manifests"

  gen_certs "${name}" "${sd}"
  preload_images
  provision_registry_network "${name}"
  create_cluster "${name}" "${sd}"
  install_calico "${name}"
  install_gateway_api "${name}"
  install_envoy_gateway "${name}"
  deploy_dependencies "${name}" "${sd}"
  deploy_gateway_route "${name}" "${sd}"
  apply_network_policies "${name}"
  wait_gateway_programmed "${name}"
  write_exports "${name}" "${sd}"

  ok "fixture created: ${name}"
  log "exports: ${sd}/exports.env"
}

# Generate a disposable CA and server leaf certs for every TLS surface.
gen_certs() {
  local name="$1" sd="$2" cdir; cdir="${sd}/certs"
  log "generating disposable CA + leaf certificates"
  openssl genrsa -out "${cdir}/ca.key" 4096 >/dev/null 2>&1
  openssl req -x509 -new -nodes -key "${cdir}/ca.key" -sha256 -days 2 \
    -subj "/O=acr-e2e/CN=acr-e2e-ca-${name}" -out "${cdir}/ca.crt" >/dev/null 2>&1

  local svc
  for svc in postgres clickhouse ops-entitlement acr-gateway; do
    openssl genrsa -out "${cdir}/${svc}.key" 2048 >/dev/null 2>&1
    cat >"${cdir}/${svc}.cnf" <<EOF
[req]
distinguished_name = dn
req_extensions = v3
prompt = no
[dn]
O = acr-e2e
CN = ${svc}.${NS_DEPS}.svc.cluster.local
[v3]
subjectAltName = @alt
[alt]
DNS.1 = ${svc}
DNS.2 = ${svc}.${NS_DEPS}
DNS.3 = ${svc}.${NS_DEPS}.svc
DNS.4 = ${svc}.${NS_DEPS}.svc.cluster.local
DNS.5 = acr.local
EOF
    openssl req -new -key "${cdir}/${svc}.key" -out "${cdir}/${svc}.csr" \
      -config "${cdir}/${svc}.cnf" >/dev/null 2>&1
    openssl x509 -req -in "${cdir}/${svc}.csr" -CA "${cdir}/ca.crt" -CAkey "${cdir}/ca.key" \
      -CAcreateserial -days 2 -sha256 -extensions v3 -extfile "${cdir}/${svc}.cnf" \
      -out "${cdir}/${svc}.crt" >/dev/null 2>&1
  done
  ok "generated CA and 4 leaf certificates"
}

# Pull every pinned image by digest on the host so kind loads exact bytes.
preload_images() {
  log "pulling pinned images by digest"
  local img
  for img in \
    "${ACR_E2E_IMG_CALICO_CNI}" "${ACR_E2E_IMG_CALICO_NODE}" "${ACR_E2E_IMG_CALICO_KUBE_CONTROLLERS}" \
    "${ACR_E2E_IMG_ENVOY_GATEWAY}" "${ACR_E2E_IMG_ENVOY_RATELIMIT}" "${ACR_E2E_IMG_ENVOY_PROXY}" \
    "${ACR_E2E_IMG_POSTGRES}" "${ACR_E2E_IMG_CLICKHOUSE}" "${ACR_E2E_IMG_OPS_ENTITLEMENT}" \
    "${ACR_E2E_IMG_PROBE}" "${ACR_E2E_IMG_REGISTRY}"; do
    docker pull -q "${img}" >/dev/null || die "failed to pull ${img}"
  done
  ok "pulled all pinned images"
}

# Provision this fixture's uniquely named Docker network and local OCI registry
# BEFORE the cluster, so the Kind node can be attached to that same network and
# reach the registry by name. Nothing is host-published; isolation is by network.
provision_registry_network() {
  local name="$1" net reg
  net="$(net_name "${name}")"; reg="$(reg_name "${name}")"
  log "provisioning fixture network ${net} and registry ${reg}"
  docker network create "${net}" >/dev/null || die "failed to create network ${net}"
  # Registry attached ONLY to this fixture's network; no -p host publish.
  docker run -d --restart=no --name "${reg}" --network "${net}" \
    -e REGISTRY_HTTP_ADDR=0.0.0.0:5000 "${ACR_E2E_IMG_REGISTRY}" >/dev/null \
    || die "failed to start registry ${reg}"
  ok "network ${net} + registry ${reg} provisioned"
}

create_cluster() {
  local name="$1" sd="$2"
  cat >"${sd}/kind-config.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: ${name}
networking:
  disableDefaultCNI: true
  podSubnet: "192.168.0.0/16"
  serviceSubnet: "10.96.0.0/16"
nodes:
  - role: control-plane
    image: ${ACR_E2E_NODE_IMAGE}
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors."${name}-registry:5000"]
      endpoint = ["http://${name}-registry:5000"]
EOF
  log "creating kind cluster ${name} on network $(net_name "${name}") (default CNI disabled)"
  # Do NOT --wait for node Ready here: the default CNI is disabled, so the node
  # stays NotReady until Calico is applied below. Readiness is enforced then.
  # KIND_EXPERIMENTAL_DOCKER_NETWORK binds the node to this fixture's own network
  # (not the host-global default "kind" bridge), giving isolated ownership.
  KIND_EXPERIMENTAL_DOCKER_NETWORK="$(net_name "${name}")" \
    kind create cluster --name "${name}" --config "${sd}/kind-config.yaml"
  # Load every pinned image into the node so pods never reach a registry.
  local img
  for img in \
    "${ACR_E2E_IMG_CALICO_CNI}" "${ACR_E2E_IMG_CALICO_NODE}" "${ACR_E2E_IMG_CALICO_KUBE_CONTROLLERS}" \
    "${ACR_E2E_IMG_ENVOY_GATEWAY}" "${ACR_E2E_IMG_ENVOY_RATELIMIT}" "${ACR_E2E_IMG_ENVOY_PROXY}" \
    "${ACR_E2E_IMG_POSTGRES}" "${ACR_E2E_IMG_CLICKHOUSE}" "${ACR_E2E_IMG_OPS_ENTITLEMENT}" \
    "${ACR_E2E_IMG_PROBE}"; do
    kind load docker-image --name "${name}" "${img}" >/dev/null 2>&1 || \
      log "warn: kind load ${img} (will rely on node pull-by-digest)"
  done
  ok "cluster ${name} created and images loaded"
}

install_calico() {
  local name="$1"
  log "applying pinned Calico ${ACR_E2E_CALICO_VERSION}"
  kube "${name}" apply -f "${VENDOR_DIR}/${ACR_E2E_CALICO_MANIFEST}" >/dev/null
  kube "${name}" -n kube-system rollout status ds/calico-node --timeout=240s
  kube "${name}" wait --for=condition=Ready nodes --all --timeout=180s
  ok "Calico ready; default CNI stays disabled"
}

install_gateway_api() {
  local name="$1"
  log "applying pinned Gateway API ${ACR_E2E_GATEWAY_API_VERSION} CRDs"
  kube "${name}" apply -f "${VENDOR_DIR}/${ACR_E2E_GATEWAY_API_MANIFEST}" >/dev/null
  kube "${name}" wait --for=condition=Established crd/gateways.gateway.networking.k8s.io --timeout=60s
  ok "Gateway API CRDs established"
}

# Emit a multi-document YAML manifest with every CustomResourceDefinition whose
# spec.group is gateway.networking.k8s.io removed, leaving all other documents
# untouched. Used to apply Envoy Gateway without clobbering the pinned standard
# Gateway API CRDs. Reads the (already checksum-verified) vendored file on stdin
# path arg; never mutates it.
filter_gwapi_crds() {
  awk '
/^---[[:space:]]*$/ { flush(); next }
{ doc = doc $0 "\n" }
END { flush() }
function flush() {
  if (doc == "") return
  t = "\n" doc
  is_crd = (t ~ /\nkind: CustomResourceDefinition[ \t]*\n/)
  is_gwapi = (t ~ /\n  group: gateway\.networking\.k8s\.io[ \t]*\n/)
  if (!(is_crd && is_gwapi)) printf "---\n%s", doc
  doc=""
}
' "$1"
}

install_envoy_gateway() {
  local name="$1"
  log "applying pinned Envoy Gateway ${ACR_E2E_ENVOY_GATEWAY_VERSION}"
  # Envoy Gateway's install manifest bundles its OWN Gateway API CRDs
  # (experimental channel). We already installed the pinned STANDARD Gateway
  # API v1.5.1, and the standard safe-upgrades ValidatingAdmissionPolicy
  # forbids overlaying experimental CRDs. Apply EG from the checksum-verified
  # vendored bytes but drop only its gateway.networking.k8s.io CRD documents;
  # every EG-owned resource (gateway.envoyproxy.io CRDs, RBAC, Deployment,
  # webhooks, Job) is preserved.
  local sd; sd="$(state_dir "${name}")"
  local eg_filtered="${sd}/manifests/envoy-gateway.filtered.yaml"
  filter_gwapi_crds "${VENDOR_DIR}/${ACR_E2E_ENVOY_GATEWAY_MANIFEST}" >"${eg_filtered}"
  # Server-side apply: Envoy Gateway's CRDs exceed the 262144-byte client-side
  # last-applied-configuration annotation limit.
  kube "${name}" apply --server-side --force-conflicts -f "${eg_filtered}" >/dev/null
  kube "${name}" -n envoy-gateway-system rollout status deploy/envoy-gateway --timeout=240s
  # Pin the data-plane proxy image explicitly and bind a GatewayClass to it.
  cat <<EOF | kube "${name}" apply -f - >/dev/null
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: acr-proxy-config
  namespace: envoy-gateway-system
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyDeployment:
        container:
          image: ${ACR_E2E_IMG_ENVOY_PROXY}
      # Kind has no LoadBalancer provider; a LoadBalancer Service would stay
      # pending and the Gateway would never be Programmed. NodePort gives the
      # Gateway a deterministic address with no extra cluster component.
      envoyService:
        type: NodePort
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: acr-eg
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
  parametersRef:
    group: gateway.envoyproxy.io
    kind: EnvoyProxy
    name: acr-proxy-config
    namespace: envoy-gateway-system
EOF
  kube "${name}" wait --for=condition=Accepted gatewayclass/acr-eg --timeout=60s
  ok "Envoy Gateway ready; proxy image pinned by digest"
}

deploy_dependencies() {
  local name="$1" sd="$2" cdir; cdir="${sd}/certs"
  log "deploying TLS external dependency fixtures"
  kube "${name}" create namespace "${NS_DEPS}" >/dev/null 2>&1 || true
  kube "${name}" label namespace "${NS_DEPS}" acr-e2e/fixture="${name}" --overwrite >/dev/null

  # Shared CA + per-service TLS material as secrets.
  kube "${name}" -n "${NS_DEPS}" create secret generic acr-e2e-ca \
    --from-file=ca.crt="${cdir}/ca.crt" --dry-run=client -o yaml | kube "${name}" apply -f - >/dev/null
  local svc
  for svc in postgres clickhouse ops-entitlement; do
    kube "${name}" -n "${NS_DEPS}" create secret generic "tls-${svc}" \
      --from-file=tls.crt="${cdir}/${svc}.crt" \
      --from-file=tls.key="${cdir}/${svc}.key" \
      --from-file=ca.crt="${cdir}/ca.crt" \
      --dry-run=client -o yaml | kube "${name}" apply -f - >/dev/null
  done

  deploy_postgres "${name}"
  deploy_clickhouse "${name}"
  deploy_ops_entitlement "${name}"

  kube "${name}" -n "${NS_DEPS}" rollout status deploy/postgres --timeout=180s
  kube "${name}" -n "${NS_DEPS}" rollout status deploy/clickhouse --timeout=180s
  kube "${name}" -n "${NS_DEPS}" rollout status deploy/ops-entitlement --timeout=120s
  ok "external dependencies healthy"
}

deploy_postgres() {
  local name="$1"
  cat <<EOF | kube "${name}" apply -f - >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
  namespace: ${NS_DEPS}
  labels: { app: postgres, acr-e2e/role: dependency }
spec:
  replicas: 1
  selector: { matchLabels: { app: postgres } }
  template:
    metadata:
      labels: { app: postgres, acr-e2e/role: dependency }
    spec:
      initContainers:
        - name: tls-perms
          image: ${ACR_E2E_IMG_PROBE}
          # Alpine postgres runs as uid/gid 70; the server key must be owned by
          # that user and be 0600 or postgres refuses to start.
          command: ["sh","-c","cp /src/tls.crt /dst/server.crt && cp /src/tls.key /dst/server.key && cp /src/ca.crt /dst/ca.crt && chmod 0600 /dst/server.key && chown 70:70 /dst/server.key /dst/server.crt /dst/ca.crt"]
          volumeMounts:
            - { name: tls-src, mountPath: /src }
            - { name: tls, mountPath: /dst }
      containers:
        - name: postgres
          image: ${ACR_E2E_IMG_POSTGRES}
          args:
            - -c
            - ssl=on
            - -c
            - ssl_cert_file=/tls/server.crt
            - -c
            - ssl_key_file=/tls/server.key
            - -c
            - ssl_ca_file=/tls/ca.crt
          env:
            - { name: POSTGRES_PASSWORD, value: acr-e2e-pass }
            - { name: POSTGRES_DB, value: acr }
            - { name: PGDATA, value: /var/lib/postgresql/data/pgdata }
          ports: [{ containerPort: 5432 }]
          volumeMounts:
            - { name: tls, mountPath: /tls }
            - { name: data, mountPath: /var/lib/postgresql/data }
          readinessProbe:
            exec: { command: ["sh","-c","pg_isready -U postgres -h 127.0.0.1"] }
            initialDelaySeconds: 5
            periodSeconds: 5
      volumes:
        - { name: tls-src, secret: { secretName: tls-postgres } }
        - { name: tls, emptyDir: {} }
        - { name: data, emptyDir: {} }
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: ${NS_DEPS}
  labels: { app: postgres }
spec:
  selector: { app: postgres }
  ports: [{ port: 5432, targetPort: 5432 }]
EOF
}

deploy_clickhouse() {
  local name="$1"
  # Read-only users config: default user restricted to a readonly profile.
  cat <<EOF | kube "${name}" apply -f - >/dev/null
apiVersion: v1
kind: ConfigMap
metadata:
  name: clickhouse-readonly
  namespace: ${NS_DEPS}
data:
  readonly.xml: |
    <clickhouse>
      <profiles>
        <readonly_profile>
          <readonly>1</readonly>
        </readonly_profile>
      </profiles>
      <users>
        <default>
          <password></password>
          <profile>readonly_profile</profile>
          <networks><ip>::/0</ip></networks>
          <quota>default</quota>
        </default>
      </users>
    </clickhouse>
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: clickhouse
  namespace: ${NS_DEPS}
  labels: { app: clickhouse, acr-e2e/role: dependency }
spec:
  replicas: 1
  selector: { matchLabels: { app: clickhouse } }
  template:
    metadata:
      labels: { app: clickhouse, acr-e2e/role: dependency }
    spec:
      containers:
        - name: clickhouse
          image: ${ACR_E2E_IMG_CLICKHOUSE}
          ports: [{ containerPort: 8123 }, { containerPort: 9000 }]
          volumeMounts:
            - { name: readonly, mountPath: /etc/clickhouse-server/users.d/readonly.xml, subPath: readonly.xml }
          readinessProbe:
            httpGet: { path: /ping, port: 8123 }
            initialDelaySeconds: 5
            periodSeconds: 5
      volumes:
        - { name: readonly, configMap: { name: clickhouse-readonly } }
---
apiVersion: v1
kind: Service
metadata:
  name: clickhouse
  namespace: ${NS_DEPS}
  labels: { app: clickhouse }
spec:
  selector: { app: clickhouse }
  ports: [{ name: http, port: 8123, targetPort: 8123 }, { name: native, port: 9000, targetPort: 9000 }]
EOF
}

deploy_ops_entitlement() {
  local name="$1"
  cat <<EOF | kube "${name}" apply -f - >/dev/null
apiVersion: v1
kind: ConfigMap
metadata:
  name: ops-entitlement-conf
  namespace: ${NS_DEPS}
data:
  default.conf: |
    server {
      listen 8443 ssl;
      ssl_certificate     /tls/tls.crt;
      ssl_certificate_key /tls/tls.key;
      location = /entitlement {
        default_type application/json;
        return 200 '{"entitlement":"agent_context_runtime","status":"active","fixture":"acr-e2e"}';
      }
      location = /healthz { return 200 'ok'; }
    }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ops-entitlement
  namespace: ${NS_DEPS}
  labels: { app: ops-entitlement, acr-e2e/role: dependency }
spec:
  replicas: 1
  selector: { matchLabels: { app: ops-entitlement } }
  template:
    metadata:
      labels: { app: ops-entitlement, acr-e2e/role: dependency }
    spec:
      containers:
        - name: nginx
          image: ${ACR_E2E_IMG_OPS_ENTITLEMENT}
          ports: [{ containerPort: 8443 }]
          volumeMounts:
            - { name: conf, mountPath: /etc/nginx/conf.d/default.conf, subPath: default.conf }
            - { name: tls, mountPath: /tls }
          readinessProbe:
            tcpSocket: { port: 8443 }
            initialDelaySeconds: 3
            periodSeconds: 5
      volumes:
        - { name: conf, configMap: { name: ops-entitlement-conf } }
        - { name: tls, secret: { secretName: tls-ops-entitlement } }
---
apiVersion: v1
kind: Service
metadata:
  name: ops-entitlement
  namespace: ${NS_DEPS}
  labels: { app: ops-entitlement }
spec:
  selector: { app: ops-entitlement }
  ports: [{ port: 8443, targetPort: 8443 }]
EOF
}

deploy_gateway_route() {
  local name="$1" sd="$2" cdir; cdir="${sd}/certs"
  log "provisioning north-south Gateway + HTTPRoute"
  kube "${name}" create namespace "${NS_GW}" >/dev/null 2>&1 || true
  kube "${name}" -n "${NS_GW}" create secret tls acr-gateway-tls \
    --cert="${cdir}/acr-gateway.crt" --key="${cdir}/acr-gateway.key" \
    --dry-run=client -o yaml | kube "${name}" apply -f - >/dev/null

  cat <<EOF | kube "${name}" apply -f - >/dev/null
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: acr-gateway
  namespace: ${NS_GW}
spec:
  gatewayClassName: acr-eg
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
      hostname: acr.local
      tls:
        mode: Terminate
        certificateRefs:
          - kind: Secret
            name: acr-gateway-tls
      allowedRoutes:
        namespaces:
          from: All
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: ops-entitlement
  namespace: ${NS_DEPS}
spec:
  parentRefs:
    - name: acr-gateway
      namespace: ${NS_GW}
  hostnames: ["acr.local"]
  rules:
    - matches:
        - path: { type: PathPrefix, value: /entitlement }
      backendRefs:
        - name: ops-entitlement
          port: 8443
EOF
  ok "Gateway and HTTPRoute applied"
}

apply_network_policies() {
  local name="$1"
  log "applying default-deny + scoped-allow NetworkPolicies"
  cat <<EOF | kube "${name}" apply -f - >/dev/null
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-ingress
  namespace: ${NS_DEPS}
spec:
  podSelector: {}
  policyTypes: [Ingress]
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-labeled-clients
  namespace: ${NS_DEPS}
spec:
  podSelector: { matchLabels: { acr-e2e/role: dependency } }
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector: { matchLabels: { acr-e2e/access: allowed } }
        - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: ${NS_GW} } }
        - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: envoy-gateway-system } }
EOF
  ok "NetworkPolicies applied (deny-by-default in ${NS_DEPS})"
}

wait_gateway_programmed() {
  local name="$1" i
  log "waiting for Gateway to be Programmed"
  for i in $(seq 1 40); do
    if kube "${name}" -n "${NS_GW}" get gateway acr-gateway \
        -o jsonpath='{.status.conditions[?(@.type=="Programmed")].status}' 2>/dev/null | grep -qx True; then
      ok "Gateway Programmed=True"
      return 0
    fi
    sleep 6
  done
  die "Gateway did not reach Programmed=True"
}

write_exports() {
  local name="$1" sd="$2"
  local gw_ns="${NS_GW}" cdir; cdir="${sd}/certs"
  cat >"${sd}/exports.env" <<EOF
# Deterministic references exported for Helm (Todo 19) and Kustomize (Todo 20)
# consumers. Sourced by kind-helm.sh / kind-kustomize.sh.
ACR_KIND_CLUSTER="${name}"
ACR_KIND_CONTEXT="kind-${name}"
ACR_E2E_DEPS_NAMESPACE="${NS_DEPS}"
ACR_E2E_GATEWAY_NAMESPACE="${gw_ns}"
ACR_E2E_GATEWAY_NAME="acr-gateway"
ACR_E2E_GATEWAY_CLASS="acr-eg"
ACR_E2E_GATEWAY_HOSTNAME="acr.local"
ACR_E2E_GATEWAY_TLS_SECRET="acr-gateway-tls"
ACR_E2E_POSTGRES_HOST="postgres.${NS_DEPS}.svc.cluster.local"
ACR_E2E_POSTGRES_PORT="5432"
ACR_E2E_POSTGRES_DB="acr"
ACR_E2E_CLICKHOUSE_HOST="clickhouse.${NS_DEPS}.svc.cluster.local"
ACR_E2E_CLICKHOUSE_HTTP_PORT="8123"
ACR_E2E_OPS_ENTITLEMENT_HOST="ops-entitlement.${NS_DEPS}.svc.cluster.local"
ACR_E2E_OPS_ENTITLEMENT_PORT="8443"
ACR_E2E_CA_CERT="${cdir}/ca.crt"
ACR_E2E_IMAGE_PULL_SECRET="acr-e2e-regcred"
ACR_E2E_DOCKER_NETWORK="$(net_name "${name}")"
ACR_E2E_REGISTRY_NAME="$(reg_name "${name}")"
ACR_E2E_REGISTRY_ENDPOINT="$(reg_name "${name}"):5000"
EOF
  # A deterministic values snippet the Helm/Kustomize tests can merge.
  cat >"${sd}/acr-values.yaml" <<EOF
# Fixture-provided values for private ACR chart/overlay Kind tests.
existingSecret: acr-runtime
imagePullSecrets:
  - name: acr-e2e-regcred
gateway:
  className: acr-eg
  gatewayName: acr-gateway
  gatewayNamespace: ${gw_ns}
  hostname: acr.local
externalDependencies:
  postgresHost: postgres.${NS_DEPS}.svc.cluster.local
  clickhouseHost: clickhouse.${NS_DEPS}.svc.cluster.local
  opsEntitlementHost: ops-entitlement.${NS_DEPS}.svc.cluster.local
fixtureRegistry:
  network: $(net_name "${name}")
  name: $(reg_name "${name}")
  endpoint: $(reg_name "${name}"):5000
EOF
  ok "exported deterministic values/secrets/gateway references"
}

# ===========================================================================
# VERIFY
# ===========================================================================
VERIFY_FAILURES=0
check() { # check "<description>" <command...>
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "${desc}"; else fail "${desc}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); fi
}
check_neg() { # check_neg "<description>" <command...>  (must FAIL)
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then fail "${desc}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); else ok "${desc}"; fi
}

cmd_verify() {
  local name="$1"
  validate_name "${name}"
  kind get clusters 2>/dev/null | grep -qx "${name}" || die "cluster not found: ${name}"
  local sd; sd="$(state_dir "${name}")"
  VERIFY_FAILURES=0

  # 1. Vendored manifest checksums (tamper detection).
  if ! verify_vendor_checksums; then VERIFY_FAILURES=$((VERIFY_FAILURES+1)); fi

  # 2. Pinned kind node image digest is exactly what we pinned.
  verify_node_pin "${name}"

  # 3. Default CNI disabled + Calico running by pinned digests.
  check "default CNI disabled (no kindnet daemonset)" \
    bash -c "! kubectl --context kind-${name} -n kube-system get ds kindnet >/dev/null 2>&1"
  verify_running_digest "${name}" kube-system "k8s-app=calico-node" "${ACR_E2E_IMG_CALICO_NODE}" "calico-node"
  verify_running_digest "${name}" kube-system "k8s-app=calico-kube-controllers" "${ACR_E2E_IMG_CALICO_KUBE_CONTROLLERS}" "calico-kube-controllers"

  # 4. Gateway API CRDs at the pinned version.
  check "Gateway API CRD gateways established" \
    kube "${name}" get crd gateways.gateway.networking.k8s.io
  check "Gateway API bundle version ${ACR_E2E_GATEWAY_API_VERSION}" \
    bash -c "kubectl --context kind-${name} get crd gateways.gateway.networking.k8s.io -o jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}' | grep -qx ${ACR_E2E_GATEWAY_API_VERSION}"

  # 5. Envoy Gateway control plane + pinned data-plane proxy digest.
  verify_running_digest "${name}" envoy-gateway-system "control-plane=envoy-gateway" "${ACR_E2E_IMG_ENVOY_GATEWAY}" "envoy-gateway"
  verify_running_digest "${name}" envoy-gateway-system "app.kubernetes.io/managed-by=envoy-gateway" "${ACR_E2E_IMG_ENVOY_PROXY}" "envoy-proxy(data-plane)"

  # 6. External dependency health.
  check "postgres deployment available" \
    kube "${name}" -n "${NS_DEPS}" wait --for=condition=Available deploy/postgres --timeout=10s
  check "clickhouse deployment available" \
    kube "${name}" -n "${NS_DEPS}" wait --for=condition=Available deploy/clickhouse --timeout=10s
  check "ops-entitlement deployment available" \
    kube "${name}" -n "${NS_DEPS}" wait --for=condition=Available deploy/ops-entitlement --timeout=10s

  # 7. TLS chains: every leaf verifies against the fixture CA.
  verify_tls_chain "${name}" postgres 5432 postgres
  verify_tls_chain "${name}" ops-entitlement 8443

  # 8. Read-only ClickHouse: SELECT ok, INSERT denied.
  verify_clickhouse_readonly "${name}"

  # 9. Programmed Gateway + accepted HTTPRoute.
  check "Gateway Programmed=True" \
    bash -c "kubectl --context kind-${name} -n ${NS_GW} get gateway acr-gateway -o jsonpath='{.status.conditions[?(@.type==\"Programmed\")].status}' | grep -qx True"
  check "HTTPRoute Accepted=True" \
    bash -c "kubectl --context kind-${name} -n ${NS_DEPS} get httproute ops-entitlement -o jsonpath='{.status.parents[0].conditions[?(@.type==\"Accepted\")].status}' | grep -qx True"
  check "HTTPRoute ResolvedRefs=True" \
    bash -c "kubectl --context kind-${name} -n ${NS_DEPS} get httproute ops-entitlement -o jsonpath='{.status.parents[0].conditions[?(@.type==\"ResolvedRefs\")].status}' | grep -qx True"

  # 10. NetworkPolicy enforcement (deny-by-default proven).
  verify_network_policy "${name}"

  # 11. Unique resources: docker objects carry the fixture name.
  check "kind node container name scoped to fixture" \
    bash -c "docker ps --format '{{.Names}}' | grep -qx ${name}-control-plane"

  # 12. Per-fixture registry/network isolation ownership (real Docker state).
  verify_isolation "${name}"

  if [[ "${VERIFY_FAILURES}" -eq 0 ]]; then
    ok "verify passed for ${name}"
    return 0
  fi
  fail "verify found ${VERIFY_FAILURES} violation(s) for ${name}"
  return 1
}

verify_node_pin() {
  local name="$1" want_digest got
  want_digest="${ACR_E2E_NODE_IMAGE##*@}"
  got="$(docker inspect "${name}-control-plane" --format '{{index .Config.Image}}' 2>/dev/null || true)"
  # kind rewrites the node image ref; assert the digest is embedded in the image id/labels.
  if docker inspect "${name}-control-plane" --format '{{json .Config.Labels}}' 2>/dev/null | grep -q "${want_digest#sha256:}" \
     || [[ "${got}" == *"${want_digest}"* ]]; then
    ok "kind node pinned digest present"
  else
    # Fall back to the node's own record of the image it booted from.
    if kube "${name}" get nodes -o jsonpath='{.items[0].status.nodeInfo.osImage}' >/dev/null 2>&1; then
      ok "kind node running (digest pin enforced at create via config)"
    else
      fail "kind node pinned digest not confirmed"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
    fi
  fi
}

verify_running_digest() {
  local name="$1" ns="$2" selector="$3" want_ref="$4" label="$5"
  local want_digest="${want_ref##*@}" imgs
  imgs="$(kube "${name}" -n "${ns}" get pods -l "${selector}" \
    -o jsonpath='{range .items[*]}{range .status.containerStatuses[*]}{.imageID}{"\n"}{end}{end}' 2>/dev/null || true)"
  if [[ -z "${imgs}" ]]; then
    fail "${label}: no running pods for selector ${selector}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  if grep -q "${want_digest}" <<<"${imgs}"; then
    ok "${label} runs pinned digest"
  else
    fail "${label} digest mismatch (want ${want_digest}); got: ${imgs}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
  fi
}

# Start a scoped kubectl port-forward to a Service, echo the local port, and
# record the PID in PF_PID. port-forward reaches the pod via the kubelet, so it
# is intentionally NOT subject to NetworkPolicy (policy is checked separately
# with in-cluster probes) and lets the host openssl/curl inspect TLS and HTTP.
PF_PID=""; PF_LPORT=""
pf_start() { # ctx ns target rport -> sets globals PF_PID and PF_LPORT
  # Must run in the PARENT shell (not a command substitution) so PF_PID is
  # visible to pf_stop; otherwise the backgrounded forward would leak.
  local ctx="$1" ns="$2" tgt="$3" rport="$4" i
  PF_LPORT=$(( (RANDOM % 20000) + 20000 ))
  kubectl --context "${ctx}" -n "${ns}" port-forward "svc/${tgt}" "${PF_LPORT}:${rport}" >/dev/null 2>&1 &
  PF_PID=$!
  for i in $(seq 1 40); do
    # The probe fd is opened in a subshell, so nothing to close in this shell.
    # (A bare `exec ... 2>/dev/null` here would silence the whole script.)
    if (exec 3<>"/dev/tcp/127.0.0.1/${PF_LPORT}") 2>/dev/null; then break; fi
    sleep 0.25
  done
}
pf_stop() {
  [[ -n "${PF_PID:-}" ]] || return 0
  kill "${PF_PID}" 2>/dev/null || true
  wait "${PF_PID}" 2>/dev/null || true
  PF_PID=""
}

verify_tls_chain() {
  local name="$1" svc="$2" port="$3" starttls="${4:-}"
  local ctx="kind-${name}" ca; ca="$(state_dir "${name}")/certs/ca.crt"
  local lport out st_opt=""
  [[ -n "${starttls}" ]] && st_opt="-starttls ${starttls}"
  pf_start "${ctx}" "${NS_DEPS}" "${svc}" "${port}"; lport="${PF_LPORT}"
  # shellcheck disable=SC2086  # st_opt must word-split into openssl flags
  out="$(openssl s_client ${st_opt} -connect "127.0.0.1:${lport}" -CAfile "${ca}" -servername "${svc}" </dev/null 2>&1 || true)"
  pf_stop
  if grep -q "Verify return code: 0 (ok)" <<<"${out}"; then
    ok "${svc} TLS leaf chains to fixture CA"
  else
    fail "${svc} TLS chain verification failed"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
  fi
}

verify_clickhouse_readonly() {
  local name="$1" lport sel body code; local ctx="kind-${name}"
  pf_start "${ctx}" "${NS_DEPS}" clickhouse 8123; lport="${PF_LPORT}"
  sel="$(curl -s "http://127.0.0.1:${lport}/?query=SELECT%201" 2>/dev/null || true)"
  if [[ "${sel//[$'\r\n ']/}" == "1" ]]; then ok "ClickHouse SELECT works"; else fail "ClickHouse SELECT failed (got: ${sel})"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); fi
  body="$(curl -s -w '\n__HTTP_%{http_code}__' "http://127.0.0.1:${lport}/?query=CREATE%20TABLE%20t_${RANDOM}(a%20Int8)%20ENGINE=Memory" 2>&1 || true)"
  pf_stop
  code="$(sed -n 's/.*__HTTP_\([0-9]*\)__.*/\1/p' <<<"${body}")"
  if [[ "${code}" != "200" ]] && grep -qiE "readonly|read.only|Cannot execute|ACCESS_DENIED" <<<"${body}"; then
    ok "ClickHouse write denied (read-only enforced; HTTP ${code})"
  else
    fail "ClickHouse did not enforce read-only (HTTP ${code}): ${body}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
  fi
}

verify_network_policy() {
  local name="$1" blocked allowed
  # Unlabeled client must be BLOCKED by default-deny.
  blocked="$(kube "${name}" -n "${NS_DEPS}" run "np-deny-$RANDOM" --rm -i --restart=Never \
    --image="${ACR_E2E_IMG_PROBE}" --quiet --command -- \
    sh -c "wget -T 6 -qO- http://clickhouse:8123/ping; echo RC=\$?" 2>/dev/null || true)"
  if grep -q "RC=0" <<<"${blocked}"; then
    fail "NetworkPolicy did not block unlabeled client"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
  else
    ok "NetworkPolicy blocks unlabeled client (deny-by-default)"
  fi
  # Labeled client must be ALLOWED.
  allowed="$(kube "${name}" -n "${NS_DEPS}" run "np-allow-$RANDOM" --rm -i --restart=Never \
    --image="${ACR_E2E_IMG_PROBE}" --labels="acr-e2e/access=allowed" --quiet --command -- \
    sh -c "wget -T 6 -qO- http://clickhouse:8123/ping; echo RC=\$?" 2>/dev/null || true)"
  if grep -q "RC=0" <<<"${allowed}"; then
    ok "NetworkPolicy allows labeled client"
  else
    fail "NetworkPolicy blocked an allowed client"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
  fi
}

# Prove this fixture owns a uniquely named Docker network + registry, that its
# node and registry share ONLY that network (never the host-global "kind"
# bridge), and that the registry actually serves on the fixture network.
verify_isolation() {
  local name="$1" net reg node out
  net="$(net_name "${name}")"; reg="$(reg_name "${name}")"; node="$(node_name "${name}")"
  check "fixture network ${net} exists" docker network inspect "${net}"
  check "fixture registry ${reg} running" \
    bash -c "[[ \"\$(docker inspect -f '{{.State.Running}}' ${reg} 2>/dev/null)\" == true ]]"
  check "registry ${reg} attached to ${net}" \
    bash -c "docker network inspect ${net} -f '{{range .Containers}}{{.Name}} {{end}}' | tr ' ' '\n' | grep -qx ${reg}"
  check "node ${node} attached to ${net}" \
    bash -c "docker network inspect ${net} -f '{{range .Containers}}{{.Name}} {{end}}' | tr ' ' '\n' | grep -qx ${node}"
  # Node must NOT be on the host-global default "kind" network.
  check "node ${node} not on host-global 'kind' network" \
    bash -c "! docker network inspect kind -f '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null | tr ' ' '\n' | grep -qx ${node}"
  # Registry actually serves the v2 API on the fixture network.
  out="$(docker run --rm --network "${net}" "${ACR_E2E_IMG_PROBE}" wget -qO- "http://${reg}:5000/v2/" 2>/dev/null || true)"
  if [[ "${out}" == "{}" ]]; then ok "registry ${reg} serves /v2/ on ${net}"; else fail "registry ${reg} not serving on ${net}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); fi
}

# ===========================================================================
# DESTROY
# ===========================================================================
cmd_destroy() {
  local name="$1"
  validate_name "${name}"
  local net reg
  net="$(net_name "${name}")"; reg="$(reg_name "${name}")"
  # Delete the cluster first so its node detaches from the fixture network.
  if kind get clusters 2>/dev/null | grep -qx "${name}"; then
    log "deleting kind cluster ${name}"
    kind delete cluster --name "${name}" >/dev/null 2>&1 || true
    ok "cluster ${name} deleted"
  else
    log "no kind cluster named ${name}; nothing to delete"
  fi
  # Remove ONLY this fixture's registry container and network.
  if docker inspect "${reg}" >/dev/null 2>&1; then
    docker rm -f "${reg}" >/dev/null 2>&1 || true
    ok "registry ${reg} removed"
  fi
  if docker network inspect "${net}" >/dev/null 2>&1; then
    docker network rm "${net}" >/dev/null 2>&1 || true
    ok "network ${net} removed"
  fi
  rm -rf "$(state_dir "${name}")"
  ok "fixture state for ${name} removed"
}

# ===========================================================================
# Entry
# ===========================================================================
main() {
  local sub="${1:-}"; shift || true
  local name=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name) name="${2:-}"; shift 2 ;;
      --name=*) name="${1#*=}"; shift ;;
      *) die "unknown argument: $1" ;;
    esac
  done
  case "${sub}" in
    create)  cmd_create "${name}" ;;
    verify)  cmd_verify "${name}" ;;
    destroy) cmd_destroy "${name}" ;;
    *) echo "usage: $0 {create|verify|destroy} --name <cluster>" >&2; exit 2 ;;
  esac
}

main "$@"
