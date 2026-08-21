#!/usr/bin/env bash
# kiac-provisioned local Kubernetes interface for ACR (CHAOS-4055).
#
# Stands up a single-node (by default) k3s cluster where every node is its own
# lightweight VM on Apple's open-source `container` runtime -- entirely outside
# any Docker daemon/VM, so it never competes with Docker-hosted compose stacks
# or Testcontainers suites. Additive: the existing kind/k3d paths
# (scripts/e2e/kind-*.sh) are untouched and remain the portable fallback.
#
# Pinned tool matrix (fail-closed; see CHAOS-4051 evaluation):
#   kiac            v0.5.1
#   apple/container 1.2.2
#
# Kubeconfig isolation: the cluster's kubeconfig is written to an isolated
# path under .tmp/ (never the user's ~/.kube/config). Every kubectl/helm
# invocation against this cluster must set KUBECONFIG to `kiac.sh kubeconfig`.
#
# Image path (no registry, no Docker):
#   container build -t <tag> ...      (apple/container local image store)
#   kiac.sh load-image <tag>          (-> containerd on every node)
#   pod imagePullPolicy: Never
#
# Usage:
#   deploy/local/kiac.sh doctor
#   deploy/local/kiac.sh up
#   deploy/local/kiac.sh kubeconfig
#   deploy/local/kiac.sh build-image <tag> <context> [dockerfile]
#   deploy/local/kiac.sh load-image <image> [image...]
#   deploy/local/kiac.sh down
#
# Environment:
#   ACR_KIAC_CLUSTER_NAME        cluster name (default: acr-local)
#   ACR_KIAC_WORKERS             worker node count (default: 0; control plane untainted)
#   ACR_KIAC_KUBECONFIG          kubeconfig path override
#   ACR_KIAC_ALLOW_VERSION_DRIFT set to 1 to downgrade the version-pin failure to a warning
set -euo pipefail

PINNED_KIAC="v0.5.1"
PINNED_CONTAINER="1.2.2"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CLUSTER_NAME="${ACR_KIAC_CLUSTER_NAME:-acr-local}"
STATE_DIR="$REPO_ROOT/.tmp/kiac/$CLUSTER_NAME"
KUBECONFIG_PATH="${ACR_KIAC_KUBECONFIG:-$STATE_DIR/kubeconfig}"
WORKERS="${ACR_KIAC_WORKERS:-0}"

log() { printf '[kiac.sh] %s\n' "$*" >&2; }
die() { printf '[kiac.sh] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  sed -n '2,35p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 2
}

require_bin() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required on PATH$2"
}

check_versions() {
  local out kiac_v container_v drift=""
  out="$(kiac version 2>/dev/null)" || die "kiac version failed; is kiac installed correctly?"
  kiac_v="$(awk '/^kiac /{print $2}' <<<"$out")"
  container_v="$(awk '/^apple\/container /{print $2}' <<<"$out")"
  [[ "$kiac_v" == "$PINNED_KIAC" ]] || drift+="kiac $kiac_v (pinned $PINNED_KIAC); "
  [[ "$container_v" == "$PINNED_CONTAINER" ]] || drift+="apple/container $container_v (pinned $PINNED_CONTAINER); "
  if [[ -n "$drift" ]]; then
    if [[ "${ACR_KIAC_ALLOW_VERSION_DRIFT:-0}" == "1" ]]; then
      log "WARNING: version drift from tested pins: $drift(continuing: ACR_KIAC_ALLOW_VERSION_DRIFT=1)"
    else
      die "version drift from tested pins: $drift set ACR_KIAC_ALLOW_VERSION_DRIFT=1 to continue anyway"
    fi
  fi
}

cluster_exists() {
  kiac get clusters 2>/dev/null | awk 'NR>1 {print $1}' | grep -qx "$CLUSTER_NAME"
}

cmd_doctor() {
  require_bin kiac " (pinned $PINNED_KIAC)"
  require_bin container " (apple/container, pinned $PINNED_CONTAINER)"
  require_bin kubectl ""
  check_versions
  # kiac issue #35: on a fresh apple/container install, `kiac doctor --fix`
  # can report success while the runtime still needs its one-time interactive
  # kernel initialization. Require the container service to already be
  # running so cluster creation never enters a partial, prompt-blocked state.
  if ! container system status >/dev/null 2>&1; then
    die "apple/container service is not running. Run 'container system start' once (interactively, it may install a kernel) and re-run doctor."
  fi
  kiac doctor || die "kiac doctor reported problems"
  log "doctor OK: kiac $PINNED_KIAC on apple/container $PINNED_CONTAINER, service running"
}

cmd_up() {
  cmd_doctor
  if cluster_exists; then
    die "cluster '$CLUSTER_NAME' already exists; refusing to reuse or mutate it (delete it with 'kiac.sh down' first)"
  fi
  mkdir -p "$STATE_DIR"
  log "creating k3s cluster '$CLUSTER_NAME' (workers=$WORKERS, kubeconfig=$KUBECONFIG_PATH)"
  KUBECONFIG="$KUBECONFIG_PATH" kiac create cluster \
    --name "$CLUSTER_NAME" \
    --distro k3s \
    --workers "$WORKERS" \
    --wait 5m
  [[ -s "$KUBECONFIG_PATH" ]] || die "kiac did not write the isolated kubeconfig at $KUBECONFIG_PATH"
  KUBECONFIG="$KUBECONFIG_PATH" kubectl wait --for=condition=Ready nodes --all --timeout=180s
  log "cluster '$CLUSTER_NAME' is up; export KUBECONFIG=$KUBECONFIG_PATH"
}

cmd_down() {
  if ! cluster_exists; then
    log "cluster '$CLUSTER_NAME' does not exist; nothing to delete"
  else
    kiac delete cluster --name "$CLUSTER_NAME"
  fi
  rm -f "$KUBECONFIG_PATH"
  log "cluster '$CLUSTER_NAME' deleted and isolated kubeconfig removed"
}

cmd_kubeconfig() {
  printf '%s\n' "$KUBECONFIG_PATH"
}

cmd_build_image() {
  [[ $# -ge 2 ]] || die "usage: kiac.sh build-image <tag> <context> [dockerfile]"
  local tag="$1" context="$2" dockerfile="${3:-}"
  require_bin container ""
  if [[ -n "$dockerfile" ]]; then
    container build -t "$tag" -f "$dockerfile" "$context"
  else
    container build -t "$tag" "$context"
  fi
  log "built $tag into the apple/container image store"
}

cmd_load_image() {
  [[ $# -ge 1 ]] || die "usage: kiac.sh load-image <image> [image...]"
  cluster_exists || die "cluster '$CLUSTER_NAME' does not exist; run 'kiac.sh up' first"
  kiac load image --name "$CLUSTER_NAME" "$@"
  log "loaded into every '$CLUSTER_NAME' node: $*"
}

case "${1:-}" in
  doctor) shift; cmd_doctor "$@" ;;
  up) shift; cmd_up "$@" ;;
  down) shift; cmd_down "$@" ;;
  kubeconfig) shift; cmd_kubeconfig "$@" ;;
  build-image) shift; cmd_build_image "$@" ;;
  load-image) shift; cmd_load_image "$@" ;;
  -h|--help|help|"") usage ;;
  *) die "unknown command: $1 (try --help)" ;;
esac
