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

# CHAOS-6403: both targets share ONE `build` stage (all four Go binaries, both
# platforms). Building each with --no-cache compiled that stage twice, ~4 min
# each on a 2-vCPU runner. The first build stays --no-cache (nothing stale can
# reach the archives); the second reuses the first's layers under the SAME
# BUILD_CACHE_ID, which is part of every RUN's cache key, so it can only hit
# what this invocation just built. The id is random per invocation, so no
# earlier run's cache can be reused either.
shared_cache_id="oci-${RANDOM}-${RANDOM}-$$"
CONTAINER_NO_CACHE=1 CONTAINER_BUILD_CACHE_ID="$shared_cache_id" CONTAINER_OUTPUT=oci CONTAINER_PLATFORMS=linux/amd64,linux/arm64 \
  CONTAINER_OCI_OUTPUT="${work_root}/acr-api.tar" "${repo_root}/scripts/container/build.sh" acr-api
CONTAINER_BUILD_CACHE_ID="$shared_cache_id" CONTAINER_OUTPUT=oci CONTAINER_PLATFORMS=linux/amd64,linux/arm64 \
  CONTAINER_OCI_OUTPUT="${work_root}/acr-mcp.tar" "${repo_root}/scripts/container/build.sh" acr-mcp
bash "${repo_root}/scripts/container/verify-oci.sh" "${work_root}/acr-api.tar" "${work_root}/acr-mcp.tar"
bash "${repo_root}/scripts/container/publish-directory.sh" "$stable_root" "$work_root" "$lock_timeout"
work_root=""
