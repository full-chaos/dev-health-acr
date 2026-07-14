#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp_root="${repo_root}/.tmp"
stable_root="${tmp_root}/container-oci"
lock_timeout="${CONTAINER_PUBLISH_LOCK_TIMEOUT:-60}"
work_root=""

[[ "$lock_timeout" =~ ^[1-9][0-9]*$ ]] || { printf 'CONTAINER_PUBLISH_LOCK_TIMEOUT must be a positive integer\n' >&2; exit 2; }
mkdir -p "$tmp_root"
work_root="$(mktemp -d "${tmp_root}/container-oci.work.XXXXXX")"

cleanup() {
  if [[ -n "$work_root" ]]; then
    rm -rf "$work_root"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

CONTAINER_NO_CACHE=1 CONTAINER_OUTPUT=oci CONTAINER_PLATFORMS=linux/amd64,linux/arm64 \
  CONTAINER_OCI_OUTPUT="${work_root}/acr-api.tar" "${repo_root}/scripts/container/build.sh" acr-api
CONTAINER_NO_CACHE=1 CONTAINER_OUTPUT=oci CONTAINER_PLATFORMS=linux/amd64,linux/arm64 \
  CONTAINER_OCI_OUTPUT="${work_root}/acr-mcp.tar" "${repo_root}/scripts/container/build.sh" acr-mcp
bash "${repo_root}/scripts/container/verify-oci.sh" "${work_root}/acr-api.tar" "${work_root}/acr-mcp.tar"
bash "${repo_root}/scripts/container/publish-directory.sh" "$stable_root" "$work_root" "$lock_timeout"
work_root=""
