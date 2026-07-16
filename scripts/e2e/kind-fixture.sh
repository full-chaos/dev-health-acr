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
# Guarantees verified by `verify`:
#   * Vendored manifests are checksum-gated, rendered into per-fixture copies
#     with every runtime image rewritten to a pins.env digest, and running
#     image IDs are recorded only after they are observed.
#   * Destroy requires a recorded, exact fixture identity for every Docker and
#     Kind resource it might remove. Derived names alone are never ownership.
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
FIXTURE_LABEL_KEY="acr-e2e.fullchaos.dev/fixture-id"
FIXTURE_MARKER="acr-e2e-fixture-ownership"
IDENTITY_FILE="fixture-identity.env"

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

# Identity is intentionally a random, opaque token rather than a value derived
# from a name. A stale state directory cannot authorize deletion of a later
# resource that happens to reuse the same derived Docker names.
FIXTURE_NAME=""
FIXTURE_ID=""
FIXTURE_NETWORK_ID=""
FIXTURE_REGISTRY_ID=""
FIXTURE_NODE_ID=""
FIXTURE_PHASE=""
FIXTURE_LOCK_DIR=""
CREATE_IN_MEMORY_OWNERSHIP=0
VERIFY_BACKEND_TLS_RESTORE_NAME=""
VERIFY_BACKEND_TLS_RESTORE_HOST=""

identity_path() { echo "$(state_dir "$1")/${IDENTITY_FILE}"; }
lock_path() { echo "${STATE_ROOT}/.$1.fixture-lock"; }

acquire_fixture_lock() {
  local name="$1" lock
  lock="$(lock_path "${name}")"
  mkdir -p "${STATE_ROOT}"
  mkdir "${lock}" 2>/dev/null || die "fixture operation already holds lock: ${name}"
  FIXTURE_LOCK_DIR="${lock}"
}

release_fixture_lock() {
  [[ -n "${FIXTURE_LOCK_DIR}" ]] || return 0
  if ! rmdir "${FIXTURE_LOCK_DIR}"; then
    fail "failed to release fixture operation lock: ${FIXTURE_LOCK_DIR}"
    return 1
  fi
  FIXTURE_LOCK_DIR=""
}

write_fixture_identity() {
  local name="$1" file tmp
  file="$(identity_path "${name}")"; tmp="${file}.tmp"
  (
    umask 077
    cat >"${tmp}" <<EOF
fixture_name=${FIXTURE_NAME}
fixture_id=${FIXTURE_ID}
network_id=${FIXTURE_NETWORK_ID}
registry_id=${FIXTURE_REGISTRY_ID}
node_id=${FIXTURE_NODE_ID}
phase=${FIXTURE_PHASE}
EOF
  )
  mv "${tmp}" "${file}"
}

# Do not source lifecycle state: even a local test fixture must not execute a
# tampered state file before deciding whether it owns a Docker object.
load_fixture_identity() {
  local name="$1" file line key value count=0
  file="$(identity_path "${name}")"
  [[ -f "${file}" ]] || return 1
  FIXTURE_NAME=""; FIXTURE_ID=""; FIXTURE_NETWORK_ID=""; FIXTURE_REGISTRY_ID=""; FIXTURE_NODE_ID=""; FIXTURE_PHASE=""
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ "${line}" == *=* ]] || return 1
    key="${line%%=*}"; value="${line#*=}"
    case "${key}" in
      fixture_name) [[ -z "${FIXTURE_NAME}" ]] || return 1; FIXTURE_NAME="${value}" ;;
      fixture_id) [[ -z "${FIXTURE_ID}" ]] || return 1; FIXTURE_ID="${value}" ;;
      network_id) [[ -z "${FIXTURE_NETWORK_ID}" ]] || return 1; FIXTURE_NETWORK_ID="${value}" ;;
      registry_id) [[ -z "${FIXTURE_REGISTRY_ID}" ]] || return 1; FIXTURE_REGISTRY_ID="${value}" ;;
      node_id) [[ -z "${FIXTURE_NODE_ID}" ]] || return 1; FIXTURE_NODE_ID="${value}" ;;
      phase) [[ -z "${FIXTURE_PHASE}" ]] || return 1; FIXTURE_PHASE="${value}" ;;
      *) return 1 ;;
    esac
    count=$((count + 1))
  done <"${file}"
  [[ "${count}" -eq 6 && "${FIXTURE_NAME}" == "${name}" ]] || return 1
  [[ "${FIXTURE_ID}" =~ ^[a-f0-9]{32}$ ]] || return 1
  [[ -z "${FIXTURE_NETWORK_ID}" || "${FIXTURE_NETWORK_ID}" =~ ^[a-f0-9]{64}$ ]] || return 1
  [[ -z "${FIXTURE_REGISTRY_ID}" || "${FIXTURE_REGISTRY_ID}" =~ ^[a-f0-9]{64}$ ]] || return 1
  [[ -z "${FIXTURE_NODE_ID}" || "${FIXTURE_NODE_ID}" =~ ^[a-f0-9]{64}(,[a-f0-9]{64})*$ ]] || return 1
  [[ "${FIXTURE_PHASE}" == "creating" || "${FIXTURE_PHASE}" == "ready" ]] || return 1
}

new_fixture_identity() {
  local name="$1"
  FIXTURE_NAME="${name}"
  FIXTURE_ID="$(openssl rand -hex 16)"
  [[ "${FIXTURE_ID}" =~ ^[a-f0-9]{32}$ ]] || die "failed to create fixture identity"
  FIXTURE_NETWORK_ID=""; FIXTURE_REGISTRY_ID=""; FIXTURE_NODE_ID=""; FIXTURE_PHASE="creating"
  CREATE_IN_MEMORY_OWNERSHIP=1
  write_fixture_identity "${name}"
}

docker_network_label() {
  docker network inspect "$1" --format "{{ index .Labels \"${FIXTURE_LABEL_KEY}\" }}" 2>/dev/null
}

docker_container_label() {
  docker inspect "$1" --format "{{ index .Config.Labels \"${FIXTURE_LABEL_KEY}\" }}" 2>/dev/null
}

network_has_container() {
  docker network inspect "$1" --format '{{range .Containers}}{{.Name}}{{"\n"}}{{end}}' 2>/dev/null | grep -qx "$2"
}

PROBE_STATE=""
PROBE_ID=""
PROBE_ERROR=""

probe_docker_network() {
  local output
  if output="$(docker network inspect "$1" --format '{{.Id}}' 2>&1)"; then
    PROBE_STATE="present"; PROBE_ID="${output}"; PROBE_ERROR=""; return 0
  fi
  if [[ "${output}" == *"No such network"* || "${output}" == *"no such network"* || "${output}" == *"network $1 not found"* ]]; then
    PROBE_STATE="absent"; PROBE_ID=""; PROBE_ERROR=""; return 0
  fi
  PROBE_STATE="unknown"; PROBE_ID=""; PROBE_ERROR="${output}"; return 1
}

