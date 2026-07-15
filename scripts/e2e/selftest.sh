#!/usr/bin/env bash
#
# scripts/e2e/selftest.sh
#
# Isolation self-test for the pinned Kind/TLS fixture (plan Todo 18). Proves,
# against REAL Docker/Kind state, that each fixture owns a uniquely named local
# registry container and Docker network derived from its cluster name, that the
# fixture's node and registry share ONLY that network, that the registry serves
# on it, and that two concurrent fixtures are mutually isolated (no host-global
# or cross-fixture collision).
#
# This is written failing-first: run against a fixture built before per-fixture
# registry/network provisioning exists, every ownership assertion fails.
#
# Subcommands:
#   static                         lock hardening regressions without Docker/Kind
#   single --name <cluster>        assert one fixture's registry/network isolation
#   pair   --a <clusterA> --b <clusterB>   assert both, plus mutual isolation
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/pins.env"

PROBE_IMG="${ACR_E2E_IMG_PROBE:-docker.io/library/busybox@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028}"
FIXTURE_SCRIPT="${SCRIPT_DIR}/kind-fixture.sh"
STATE_ROOT="${ACR_E2E_STATE_ROOT:-${REPO_ROOT}/.tmp/e2e}"
FIXTURE_LABEL_KEY="acr-e2e.fullchaos.dev/fixture-id"

FAILURES=0
ok()   { printf '[selftest] ok: %s\n' "$*" >&2; }
bad()  { printf '[selftest] FAIL: %s\n' "$*" >&2; FAILURES=$((FAILURES+1)); }
die()  { printf '[selftest] FATAL: %s\n' "$*" >&2; exit 2; }

net_name()  { echo "$1-net"; }
reg_name()  { echo "$1-registry"; }
node_name() { echo "$1-control-plane"; }
identity_path() { echo "${STATE_ROOT}/$1/fixture-identity.env"; }

# Is docker object $2 (a container name) attached to docker network $1?
net_has_container() {
  docker network inspect "$1" --format '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null | tr ' ' '\n' | grep -qx "$2"
}

identity_value() {
  local name="$1" key="$2" file
  file="$(identity_path "${name}")"
  [[ -f "${file}" ]] || return 1
  awk -F= -v key="${key}" '$1 == key { print $2 }' "${file}"
}

assert_fixture_script_contains() {
  local expression="$1" description="$2"
  if grep -Eq -- "${expression}" "${FIXTURE_SCRIPT}"; then ok "${description}"; else bad "${description}"; fi
}

assert_fixture_script_lacks() {
  local expression="$1" description="$2"
  if grep -Eq -- "${expression}" "${FIXTURE_SCRIPT}"; then bad "${description}"; else ok "${description}"; fi
}

assert_rendering_with_bash() {
  local shell="$1" rendered
  # shellcheck disable=SC2016
  if rendered="$("${shell}" -c '
    ACR_E2E_LIB_ONLY=1
    export ACR_E2E_LIB_ONLY
    source "$1"
    tmp="$(mktemp -d "${TMPDIR:-/tmp}/acr-kind-selftest.XXXXXX")"
    trap "rm -rf \"${tmp}\"" EXIT
    render_pinned_manifest "$2/vendor/calico.yaml" "${tmp}/calico.yaml" \
      "quay.io/calico/cni:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_CNI}" \
      "quay.io/calico/node:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_NODE}" \
      "quay.io/calico/kube-controllers:v${ACR_E2E_CALICO_VERSION#v}" "${ACR_E2E_IMG_CALICO_KUBE_CONTROLLERS}"
    assert_manifest_images_pinned "${tmp}/calico.yaml"
    grep -Fxq "          image: ${ACR_E2E_IMG_CALICO_NODE}" "${tmp}/calico.yaml"
  ' -- "${FIXTURE_SCRIPT}" "${SCRIPT_DIR}" 2>&1)"; then
    ok "${shell} renders digest-only Calico image references"
  else
    bad "${shell} cannot render digest-only Calico image references: ${rendered}"
  fi
}

