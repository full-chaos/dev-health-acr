#!/usr/bin/env bash
# Per-shard Postgres+FalkorDB pair driver (CHAOS-4055; CHAOS-4033 consumer).
#
# Renders deploy/local/templates/shard.yaml for one shard index and drives its
# lifecycle against whatever cluster KUBECONFIG points at (normally a kiac k3s
# cluster from deploy/local/kiac.sh; any conformant cluster with reachable
# NodePorts works).
#
# Contract (stable; the harness may rely on it):
#   namespace        acr-shard-<i>            teardown = delete this namespace
#   postgres         NodePort 31000 + 2*i     user acr, db acr, password below
#   falkordb         NodePort 31001 + 2*i     no auth
#   readiness        pg_isready / GRAPH.QUERY probes gate rollout; 'wait'
#                    returns only when both Deployments are fully rolled out
#   DSN              postgres://acr:<password>@<node-ip>:<pg-port>/acr?sslmode=disable
#   falkor addr      <node-ip>:<falkor-port>
#
# Usage:
#   deploy/local/shard.sh render <i>
#   deploy/local/shard.sh apply <i>
#   deploy/local/shard.sh wait <i>
#   deploy/local/shard.sh dsn <i>
#   deploy/local/shard.sh delete <i>
#
# Environment:
#   ACR_SHARD_PG_PASSWORD  postgres password (default: acr-local-dev; DEV ONLY;
#                          restricted to [A-Za-z0-9._~-] -- it is substituted
#                          into YAML and the DSN verbatim)
#   KUBECONFIG             cluster to operate on (required for everything but
#                          render; the ambient default cluster is refused)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="$SCRIPT_DIR/templates/shard.yaml"
PG_PASSWORD="${ACR_SHARD_PG_PASSWORD:-acr-local-dev}"

log() { printf '[shard.sh] %s\n' "$*" >&2; }
die() { printf '[shard.sh] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,27p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 2
}

shard_vars() {
  local i="$1"
  # Strict decimal (no leading zeros -- bash arithmetic would read them as
  # octal; bounded length -- unbounded digits overflow 64-bit arithmetic and
  # would bypass the NodePort budget check below).
  [[ "$i" =~ ^(0|[1-9][0-9]{0,2})$ ]] || die "shard index must be a plain decimal 0-883, got: $i"
  # NodePort range is 30000-32767; 31000 + 2*i stays inside it for i <= 883.
  (( i <= 883 )) || die "shard index $i exceeds the NodePort budget (max 883)"
  # The password lands in a sed replacement, a YAML scalar, and a PostgreSQL
  # URL. Restrict it to characters that are inert in all three rather than
  # attempting three different escapings (dev-only credential; fail closed).
  [[ "$PG_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ ]] \
    || die "ACR_SHARD_PG_PASSWORD may only contain [A-Za-z0-9._~-] (it is substituted into YAML and a DSN verbatim)"
  NAMESPACE="acr-shard-$i"
  PG_NODEPORT=$(( 31000 + 2 * i ))
  FALKOR_NODEPORT=$(( 31001 + 2 * i ))
}

require_kubeconfig() {
  # Refuse to operate on whatever ambient cluster the user's default
  # kubeconfig points at: every mutating/reading command requires an explicit
  # KUBECONFIG (normally the isolated path from kiac.sh kubeconfig).
  [[ -n "${KUBECONFIG:-}" && -f "$KUBECONFIG" ]] \
    || die "KUBECONFIG must be set to an existing file (e.g. \$(deploy/local/kiac.sh kubeconfig)); refusing to use the ambient default cluster"
}

render() {
  sed \
    -e "s|__NAMESPACE__|$NAMESPACE|g" \
    -e "s|__SHARD_INDEX__|$1|g" \
    -e "s|__PG_NODEPORT__|$PG_NODEPORT|g" \
    -e "s|__FALKOR_NODEPORT__|$FALKOR_NODEPORT|g" \
    -e "s|__PG_PASSWORD__|$PG_PASSWORD|g" \
    "$TEMPLATE"
}

node_ip() {
  kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}'
}

cmd_render() { render "$1"; }

cmd_apply() {
  require_kubeconfig
  render "$1" | kubectl apply -f -
  log "shard $1 applied to namespace $NAMESPACE"
}

cmd_wait() {
  require_kubeconfig
  kubectl -n "$NAMESPACE" rollout status deployment/shard-postgres --timeout=180s
  kubectl -n "$NAMESPACE" rollout status deployment/shard-falkordb --timeout=180s
  log "shard $1 ready: postgres and falkordb rolled out in $NAMESPACE"
}

cmd_dsn() {
  require_kubeconfig
  local ip
  ip="$(node_ip)"
  [[ -n "$ip" ]] || die "could not resolve a node InternalIP from KUBECONFIG"
  printf 'ACR_SHARD_%s_POSTGRES_DSN=postgres://acr:%s@%s:%d/acr?sslmode=disable\n' "$1" "$PG_PASSWORD" "$ip" "$PG_NODEPORT"
  printf 'ACR_SHARD_%s_FALKOR_ADDR=%s:%d\n' "$1" "$ip" "$FALKOR_NODEPORT"
}

cmd_delete() {
  require_kubeconfig
  kubectl delete namespace "$NAMESPACE" --ignore-not-found --wait=true --timeout=120s
  log "shard $1 torn down (namespace $NAMESPACE deleted)"
}

[[ $# -ge 1 ]] || usage
cmd="$1"; shift || true
case "$cmd" in
  -h|--help|help) usage ;;
esac
[[ $# -ge 1 ]] || die "missing shard index (usage: shard.sh $cmd <i>)"
shard_vars "$1"
case "$cmd" in
  render) cmd_render "$1" ;;
  apply) cmd_apply "$1" ;;
  wait) cmd_wait "$1" ;;
  dsn) cmd_dsn "$1" ;;
  delete) cmd_delete "$1" ;;
  *) die "unknown command: $cmd (try --help)" ;;
esac