probe_docker_container() {
  local output
  if output="$(docker inspect "$1" --format '{{.Id}}' 2>&1)"; then
    PROBE_STATE="present"; PROBE_ID="${output}"; PROBE_ERROR=""; return 0
  fi
  if [[ "${output}" == *"No such object"* || "${output}" == *"no such object"* || "${output}" == *"No such container"* || "${output}" == *"no such container"* ]]; then
    PROBE_STATE="absent"; PROBE_ID=""; PROBE_ERROR=""; return 0
  fi
  PROBE_STATE="unknown"; PROBE_ID=""; PROBE_ERROR="${output}"; return 1
}

probe_kind_cluster() {
  local output line
  if ! output="$(kind get clusters 2>&1)"; then
    PROBE_STATE="unknown"; PROBE_ID=""; PROBE_ERROR="${output}"; return 1
  fi
  while IFS= read -r line || [[ -n "${line}" ]]; do
    if [[ "${line}" == "$1" ]]; then
      PROBE_STATE="present"; PROBE_ID=""; PROBE_ERROR=""; return 0
    fi
  done <<<"${output}"
  PROBE_STATE="absent"; PROBE_ID=""; PROBE_ERROR=""; return 0
}

probe_kind_nodes() {
  local output node_id node_ids=""
  if ! output="$(docker ps -a --no-trunc --filter "label=io.x-k8s.kind.cluster=$1" --format '{{.ID}}' 2>&1)"; then
    PROBE_STATE="unknown"; PROBE_ID=""; PROBE_ERROR="${output}"; return 1
  fi
  while IFS= read -r node_id || [[ -n "${node_id}" ]]; do
    [[ -n "${node_id}" ]] || continue
    node_ids+="${node_ids:+,}${node_id}"
  done < <(printf '%s\n' "${output}" | LC_ALL=C sort)
  if [[ -n "${node_ids}" ]]; then
    PROBE_STATE="present"; PROBE_ID="${node_ids}"; PROBE_ERROR=""; return 0
  fi
  PROBE_STATE="absent"; PROBE_ID=""; PROBE_ERROR=""; return 0
}

kind_version_matches() {
  local got
  got="$(kind version -q 2>/dev/null || true)"
  [[ "${got}" == v* ]] || got="v${got}"
  [[ "${got}" == "${ACR_E2E_KIND_VERSION}" ]]
}

require_kind_version() {
  local got
  got="$(kind version -q 2>/dev/null || true)"
  [[ "${got}" == v* ]] || got="v${got}"
  kind_version_matches || die "kind version must be ${ACR_E2E_KIND_VERSION}; got ${got:-<unavailable>}"
}

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