assert_static_hardening() {
  assert_fixture_script_contains 'require_kind_version' 'Kind version gate is implemented'
  assert_fixture_script_contains 'kind version -q' 'Kind version gate queries exact version'
  assert_fixture_script_contains 'render_pinned_manifest' 'vendored manifests are rewritten before apply'
  assert_fixture_script_contains 'calico\.pinned\.yaml' 'Calico applies digest-only rendered manifest'
  assert_fixture_script_contains 'envoy-gateway\.pinned\.yaml' 'Envoy Gateway applies digest-only rendered manifest'
  assert_fixture_script_contains 'assert_manifest_images_pinned' 'rendered manifests reject mutable image tags'
  assert_fixture_script_contains 'kind: BackendTLSPolicy' 'Gateway backend TLS policy is rendered'
  assert_fixture_script_contains 'verify_north_south_entitlement' 'actual north-south HTTPS response is checked'
  assert_fixture_script_contains 'wrong-\$\{backend_host\}' 'Backend TLS verification includes a wrong-SAN negative control'
  assert_fixture_script_contains '--noproxy' 'north-south localhost probe bypasses HTTPS proxies'
  assert_fixture_script_contains 'rollback_create_on_exit' 'create has a rollback trap'
  assert_fixture_script_contains 'cleanup_owned_fixture' 'destroy uses scoped ownership cleanup'
  assert_fixture_script_contains 'fixture-identity\.env' 'destroy requires a recorded fixture identity'
  assert_fixture_script_contains 'fixture-id' 'Docker and Kubernetes resources carry fixture identity labels'
  assert_fixture_script_contains 'docker image inspect.*ACR_E2E_NODE_IMAGE' 'node verification compares exact pinned image ID'
  assert_fixture_script_contains 'verify_host_container_image' 'registry verification compares exact pinned runtime image ID'
  assert_fixture_script_contains 'runtime_image_id_matches_pin' 'pod runtime image IDs resolve back to the pinned image'
  assert_fixture_script_contains 'runtime_image_id_%d' 'evidence records observed runtime image IDs'
  assert_fixture_script_contains 'acquire_fixture_lock' 'create and destroy serialize fixture lifecycle ownership'
  assert_fixture_script_contains 'fixture ownership verification failed' 'verify aborts before mutating an unowned fixture'
  assert_fixture_script_contains 'probe_kind_nodes' 'Kind teardown authenticates its complete recorded node set'
  assert_fixture_script_contains '--no-trunc' 'Kind node ownership records immutable full Docker IDs'
  assert_fixture_script_contains 'kind-created-before-record' 'partial Kind creation has a reconciled rollback path'
  assert_fixture_script_contains 'shutdown-manager' 'Envoy shutdown-manager runtime image is verified'
  assert_fixture_script_contains 'probe_docker_network' 'Docker observation errors are fail-closed'
  assert_fixture_script_lacks 'mapfile' 'north-south service discovery supports stock macOS Bash'
  assert_rendering_with_bash "${BASH}" 
  if [[ -x /bin/bash && /bin/bash != "${BASH}" ]]; then assert_rendering_with_bash /bin/bash; fi
  assert_fixture_script_lacks 'kind delete cluster --name .*[|][|] true' 'cluster deletion failures are not suppressed'
  assert_fixture_script_lacks 'docker rm -f .*[|][|] true' 'registry deletion failures are not suppressed'
  assert_fixture_script_lacks 'docker network rm .*[|][|] true' 'network deletion failures are not suppressed'
}

