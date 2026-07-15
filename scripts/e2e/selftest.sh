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
#   single --name <cluster>        assert one fixture's registry/network isolation
#   pair   --a <clusterA> --b <clusterB>   assert both, plus mutual isolation
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/pins.env"

PROBE_IMG="${ACR_E2E_IMG_PROBE:-docker.io/library/busybox@sha256:9532d8c39891ca2ecde4d30d7710e01fb739c87a8b9299685c63704296b16028}"

FAILURES=0
ok()   { printf '[selftest] ok: %s\n' "$*" >&2; }
bad()  { printf '[selftest] FAIL: %s\n' "$*" >&2; FAILURES=$((FAILURES+1)); }
die()  { printf '[selftest] FATAL: %s\n' "$*" >&2; exit 2; }

net_name()  { echo "$1-net"; }
reg_name()  { echo "$1-registry"; }
node_name() { echo "$1-control-plane"; }

# Is docker object $2 (a container name) attached to docker network $1?
net_has_container() {
  docker network inspect "$1" --format '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null | tr ' ' '\n' | grep -qx "$2"
}

assert_single() {
  local name="$1"
  local net reg node
  net="$(net_name "$name")"; reg="$(reg_name "$name")"; node="$(node_name "$name")"

  # 1. Uniquely named Docker network exists and is fixture-derived.
  if docker network inspect "$net" >/dev/null 2>&1; then ok "network ${net} exists"; else bad "network ${net} missing"; fi

  # 2. Uniquely named registry container exists and is running.
  if [[ "$(docker inspect -f '{{.State.Running}}' "$reg" 2>/dev/null)" == "true" ]]; then
    ok "registry ${reg} running"
  else
    bad "registry ${reg} not running"
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
    single)
      [[ -n "$name" ]] || die "single requires --name"
      assert_single "$name" ;;
    pair)
      [[ -n "$a" && -n "$b" ]] || die "pair requires --a and --b"
      assert_single "$a"; assert_single "$b"; assert_pair_isolation "$a" "$b" ;;
    *) echo "usage: $0 {single --name <c>|pair --a <c> --b <c>}" >&2; exit 2 ;;
  esac

  if [[ "$FAILURES" -eq 0 ]]; then printf '[selftest] PASS: isolation proven\n' >&2; exit 0; fi
  printf '[selftest] FAIL: %d isolation violation(s)\n' "$FAILURES" >&2; exit 1
}

main "$@"