render_pinned_manifest() {
  local source="$1" destination="$2" line from to i
  shift 2
  [[ $(( $# % 2 )) -eq 0 ]] || die "internal image replacement map is invalid"
  local -a replacements=("$@")
  : >"${destination}"
  while IFS= read -r line || [[ -n "${line}" ]]; do
    for ((i = 0; i < ${#replacements[@]}; i += 2)); do
      from="${replacements[$i]}"; to="${replacements[$((i + 1))]}"
      line="${line//$from/$to}"
    done
    printf '%s\n' "${line}" >>"${destination}"
  done <"${source}"
}

assert_manifest_images_pinned() {
  local manifest="$1" unpinned
  unpinned="$(awk '
    /^[[:space:]]*image:[[:space:]]*[^[:space:]#]+/ {
      value = $0
      sub(/^[[:space:]]*image:[[:space:]]*/, "", value)
      sub(/[[:space:]#].*$/, "", value)
      if (value !~ /@sha256:[a-f0-9]{64}$/) print value
    }
  ' "${manifest}")"
  [[ -z "${unpinned}" ]] || {
    fail "rendered manifest has unpinned image references: ${unpinned//$'\n'/, }"
    return 1
  }
}

render_vendor_manifests() {
  local name="$1" sd="$2" calico envoy
  calico="${sd}/manifests/calico.pinned.yaml"
  envoy="${sd}/manifests/envoy-gateway.pinned.yaml"
  render_pinned_manifest "${VENDOR_DIR}/${ACR_E2E_CALICO_MANIFEST}" "${calico}" \
    "quay.io/calico/cni:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_CNI}" \
    "quay.io/calico/node:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_NODE}" \
    "quay.io/calico/kube-controllers:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_KUBE_CONTROLLERS}"
  render_pinned_manifest "${VENDOR_DIR}/${ACR_E2E_ENVOY_GATEWAY_MANIFEST}" "${envoy}" \
    "docker.io/envoyproxy/ratelimit:05c08d03" "${ACR_E2E_IMG_ENVOY_RATELIMIT}" \
    "envoyproxy/gateway:${ACR_E2E_ENVOY_GATEWAY_VERSION}" "${ACR_E2E_IMG_ENVOY_GATEWAY}" \
    "docker.io/envoyproxy/gateway:${ACR_E2E_ENVOY_GATEWAY_VERSION}" "${ACR_E2E_IMG_ENVOY_GATEWAY}"
  assert_manifest_images_pinned "${calico}" || return 1
  assert_manifest_images_pinned "${envoy}" || return 1
  ok "rendered checksum-gated manifests with digest-only image references"
}

fixture_marker_matches() {
  local name="$1" marker_id marker_name marker_node
  marker_id="$(kube "${name}" -n kube-system get configmap "${FIXTURE_MARKER}" -o jsonpath='{.data.fixture_id}' 2>/dev/null || true)"
  marker_name="$(kube "${name}" -n kube-system get configmap "${FIXTURE_MARKER}" -o jsonpath='{.data.fixture_name}' 2>/dev/null || true)"
  marker_node="$(kube "${name}" -n kube-system get configmap "${FIXTURE_MARKER}" -o jsonpath='{.data.node_id}' 2>/dev/null || true)"
  [[ "${marker_id}" == "${FIXTURE_ID}" && "${marker_name}" == "${name}" && "${marker_node}" == "${FIXTURE_NODE_ID}" ]]
}

verify_fixture_ownership() {
  local name="$1" marker_mode="${2:-marker-required}" net reg node current_id kind_label
  net="$(net_name "${name}")"; reg="$(reg_name "${name}")"; node="$(node_name "${name}")"
  if [[ "${CREATE_IN_MEMORY_OWNERSHIP}" -eq 1 ]]; then
    [[ "${FIXTURE_NAME}" == "${name}" ]] || {
      fail "missing or invalid fixture ownership record for ${name}"; return 1
    }
  elif ! load_fixture_identity "${name}"; then
    fail "missing or invalid fixture ownership record for ${name}"
    return 1
  fi

  if ! probe_docker_network "${net}"; then
    fail "cannot determine ownership of network ${net}: ${PROBE_ERROR}"; return 1
  fi
  if [[ "${PROBE_STATE}" == "present" ]]; then
    current_id="${PROBE_ID}"
    [[ -n "${FIXTURE_NETWORK_ID}" && "${current_id}" == "${FIXTURE_NETWORK_ID}" && "$(docker_network_label "${net}")" == "${FIXTURE_ID}" ]] || {
      fail "network ${net} lacks this fixture's exact ownership identity"; return 1
    }
  elif [[ -n "${FIXTURE_NETWORK_ID}" ]]; then
    log "owned network ${net} is already absent"
  fi

  if ! probe_docker_container "${reg}"; then
    fail "cannot determine ownership of registry ${reg}: ${PROBE_ERROR}"; return 1
  fi
  if [[ "${PROBE_STATE}" == "present" ]]; then
    current_id="${PROBE_ID}"
    [[ -n "${FIXTURE_REGISTRY_ID}" && "${current_id}" == "${FIXTURE_REGISTRY_ID}" && "$(docker_container_label "${reg}")" == "${FIXTURE_ID}" ]] || {
      fail "registry ${reg} lacks this fixture's exact ownership identity"; return 1
    }
    if ! [[ -n "${FIXTURE_NETWORK_ID}" ]] || ! network_has_container "${net}" "${reg}"; then
      fail "registry ${reg} is not attached to its recorded fixture network"; return 1
    fi
  elif [[ -n "${FIXTURE_REGISTRY_ID}" ]]; then
    log "owned registry ${reg} is already absent"
  fi

  if ! probe_kind_nodes "${name}"; then
    fail "cannot determine ownership of Kind nodes for ${name}: ${PROBE_ERROR}"; return 1
  fi
  if [[ "${PROBE_STATE}" == "present" ]]; then
    current_id="${PROBE_ID}"
    kind_label="$(docker inspect "${node}" --format '{{ index .Config.Labels "io.x-k8s.kind.cluster" }}' 2>/dev/null || true)"
    [[ -n "${FIXTURE_NODE_ID}" && "${current_id}" == "${FIXTURE_NODE_ID}" && "${kind_label}" == "${name}" ]] || {
      fail "Kind nodes for ${name} differ from the recorded fixture identity"; return 1
    }
    if ! [[ -n "${FIXTURE_NETWORK_ID}" ]] || ! network_has_container "${net}" "${node}"; then
      fail "Kind node ${node} is not attached to its recorded fixture network"; return 1
    fi
    if [[ "${marker_mode}" == "marker-required" ]] && ! fixture_marker_matches "${name}"; then
      fail "Kind cluster ${name} lacks this fixture's exact ownership marker"
      return 1
    fi
  elif [[ -n "${FIXTURE_NODE_ID}" ]]; then
    log "owned Kind nodes for ${name} are already absent"
  fi
}

record_kind_nodes() {
  local name="$1"
  probe_kind_nodes "${name}" || return 1
  [[ "${PROBE_STATE}" == "present" ]] || return 1
  FIXTURE_NODE_ID="${PROBE_ID}"
  write_fixture_identity "${name}"
}

record_cluster_ownership() {
  local name="$1"
  cat <<EOF | kube "${name}" -n kube-system apply -f - >/dev/null
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${FIXTURE_MARKER}
  labels:
    ${FIXTURE_LABEL_KEY}: ${FIXTURE_ID}
data:
  fixture_name: ${name}
  fixture_id: ${FIXTURE_ID}
  node_id: ${FIXTURE_NODE_ID}
  network_id: ${FIXTURE_NETWORK_ID}
EOF
}

cleanup_owned_fixture() {
  local name="$1" marker_mode="${2:-marker-required}" net reg rc=0 node_id
  local -a node_ids=()
  net="$(net_name "${name}")"; reg="$(reg_name "${name}")"
  if [[ "${CREATE_IN_MEMORY_OWNERSHIP}" -ne 1 ]]; then load_fixture_identity "${name}" || return 1; fi
  if [[ "${FIXTURE_PHASE}" == "creating" ]]; then marker_mode="marker-optional"; fi
  verify_fixture_ownership "${name}" "${marker_mode}" || return 1
  probe_kind_nodes "${name}" || { fail "cannot confirm Kind node state before deletion: ${PROBE_ERROR}"; return 1; }
  if [[ "${PROBE_STATE}" == "present" ]]; then
    verify_fixture_ownership "${name}" "${marker_mode}" || return 1
    IFS=',' read -r -a node_ids <<<"${FIXTURE_NODE_ID}"
    for node_id in "${node_ids[@]}"; do
      if docker rm -f "${node_id}" >/dev/null; then ok "Kind node ${node_id} removed"; else fail "Kind node ${node_id} deletion failed"; rc=1; fi
    done
  fi
  if ! probe_docker_container "${reg}"; then
    fail "cannot confirm registry state before deletion: ${PROBE_ERROR}"; rc=1
  elif [[ "${PROBE_STATE}" == "present" ]]; then
    verify_fixture_ownership "${name}" "${marker_mode}" || return 1
    if docker rm -f "${FIXTURE_REGISTRY_ID}" >/dev/null; then ok "registry ${reg} removed"; else fail "registry ${reg} deletion failed"; rc=1; fi
  fi
  if ! probe_docker_network "${net}"; then
    fail "cannot confirm network state before deletion: ${PROBE_ERROR}"; rc=1
  elif [[ "${PROBE_STATE}" == "present" ]]; then
    verify_fixture_ownership "${name}" "${marker_mode}" || return 1
    if docker network rm "${FIXTURE_NETWORK_ID}" >/dev/null; then ok "network ${net} removed"; else fail "network ${net} deletion failed"; rc=1; fi
  fi
  probe_kind_nodes "${name}" || { fail "cannot confirm Kind node absence after cleanup: ${PROBE_ERROR}"; rc=1; }
  [[ "${PROBE_STATE}" == "absent" ]] || { fail "Kind nodes for ${name} remain after cleanup"; rc=1; }
  probe_docker_container "${reg}" || { fail "cannot confirm registry absence after cleanup: ${PROBE_ERROR}"; rc=1; }
  [[ "${PROBE_STATE}" == "absent" ]] || { fail "registry ${reg} remains after cleanup"; rc=1; }
  probe_docker_network "${net}" || { fail "cannot confirm network absence after cleanup: ${PROBE_ERROR}"; rc=1; }
  [[ "${PROBE_STATE}" == "absent" ]] || { fail "network ${net} remains after cleanup"; rc=1; }
  if [[ "${rc}" -eq 0 ]]; then
    rm -rf "$(state_dir "${name}")"
    ok "fixture state for ${name} removed"
  else
    fail "fixture ${name} cleanup incomplete; ownership record retained for retry"
  fi
  return "${rc}"
}

CREATE_ROLLBACK_ARMED=0
CREATE_ROLLBACK_NAME=""
rollback_create_on_exit() {
  local rc="$1" name="$2"
  if [[ "${CREATE_ROLLBACK_ARMED}" -eq 1 && "${rc}" -ne 0 ]]; then
    trap - EXIT
    fail "create failed; attempting owned rollback for ${name}"
    set +e
    cleanup_owned_fixture "${name}" marker-optional
    local cleanup_rc=$?
    if [[ "${cleanup_rc}" -ne 0 ]]; then
      fail "rollback for ${name} was incomplete; retained ownership record for a safe retry"
    fi
  fi
  release_fixture_lock
  return "${rc}"
}

release_lock_on_exit() {
  local rc="$1"
  if ! restore_backend_tls_policy; then rc=1; fi
  pf_stop
  release_fixture_lock
  return "${rc}"
}

test_fail_after() {
  if [[ "${ACR_E2E_TEST_FAIL_AFTER:-}" == "$1" ]]; then
    die "injected create failure after $1"
  fi
}

# ===========================================================================
# CREATE
# ===========================================================================
cmd_create() {
  local name="$1"
  validate_name "${name}"
  acquire_fixture_lock "${name}"
  CREATE_ROLLBACK_NAME="${name}"
  trap 'rollback_create_on_exit "$?" "$CREATE_ROLLBACK_NAME"' EXIT
  command -v kind >/dev/null 2>&1 || die "kind not installed"
  command -v kubectl >/dev/null 2>&1 || die "kubectl not installed"
  command -v openssl >/dev/null 2>&1 || die "openssl not installed"
  require_kind_version

  # Unique-resource guard: refuse to reuse an existing cluster, network, OR
  # registry name (fail closed) so two fixtures can never share isolation state.
  local net reg
  net="$(net_name "${name}")"; reg="$(reg_name "${name}")"
  probe_kind_cluster "${name}" || die "cannot determine whether cluster ${name} already exists: ${PROBE_ERROR}"
  if [[ "${PROBE_STATE}" == "present" ]]; then
    die "cluster already exists: ${name} (reused name refused)"
  fi
  probe_docker_network "${net}" || die "cannot determine whether network ${net} already exists: ${PROBE_ERROR}"
  if [[ "${PROBE_STATE}" == "present" ]]; then
    die "docker network already exists: ${net} (reused name refused)"
  fi
  probe_docker_container "${reg}" || die "cannot determine whether registry ${reg} already exists: ${PROBE_ERROR}"
  if [[ "${PROBE_STATE}" == "present" ]]; then
    die "registry container already exists: ${reg} (reused name refused)"
  fi

  verify_vendor_checksums || die "vendored manifest integrity gate failed"

  local sd; sd="$(state_dir "${name}")"
  [[ ! -e "${sd}" ]] || die "fixture state already exists: ${sd} (reused name refused)"
  mkdir -p "${sd}/certs" "${sd}/manifests"
  new_fixture_identity "${name}"
  CREATE_ROLLBACK_ARMED=1

  gen_certs "${name}" "${sd}"
  preload_images
  render_vendor_manifests "${name}" "${sd}"
  provision_registry_network "${name}"
  test_fail_after registry-network
  create_cluster "${name}" "${sd}"
  test_fail_after cluster
  install_calico "${name}" "${sd}"
  install_gateway_api "${name}"
  install_envoy_gateway "${name}" "${sd}"
  deploy_dependencies "${name}" "${sd}"
  deploy_gateway_route "${name}" "${sd}"
  apply_network_policies "${name}"
  wait_gateway_programmed "${name}"
  write_exports "${name}" "${sd}"

  CREATE_ROLLBACK_ARMED=0
  CREATE_IN_MEMORY_OWNERSHIP=0
  CREATE_ROLLBACK_NAME=""
  release_fixture_lock
  trap - EXIT
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
EOF
    if [[ "${svc}" == "acr-gateway" ]]; then
      printf 'DNS.1 = acr.local\n' >>"${cdir}/${svc}.cnf"
    else
      cat >>"${cdir}/${svc}.cnf" <<EOF
DNS.1 = ${svc}
DNS.2 = ${svc}.${NS_DEPS}
DNS.3 = ${svc}.${NS_DEPS}.svc
DNS.4 = ${svc}.${NS_DEPS}.svc.cluster.local
EOF
    fi
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
  FIXTURE_NETWORK_ID="$(docker network create --label "${FIXTURE_LABEL_KEY}=${FIXTURE_ID}" "${net}")" || die "failed to create network ${net}"
  [[ "${FIXTURE_NETWORK_ID}" =~ ^[a-f0-9]{64}$ ]] || die "failed to record network ownership identity"
  write_fixture_identity "${name}"
  # Registry attached ONLY to this fixture's network; no -p host publish.
  FIXTURE_REGISTRY_ID="$(docker run -d --restart=no --name "${reg}" --network "${net}" --label "${FIXTURE_LABEL_KEY}=${FIXTURE_ID}" \
    -e REGISTRY_HTTP_ADDR=0.0.0.0:5000 "${ACR_E2E_IMG_REGISTRY}")" \
    || die "failed to start registry ${reg}"
  [[ "${FIXTURE_REGISTRY_ID}" =~ ^[a-f0-9]{64}$ ]] || die "failed to record registry ownership identity"
  write_fixture_identity "${name}"
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
  if ! KIND_EXPERIMENTAL_DOCKER_NETWORK="$(net_name "${name}")" \
    kind create cluster --name "${name}" --config "${sd}/kind-config.yaml"; then
    record_kind_nodes "${name}" || true
    die "failed to create Kind cluster ${name}"
  fi
  if [[ "${ACR_E2E_TEST_FAIL_AFTER:-}" == "kind-created-before-record" ]]; then
    record_kind_nodes "${name}" || die "failed to reconcile Kind nodes after injected failure"
    die "injected create failure after Kind created before normal ownership record"
  fi
  record_kind_nodes "${name}" || die "failed to record Kind node ownership identity"
  record_cluster_ownership "${name}"
  FIXTURE_PHASE="ready"
  write_fixture_identity "${name}"
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
  local name="$1" sd="$2"
  log "applying pinned Calico ${ACR_E2E_CALICO_VERSION}"
  kube "${name}" apply -f "${sd}/manifests/calico.pinned.yaml" >/dev/null
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
  local name="$1" sd="$2"
  log "applying pinned Envoy Gateway ${ACR_E2E_ENVOY_GATEWAY_VERSION}"
  # Envoy Gateway's install manifest bundles its OWN Gateway API CRDs
  # (experimental channel). We already installed the pinned STANDARD Gateway
  # API v1.5.1, and the standard safe-upgrades ValidatingAdmissionPolicy
  # forbids overlaying experimental CRDs. Apply EG from the checksum-verified
  # vendored bytes but drop only its gateway.networking.k8s.io CRD documents;
  # every EG-owned resource (gateway.envoyproxy.io CRDs, RBAC, Deployment,
  # webhooks, Job) is preserved.
  local eg_filtered="${sd}/manifests/envoy-gateway.filtered.yaml"
  filter_gwapi_crds "${sd}/manifests/envoy-gateway.pinned.yaml" >"${eg_filtered}"
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
  ok "Envoy Gateway ready; runtime image IDs will be checked by verify"
}

deploy_dependencies() {
  local name="$1" sd="$2" cdir; cdir="${sd}/certs"
  log "deploying TLS external dependency fixtures"
  kube "${name}" create namespace "${NS_DEPS}" >/dev/null 2>&1 || true
  kube "${name}" label namespace "${NS_DEPS}" "${FIXTURE_LABEL_KEY}=${FIXTURE_ID}" --overwrite >/dev/null

  # Shared CA + per-service TLS material as secrets.
  kube "${name}" -n "${NS_DEPS}" create secret generic acr-e2e-ca \
    --from-file=ca.crt="${cdir}/ca.crt" --dry-run=client -o yaml | kube "${name}" apply -f - >/dev/null
  kube "${name}" -n "${NS_DEPS}" create configmap acr-e2e-ca \
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
  bootstrap_clickhouse_catalog "${name}"
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
          <readonly>2</readonly>
        </readonly_profile>
      </profiles>
      <users>
        <default>
          <password></password>
          <profile>readonly_profile</profile>
          <networks><ip>::/0</ip></networks>
          <quota>default</quota>
        </default>
        <fixture_admin>
          <password></password>
          <profile>default</profile>
          <networks><ip>127.0.0.1</ip><ip>::1</ip></networks>
          <quota>default</quota>
        </fixture_admin>
      </users>
    </clickhouse>
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: clickhouse-tls
  namespace: ${NS_DEPS}
data:
  tls.xml: |
    <clickhouse>
      <tcp_port_secure>9440</tcp_port_secure>
      <openSSL>
        <server>
          <certificateFile>/tls/tls.crt</certificateFile>
          <privateKeyFile>/tls/tls.key</privateKeyFile>
          <caConfig>/tls/ca.crt</caConfig>
          <verificationMode>none</verificationMode>
        </server>
      </openSSL>
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
          ports: [{ containerPort: 8123 }, { containerPort: 9000 }, { containerPort: 9440 }]
          volumeMounts:
            - { name: readonly, mountPath: /etc/clickhouse-server/users.d/readonly.xml, subPath: readonly.xml }
            - { name: tls-config, mountPath: /etc/clickhouse-server/config.d/tls.xml, subPath: tls.xml }
            - { name: tls, mountPath: /tls, readOnly: true }
          readinessProbe:
            httpGet: { path: /ping, port: 8123 }
            initialDelaySeconds: 5
            periodSeconds: 5
      volumes:
        - { name: readonly, configMap: { name: clickhouse-readonly } }
        - { name: tls-config, configMap: { name: clickhouse-tls } }
        - { name: tls, secret: { secretName: tls-clickhouse } }
---
apiVersion: v1
kind: Service
metadata:
  name: clickhouse
  namespace: ${NS_DEPS}
  labels: { app: clickhouse }
spec:
  selector: { app: clickhouse }
  ports: [{ name: http, port: 8123, targetPort: 8123 }, { name: native, port: 9000, targetPort: 9000 }, { name: secure-native, port: 8443, targetPort: 9440 }]
EOF
}

bootstrap_clickhouse_catalog() {
  local name="$1"
  kube "${name}" -n "${NS_DEPS}" exec deploy/clickhouse -- \
    clickhouse-client --host 127.0.0.1 --user fixture_admin --query \
    'CREATE TABLE IF NOT EXISTS repos (id UUID, org_id String, repo String, ref Nullable(String)) ENGINE = ReplacingMergeTree ORDER BY (org_id, repo)'
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
      location = /api/v1/internal/acr/health {
        default_type application/json;
        return 200 '{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok"}';
      }
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
  ports: [{ name: https, port: 8443, targetPort: 8443 }]
EOF
}

deploy_gateway_route() {
  local name="$1" sd="$2" cdir; cdir="${sd}/certs"
  log "provisioning north-south Gateway + HTTPRoute"
  kube "${name}" create namespace "${NS_GW}" >/dev/null 2>&1 || true
  kube "${name}" label namespace "${NS_GW}" "${FIXTURE_LABEL_KEY}=${FIXTURE_ID}" --overwrite >/dev/null
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
---
apiVersion: gateway.networking.k8s.io/v1
kind: BackendTLSPolicy
metadata:
  name: ops-entitlement
  namespace: ${NS_DEPS}
  labels:
    ${FIXTURE_LABEL_KEY}: ${FIXTURE_ID}
spec:
  targetRefs:
    - group: ""
      kind: Service
      name: ops-entitlement
      sectionName: https
  validation:
    hostname: ops-entitlement.${NS_DEPS}.svc.cluster.local
    caCertificateRefs:
      - group: ""
        kind: ConfigMap
        name: acr-e2e-ca
EOF
  ok "Gateway, HTTPRoute, and BackendTLSPolicy applied; north-south success deferred to verify"
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
        - namespaceSelector: { matchLabels: { acr-e2e/access: allowed } }
          podSelector: { matchLabels: { acr-e2e/access: allowed } }
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
RUNTIME_IMAGE_REFS=()
RUNTIME_IMAGE_IDS=()
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
  acquire_fixture_lock "${name}"
  trap 'release_lock_on_exit "$?"' EXIT
  probe_kind_cluster "${name}" || die "cannot determine whether cluster ${name} exists: ${PROBE_ERROR}"
  [[ "${PROBE_STATE}" == "present" ]] || die "cluster not found: ${name}"
  local sd; sd="$(state_dir "${name}")"
  VERIFY_FAILURES=0
  RUNTIME_IMAGE_REFS=()
  RUNTIME_IMAGE_IDS=()

  check "exact Kind version ${ACR_E2E_KIND_VERSION}" kind_version_matches
  verify_fixture_ownership "${name}" || die "fixture ownership verification failed for ${name}"

  # 1. Vendored manifest checksums (tamper detection).
  if ! verify_vendor_checksums; then VERIFY_FAILURES=$((VERIFY_FAILURES+1)); fi

  # 2. Pinned kind node image digest is exactly what we pinned.
  verify_node_pin "${name}"

  # 3. Default CNI disabled + Calico running from explicit digest references.
  check "default CNI disabled (no kindnet daemonset)" \
    bash -c "! kubectl --context kind-${name} -n kube-system get ds kindnet >/dev/null 2>&1"
  verify_pod_image "${name}" kube-system "k8s-app=calico-node" initContainers upgrade-ipam "${ACR_E2E_IMG_CALICO_CNI}"
  verify_pod_image "${name}" kube-system "k8s-app=calico-node" initContainers install-cni "${ACR_E2E_IMG_CALICO_CNI}"
  verify_pod_image "${name}" kube-system "k8s-app=calico-node" initContainers ebpf-bootstrap "${ACR_E2E_IMG_CALICO_NODE}"
  verify_pod_image "${name}" kube-system "k8s-app=calico-node" containers calico-node "${ACR_E2E_IMG_CALICO_NODE}"
  verify_pod_image "${name}" kube-system "k8s-app=calico-kube-controllers" containers calico-kube-controllers "${ACR_E2E_IMG_CALICO_KUBE_CONTROLLERS}"

  # 4. Gateway API CRDs at the pinned version.
  check "Gateway API CRD gateways established" \
    kube "${name}" get crd gateways.gateway.networking.k8s.io
  check "Gateway API bundle version ${ACR_E2E_GATEWAY_API_VERSION}" \
    bash -c "kubectl --context kind-${name} get crd gateways.gateway.networking.k8s.io -o jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}' | grep -qx ${ACR_E2E_GATEWAY_API_VERSION}"

  # 5. Envoy Gateway control plane + data-plane are digest-configured and running.
  verify_pod_image "${name}" envoy-gateway-system "control-plane=envoy-gateway" containers envoy-gateway "${ACR_E2E_IMG_ENVOY_GATEWAY}"
  verify_pod_image "${name}" envoy-gateway-system "gateway.envoyproxy.io/owning-gateway-name=acr-gateway,gateway.envoyproxy.io/owning-gateway-namespace=${NS_GW}" containers envoy "${ACR_E2E_IMG_ENVOY_PROXY}"
  verify_pod_image "${name}" envoy-gateway-system "gateway.envoyproxy.io/owning-gateway-name=acr-gateway,gateway.envoyproxy.io/owning-gateway-namespace=${NS_GW}" containers shutdown-manager "${ACR_E2E_IMG_ENVOY_GATEWAY}"

  # 6. External dependency health.
  check "postgres deployment available" \
    kube "${name}" -n "${NS_DEPS}" wait --for=condition=Available deploy/postgres --timeout=10s
  check "clickhouse deployment available" \
    kube "${name}" -n "${NS_DEPS}" wait --for=condition=Available deploy/clickhouse --timeout=10s
  check "ops-entitlement deployment available" \
    kube "${name}" -n "${NS_DEPS}" wait --for=condition=Available deploy/ops-entitlement --timeout=10s
  verify_pod_image "${name}" "${NS_DEPS}" "app=postgres" initContainers tls-perms "${ACR_E2E_IMG_PROBE}"
  verify_pod_image "${name}" "${NS_DEPS}" "app=postgres" containers postgres "${ACR_E2E_IMG_POSTGRES}"
  verify_pod_image "${name}" "${NS_DEPS}" "app=clickhouse" containers clickhouse "${ACR_E2E_IMG_CLICKHOUSE}"
  verify_pod_image "${name}" "${NS_DEPS}" "app=ops-entitlement" containers nginx "${ACR_E2E_IMG_OPS_ENTITLEMENT}"

  # 7. TLS chains: every leaf verifies against the fixture CA.
  verify_tls_chain "${name}" postgres 5432 postgres
  verify_tls_chain "${name}" clickhouse 8443
  verify_tls_chain "${name}" ops-entitlement 8443

  # 8. Read-only ClickHouse: SELECT ok, INSERT denied.
  verify_clickhouse_readonly "${name}"

  # 9. Programmed Gateway + resolved backend TLS + real north-south request.
  check "Gateway Programmed=True" \
    bash -c "kubectl --context kind-${name} -n ${NS_GW} get gateway acr-gateway -o jsonpath='{.status.conditions[?(@.type==\"Programmed\")].status}' | grep -qx True"
  check "HTTPRoute Accepted=True" \
    bash -c "kubectl --context kind-${name} -n ${NS_DEPS} get httproute ops-entitlement -o jsonpath='{.status.parents[0].conditions[?(@.type==\"Accepted\")].status}' | grep -qx True"
  check "HTTPRoute ResolvedRefs=True" \
    bash -c "kubectl --context kind-${name} -n ${NS_DEPS} get httproute ops-entitlement -o jsonpath='{.status.parents[0].conditions[?(@.type==\"ResolvedRefs\")].status}' | grep -qx True"
  check "BackendTLSPolicy Accepted=True" \
    bash -c "kubectl --context kind-${name} -n ${NS_DEPS} get backendtlspolicy ops-entitlement -o jsonpath='{.status.ancestors[0].conditions[?(@.type==\"Accepted\")].status}' | grep -qx True"
  check "BackendTLSPolicy ResolvedRefs=True" \
    bash -c "kubectl --context kind-${name} -n ${NS_DEPS} get backendtlspolicy ops-entitlement -o jsonpath='{.status.ancestors[0].conditions[?(@.type==\"ResolvedRefs\")].status}' | grep -qx True"
  verify_north_south_entitlement "${name}"

  # 10. NetworkPolicy enforcement (deny-by-default proven).
  verify_network_policy "${name}"

  # 11. Unique resources: docker objects carry the fixture name.
  check "kind node container name scoped to fixture" \
    bash -c "docker ps --format '{{.Names}}' | grep -qx ${name}-control-plane"

  # 12. Per-fixture registry/network isolation ownership (real Docker state).
  verify_host_container_image "$(reg_name "${name}")" "${ACR_E2E_IMG_REGISTRY}"
  verify_isolation "${name}"

  if [[ "${VERIFY_FAILURES}" -eq 0 ]]; then
    write_verification_evidence "${name}" "${sd}"
    ok "verify passed for ${name}"
    trap - EXIT
    release_fixture_lock
    return 0
  fi
  fail "verify found ${VERIFY_FAILURES} violation(s) for ${name}"
  restore_backend_tls_policy || return 1
  trap - EXIT
  release_fixture_lock
  return 1
}

verify_node_pin() {
  local name="$1" want_id got_id repo_digests want_repo_digest
  want_id="$(docker image inspect "${ACR_E2E_NODE_IMAGE}" --format '{{.Id}}' 2>/dev/null || true)"
  got_id="$(docker inspect "$(node_name "${name}")" --format '{{.Image}}' 2>/dev/null || true)"
  repo_digests="$(docker image inspect "${ACR_E2E_NODE_IMAGE}" --format '{{range .RepoDigests}}{{.}}{{"\n"}}{{end}}' 2>/dev/null || true)"
  want_repo_digest="${ACR_E2E_NODE_IMAGE%%:*}@${ACR_E2E_NODE_IMAGE##*@}"
  if [[ -n "${want_id}" && "${got_id}" == "${want_id}" ]] && grep -Fxq "${want_repo_digest}" <<<"${repo_digests}"; then
    RUNTIME_IMAGE_REFS+=("${ACR_E2E_NODE_IMAGE}")
    RUNTIME_IMAGE_IDS+=("${got_id}")
    ok "Kind node image ID exactly matches pins.env digest"
  else
    fail "Kind node image ID does not exactly match ${ACR_E2E_NODE_IMAGE}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
  fi
}

verify_pod_image() {
  local name="$1" ns="$2" selector="$3" type="$4" container="$5" want_ref="$6" specs image_ids spec status_type image_id invalid=0
  if [[ "${type}" == "initContainers" ]]; then status_type="initContainerStatuses"; else status_type="containerStatuses"; fi
  spec="{range .items[*]}{range .spec.${type}[?(@.name==\"${container}\")]}{.image}{\"\\n\"}{end}{end}"
  status="{range .items[*]}{range .status.${status_type}[?(@.name==\"${container}\")]}{.imageID}{\"\\n\"}{end}{end}"
  specs="$(kube "${name}" -n "${ns}" get pods -l "${selector}" -o jsonpath="${spec}" 2>/dev/null || true)"
  image_ids="$(kube "${name}" -n "${ns}" get pods -l "${selector}" -o jsonpath="${status}" 2>/dev/null || true)"
  if [[ -z "${specs}" || -z "${image_ids}" ]]; then
    fail "${ns}/${selector}/${container}: missing configured image or runtime image ID"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  if grep -Fxvq "${want_ref}" <<<"${specs}" || grep -Evq 'sha256:[a-f0-9]{64}' <<<"${image_ids}"; then
    fail "${ns}/${selector}/${container}: image reference or runtime image ID is not digest-pinned"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  while IFS= read -r image_id || [[ -n "${image_id}" ]]; do
    [[ -n "${image_id}" ]] || continue
    if ! runtime_image_id_matches_pin "${name}" "${image_id}" "${want_ref}"; then invalid=1; fi
  done <<<"${image_ids}"
  if [[ "${invalid}" -ne 0 ]]; then
    fail "${ns}/${selector}/${container}: runtime image ID does not resolve to pins.env digest"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  ok "${ns}/${selector}/${container} uses pins.env digest and exposes a runtime image ID"
}

runtime_image_id_matches_pin() {
  local name="$1" image_id="$2" want_ref="$3" runtime_ref output
  runtime_ref="${image_id#docker-pullable://}"
  runtime_ref="${runtime_ref#docker://}"
  output="$(docker exec "$(node_name "${name}")" crictl inspecti "${runtime_ref}" 2>&1)" || return 1
  grep -Fq "${want_ref}" <<<"${output}" || return 1
  RUNTIME_IMAGE_REFS+=("${want_ref}")
  RUNTIME_IMAGE_IDS+=("${image_id}")
}

# Start a scoped kubectl port-forward to a Service, echo the local port, and
# record the PID in PF_PID. port-forward reaches the pod via the kubelet, so it
# is intentionally NOT subject to NetworkPolicy (policy is checked separately
# with in-cluster probes) and lets the host openssl/curl inspect TLS and HTTP.
PF_PID=""; PF_LPORT=""
pf_start() { # ctx ns target rport -> sets globals PF_PID and PF_LPORT
  # Must run in the PARENT shell (not a command substitution) so PF_PID is
  # visible to pf_stop; otherwise the backgrounded forward would leak.
  local ctx="$1" ns="$2" tgt="$3" rport="$4" i ready=0
  PF_LPORT=$(( (RANDOM % 20000) + 20000 ))
  kubectl --context "${ctx}" -n "${ns}" port-forward "svc/${tgt}" "${PF_LPORT}:${rport}" >/dev/null 2>&1 &
  PF_PID=$!
  for i in $(seq 1 40); do
    # The probe fd is opened in a subshell, so nothing to close in this shell.
    # (A bare `exec ... 2>/dev/null` here would silence the whole script.)
    if (exec 3<>"/dev/tcp/127.0.0.1/${PF_LPORT}") 2>/dev/null; then ready=1; break; fi
    sleep 0.25
  done
  if [[ "${ready}" -ne 1 ]]; then
    pf_stop
    return 1
  fi
}
pf_stop() {
  [[ -n "${PF_PID:-}" ]] || return 0
  kill "${PF_PID}" 2>/dev/null || true
  wait "${PF_PID}" 2>/dev/null || true
  PF_PID=""
}

gateway_service_name() {
  local name="$1" output service="" candidate count=0
  output="$(kube "${name}" -n envoy-gateway-system get service \
    -l "gateway.envoyproxy.io/owning-gateway-name=acr-gateway,gateway.envoyproxy.io/owning-gateway-namespace=${NS_GW}" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null)" || return 1
  while IFS= read -r candidate || [[ -n "${candidate}" ]]; do
    [[ -n "${candidate}" ]] || continue
    service="${candidate}"
    count=$((count + 1))
  done <<<"${output}"
  [[ "${count}" -eq 1 ]] || return 1
  printf '%s\n' "${service}"
}

verify_north_south_entitlement() {
  local name="$1" service lport body ca i backend_host wrong_host negative_observed=0 positive_observed=0 baseline_observed=0 consecutive_failures=0
  verify_fixture_ownership "${name}" || {
    fail "north-south entitlement: fixture ownership changed before BackendTLSPolicy control"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  }
  service="$(gateway_service_name "${name}" || true)"
  if [[ -z "${service}" ]]; then
    fail "north-south entitlement: no unique Envoy Gateway Service for acr-gateway"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  ca="$(state_dir "${name}")/certs/ca.crt"
  backend_host="ops-entitlement.${NS_DEPS}.svc.cluster.local"
  wrong_host="wrong-${backend_host}"
  if ! pf_start "kind-${name}" envoy-gateway-system "${service}" 443; then
    fail "north-south entitlement: gateway port-forward did not become ready"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  lport="${PF_LPORT}"
  for i in $(seq 1 10); do
    body="$(gateway_entitlement_response "${ca}" "${lport}")"
    if entitlement_response_active "${body}"; then baseline_observed=1; break; fi
    sleep 1
  done
  if [[ "${baseline_observed}" -ne 1 ]]; then
    pf_stop
    fail "north-south entitlement baseline did not return active HTTPS response: ${body}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  VERIFY_BACKEND_TLS_RESTORE_NAME="${name}"
  VERIFY_BACKEND_TLS_RESTORE_HOST="${backend_host}"
  if ! kube "${name}" -n "${NS_DEPS}" patch backendtlspolicy ops-entitlement --type merge \
    -p "{\"spec\":{\"validation\":{\"hostname\":\"${wrong_host}\"}}}" >/dev/null; then
    restore_backend_tls_policy || true
    pf_stop
    fail "north-south entitlement: could not apply wrong-SAN BackendTLSPolicy control"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  for i in $(seq 1 20); do
    body="$(gateway_entitlement_response "${ca}" "${lport}")"
    if grep -Fq '__HTTP_503__' <<<"${body}"; then
      consecutive_failures=$((consecutive_failures + 1))
      if [[ "${consecutive_failures}" -eq 2 ]]; then negative_observed=1; break; fi
    else
      consecutive_failures=0
    fi
    sleep 1
  done
  if ! restore_backend_tls_policy; then
    pf_stop
    fail "north-south entitlement: could not restore BackendTLSPolicy hostname"; VERIFY_FAILURES=$((VERIFY_FAILURES+1)); return
  fi
  for i in $(seq 1 20); do
    body="$(gateway_entitlement_response "${ca}" "${lport}")"
    if entitlement_response_active "${body}"; then positive_observed=1; break; fi
    sleep 1
  done
  pf_stop
  if [[ "${negative_observed}" -eq 1 && "${positive_observed}" -eq 1 ]]; then
    ok "north-south HTTPS entitlement requires BackendTLSPolicy hostname validation"
  else
    fail "north-south HTTPS entitlement did not prove BackendTLSPolicy hostname validation: ${body}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
  fi
}

restore_backend_tls_policy() {
  local i
  [[ -n "${VERIFY_BACKEND_TLS_RESTORE_NAME}" ]] || return 0
  for i in $(seq 1 3); do
    if kube "${VERIFY_BACKEND_TLS_RESTORE_NAME}" -n "${NS_DEPS}" patch backendtlspolicy ops-entitlement --type merge \
      -p "{\"spec\":{\"validation\":{\"hostname\":\"${VERIFY_BACKEND_TLS_RESTORE_HOST}\"}}}" >/dev/null; then
      VERIFY_BACKEND_TLS_RESTORE_NAME=""
      VERIFY_BACKEND_TLS_RESTORE_HOST=""
      return 0
    fi
    sleep 1
  done
  return 1
}

gateway_entitlement_response() {
  local ca="$1" lport="$2"
  curl --noproxy '*' --max-time 5 --silent --show-error --cacert "${ca}" \
    --resolve "acr.local:${lport}:127.0.0.1" -w '\n__HTTP_%{http_code}__' \
    "https://acr.local:${lport}/entitlement" 2>&1 || true
}

entitlement_response_active() {
  local body="$1"
  grep -Fq '"entitlement":"agent_context_runtime"' <<<"${body}" \
    && grep -Fq '"status":"active"' <<<"${body}" \
    && grep -Fq '__HTTP_200__' <<<"${body}"
}

verify_tls_chain() {
  local name="$1" svc="$2" port="$3" starttls="${4:-}"
  local ctx="kind-${name}" ca; ca="$(state_dir "${name}")/certs/ca.crt"
  local lport out st_opt=""
  [[ -n "${starttls}" ]] && st_opt="-starttls ${starttls}"
  pf_start "${ctx}" "${NS_DEPS}" "${svc}" "${port}"; lport="${PF_LPORT}"
  # shellcheck disable=SC2086  # st_opt must word-split into openssl flags
  out="$(openssl s_client -no_ign_eof ${st_opt} -connect "127.0.0.1:${lport}" -CAfile "${ca}" -servername "${svc}" </dev/null 2>&1 || true)"
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
  check "fixture network ${net} has exact ownership label" \
    bash -c "[[ \"\$(docker network inspect ${net} --format '{{ index .Labels \"${FIXTURE_LABEL_KEY}\" }}' 2>/dev/null)\" == \"${FIXTURE_ID}\" ]]"
  check "fixture registry ${reg} running" \
    bash -c "[[ \"\$(docker inspect -f '{{.State.Running}}' ${reg} 2>/dev/null)\" == true ]]"
  check "fixture registry ${reg} has exact ownership label" \
    bash -c "[[ \"\$(docker inspect ${reg} --format '{{ index .Config.Labels \"${FIXTURE_LABEL_KEY}\" }}' 2>/dev/null)\" == \"${FIXTURE_ID}\" ]]"
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

verify_host_container_image() {
  local container="$1" want_ref="$2" configured want_id actual_id
  configured="$(docker inspect "${container}" --format '{{.Config.Image}}' 2>/dev/null || true)"
  want_id="$(docker image inspect "${want_ref}" --format '{{.Id}}' 2>/dev/null || true)"
  actual_id="$(docker inspect "${container}" --format '{{.Image}}' 2>/dev/null || true)"
  if [[ "${configured}" == "${want_ref}" && -n "${want_id}" && "${actual_id}" == "${want_id}" ]]; then
    RUNTIME_IMAGE_REFS+=("${want_ref}")
    RUNTIME_IMAGE_IDS+=("${actual_id}")
    ok "${container} uses pins.env digest and exact runtime image ID"
  else
    fail "${container} does not exactly match pinned image ${want_ref}"; VERIFY_FAILURES=$((VERIFY_FAILURES+1))
  fi
}

write_verification_evidence() {
  local name="$1" sd="$2" i
  cat >"${sd}/verification-evidence.env" <<EOF
fixture_name=${name}
fixture_id=${FIXTURE_ID}
kind_version=${ACR_E2E_KIND_VERSION}
node_image=${ACR_E2E_NODE_IMAGE}
north_south_https_entitlement=observed
verified_at_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
  for ((i = 0; i < ${#RUNTIME_IMAGE_IDS[@]}; i += 1)); do
    printf 'runtime_image_ref_%d=%s\n' "$((i + 1))" "${RUNTIME_IMAGE_REFS[$i]}" >>"${sd}/verification-evidence.env"
    printf 'runtime_image_id_%d=%s\n' "$((i + 1))" "${RUNTIME_IMAGE_IDS[$i]}" >>"${sd}/verification-evidence.env"
  done
  ok "wrote observed verification evidence"
}

# ===========================================================================
# DESTROY
# ===========================================================================
cmd_destroy() {
  local name="$1"
  validate_name "${name}"
  acquire_fixture_lock "${name}"
  trap 'release_lock_on_exit "$?"' EXIT
  cleanup_owned_fixture "${name}"
  trap - EXIT
  release_fixture_lock
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

if [[ "${ACR_E2E_LIB_ONLY:-0}" != "1" ]]; then
  main "$@"
fi