assert_single() {
  local name="$1"
  local net reg node fixture_id
  net="$(net_name "$name")"; reg="$(reg_name "$name")"; node="$(node_name "$name")"
  fixture_id="$(identity_value "${name}" fixture_id || true)"

  if [[ "${fixture_id}" =~ ^[a-f0-9]{32}$ ]]; then ok "fixture ${name} has a recorded opaque identity"; else bad "fixture ${name} has no valid ownership identity"; fi

  # 1. Uniquely named Docker network exists and is fixture-derived.
  if docker network inspect "$net" >/dev/null 2>&1; then ok "network ${net} exists"; else bad "network ${net} missing"; fi
  if [[ "$(docker network inspect "$net" --format "{{ index .Labels \"${FIXTURE_LABEL_KEY}\" }}" 2>/dev/null)" == "${fixture_id}" ]]; then
    ok "network ${net} label matches exact fixture identity"
  else
    bad "network ${net} is not owned by fixture identity"
  fi

  # 2. Uniquely named registry container exists and is running.
  if [[ "$(docker inspect -f '{{.State.Running}}' "$reg" 2>/dev/null)" == "true" ]]; then
    ok "registry ${reg} running"
  else
    bad "registry ${reg} not running"
  fi
  if [[ "$(docker inspect "$reg" --format "{{ index .Config.Labels \"${FIXTURE_LABEL_KEY}\" }}" 2>/dev/null)" == "${fixture_id}" ]]; then
    ok "registry ${reg} label matches exact fixture identity"
  else
    bad "registry ${reg} is not owned by fixture identity"
  fi

  # 3. Registry is attached to THIS fixture's network.
  if net_has_container "$net" "$reg"; then ok "registry ${reg} attached to ${net}"; else bad "registry ${reg} not on ${net}"; fi

  # 4. The fixture node is attached to THIS fixture's network.
  if net_has_container "$net" "$node"; then ok "node ${node} attached to ${net}"; else bad "node ${node} not on ${net}"; fi

  # 5. Node is NOT on the host-global default "kind" network (no shared bridge).
  if docker network inspect kind >/dev/null 2>&1; then
    if net_has_container kind "$node"; then bad "node ${node} leaked onto host-global 'kind' network"; else ok "node ${node} not on host-global 'kind' network"; fi
  else
    ok "no host-global 'kind' network present"
  fi

  # 6. Registry actually serves the v2 API on the fixture network (real reach).
  local out
  out="$(docker run --rm --network "$net" "$PROBE_IMG" wget -qO- "http://${reg}:5000/v2/" 2>/dev/null || true)"
  if [[ "$out" == "{}" ]]; then ok "registry ${reg} serves /v2/ on ${net}"; else bad "registry ${reg} not reachable/serving on ${net} (got: '${out}')"; fi
}

assert_pair_isolation() {
  local a="$1" b="$2"
  local aNet bNet aReg bReg
  aNet="$(net_name "$a")"; bNet="$(net_name "$b")"; aReg="$(reg_name "$a")"; bReg="$(reg_name "$b")"

  # Distinct network identities.
  local aId bId
  aId="$(docker network inspect "$aNet" -f '{{.Id}}' 2>/dev/null || echo none-a)"
  bId="$(docker network inspect "$bNet" -f '{{.Id}}' 2>/dev/null || echo none-b)"
  if [[ "$aId" != "$bId" && "$aId" != none-a && "$bId" != none-b ]]; then ok "networks ${aNet} and ${bNet} are distinct"; else bad "networks not distinct/absent"; fi

  # Cross-membership must NOT exist: A's registry off B's net and vice versa.
  if net_has_container "$bNet" "$aReg"; then bad "${aReg} leaked onto ${bNet}"; else ok "${aReg} absent from ${bNet}"; fi
  if net_has_container "$aNet" "$bReg"; then bad "${bReg} leaked onto ${aNet}"; else ok "${bReg} absent from ${aNet}"; fi

  # B's registry must NOT be resolvable/reachable from A's network (isolation).
  local xout
  xout="$(docker run --rm --network "$aNet" "$PROBE_IMG" wget -T 4 -qO- "http://${bReg}:5000/v2/" 2>/dev/null || true)"
  if [[ -z "$xout" ]]; then ok "${bReg} unreachable from ${aNet} (isolated)"; else bad "${bReg} reachable from ${aNet} (isolation breach)"; fi
}

main() {
  local sub="${1:-}"; shift || true
  local name="" a="" b=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --name) name="${2:-}"; shift 2 ;;
      --a) a="${2:-}"; shift 2 ;;
      --b) b="${2:-}"; shift 2 ;;
      *) die "unknown argument: $1" ;;
    esac
  done
  case "$sub" in
    static)
      [[ "$#" -eq 0 ]] || die "static accepts no arguments"
      assert_static_hardening ;;
    single)
      [[ -n "$name" ]] || die "single requires --name"
      assert_single "$name" ;;
    pair)
      [[ -n "$a" && -n "$b" ]] || die "pair requires --a and --b"
      assert_single "$a"; assert_single "$b"; assert_pair_isolation "$a" "$b" ;;
    *) echo "usage: $0 {static|single --name <c>|pair --a <c> --b <c>}" >&2; exit 2 ;;
  esac

  if [[ "$FAILURES" -eq 0 ]]; then printf '[selftest] PASS: fixture hardening proven\n' >&2; exit 0; fi
  printf '[selftest] FAIL: %d fixture hardening violation(s)\n' "$FAILURES" >&2; exit 1
}

main "$@"
