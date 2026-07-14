#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
first_suffix="repro-first-$$"
second_suffix="repro-second-$$"
images=("acr-api:${first_suffix}" "acr-mcp:${first_suffix}" "acr-api:${second_suffix}" "acr-mcp:${second_suffix}")
tmp_dir="$(mktemp -d)"
snapshot_dir="${tmp_dir}/source"

# created_containers tracks every `docker create` result the moment it
# succeeds, before the `docker cp` that could fail -- so a failed copy
# path still gets its container removed by the EXIT trap, instead of
# leaking a container that only the happy path's own `docker rm` cleaned
# up.
created_containers=()

cleanup() {
  local container
  if ((${#created_containers[@]})); then
    for container in "${created_containers[@]}"; do
      docker rm -f "$container" >/dev/null 2>&1 || true
    done
  fi
  docker image rm -f "${images[@]}" >/dev/null 2>&1 || true
  rm -rf "$tmp_dir"
}
trap cleanup EXIT
digest() { docker image inspect "$1" | jq -r '.[0].RootFS.Layers[-1]'; }
binary_hash() {
  local image="$1"
  local binary="$2"
  local container
  container="$(docker create "$image")"
  created_containers+=("$container")
  docker cp "${container}:${binary}" "$tmp_dir/${container}" >/dev/null
  docker rm "$container" >/dev/null
  shasum -a 256 "$tmp_dir/${container}" | awk '{print $1}'
}
build() {
  local image="$1"
  local target="$2"
  CONTAINER_CONTEXT="$snapshot_dir" CONTAINER_SOURCE_ROOT="$repo_root" \
    CONTAINER_IMAGE="$image" CONTAINER_NO_CACHE=1 "${repo_root}/scripts/container/build.sh" "$target"
}

test -z "$(git -C "$repo_root" status --porcelain)" || {
  printf 'reproducibility requires a clean committed product worktree\n' >&2
  exit 1
}
commit="$(git -C "$repo_root" rev-parse HEAD)"
mkdir -p "$snapshot_dir"
git -C "$repo_root" archive "$commit" | tar -x -C "$snapshot_dir"

build "${images[0]}" acr-api
build "${images[1]}" acr-mcp
build "${images[2]}" acr-api
build "${images[3]}" acr-mcp

test "$(digest "${images[0]}")" = "$(digest "${images[2]}")"
test "$(digest "${images[1]}")" = "$(digest "${images[3]}")"
test "$(binary_hash "${images[0]}" /usr/local/bin/acr-api)" = "$(binary_hash "${images[2]}" /usr/local/bin/acr-api)"
test "$(binary_hash "${images[1]}" /usr/local/bin/acr-mcp)" = "$(binary_hash "${images[3]}" /usr/local/bin/acr-mcp)"

printf 'application layers and binaries are reproducible\n'
