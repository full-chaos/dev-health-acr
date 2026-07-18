#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
mode=""
compose_file=""
overlay_file=""
cluster="${ACR_KIND_CLUSTER:-}"
kustomize_cluster="${ACR_KUSTOMIZE_CLUSTER:-}"

usage() {
  cat >&2 <<'EOF'
usage: clean-room.sh --mode compose|helm|kustomize|mcp [options]

compose options: --compose <root-compose.yml> --overlay <acr.compose.yml>
helm options: --cluster <kind-cluster>
kustomize options: --cluster <kind-cluster>
EOF
}

skip() {
  printf 'SKIP: %s\n' "$*"
  exit 0
}

require_commands() {
  local command_name
  for command_name in "$@"; do
    command -v "$command_name" >/dev/null 2>&1 || skip "required infrastructure command is unavailable: $command_name"
  done
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      mode="$2"
      shift 2
      ;;
    --compose)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      compose_file="$2"
      shift 2
      ;;
    --overlay)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      overlay_file="$2"
      shift 2
      ;;
    --cluster)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      cluster="$2"
      kustomize_cluster="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'error: unknown argument %s\n' "$1" >&2
      usage
      exit 2
      ;;
  esac
done

case "$mode" in
  compose)
    require_commands docker openssl curl git jq
    docker info >/dev/null 2>&1 || skip 'Docker daemon is unavailable; Compose clean-room was not run'
    docker compose version >/dev/null 2>&1 || skip 'Docker Compose plugin is unavailable; Compose clean-room was not run'
    [ -n "$compose_file" ] && [ -n "$overlay_file" ] || { usage; exit 2; }
    [ -f "$compose_file" ] || { printf 'error: compose file is not a file: %s\n' "$compose_file" >&2; exit 2; }
    [ -f "$overlay_file" ] || { printf 'error: overlay file is not a file: %s\n' "$overlay_file" >&2; exit 2; }
    project="acr-e2e-docs-${RANDOM}-${RANDOM}"
    exec bash "$repo_root/scripts/e2e/compose.sh" \
      --compose "$compose_file" \
      --overlay "$overlay_file" \
      --project "$project" \
      --scenario happy
    ;;
  helm)
    [ -n "$cluster" ] || { printf 'error: --cluster or ACR_KIND_CLUSTER is required\n' >&2; exit 2; }
    require_commands docker kind kubectl helm openssl htpasswd curl git jq
    docker info >/dev/null 2>&1 || skip 'Docker daemon is unavailable; Helm clean-room was not run'
    kind get clusters | grep -Fx "$cluster" >/dev/null || skip "Kind cluster is unavailable: $cluster"
    bash "$repo_root/scripts/e2e/kind-fixture.sh" verify --name "$cluster"
    exec bash "$repo_root/scripts/e2e/kind-helm.sh" --cluster "$cluster" --scenario lifecycle
    ;;
  kustomize)
    [ -n "$kustomize_cluster" ] || { printf 'error: --cluster or ACR_KUSTOMIZE_CLUSTER is required\n' >&2; exit 2; }
    require_commands docker kind kubectl helm openssl htpasswd curl git jq
    docker info >/dev/null 2>&1 || skip 'Docker daemon is unavailable; Kustomize clean-room was not run'
    kind get clusters | grep -Fx "$kustomize_cluster" >/dev/null || skip "Kind cluster is unavailable: $kustomize_cluster"
    bash "$repo_root/scripts/e2e/kind-fixture.sh" verify --name "$kustomize_cluster"
    exec bash "$repo_root/scripts/e2e/kind-kustomize.sh" --cluster "$kustomize_cluster" --scenario lifecycle
    ;;
  mcp)
    require_commands go
    (
      cd "$repo_root"
      go run ./cmd/acr-mcp doctor --offline
      go run ./cmd/acr-mcp metadata
      go test ./internal/mcp -run '^TestCommandTransportE2EBothToolsAgainstLiveTLSFixture$' -count=1
    )
    ;;
  *)
    usage
    exit 2
    ;;
esac
