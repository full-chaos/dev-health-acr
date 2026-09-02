#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source_root="${CONTAINER_SOURCE_ROOT:-$repo_root}"
target="${1:?usage: build.sh <acr-api|acr-mcp>}"

case "$target" in
  acr-api|acr-mcp) ;;
  *) printf 'unsupported container target: %s\n' "$target" >&2; exit 2 ;;
esac

command -v docker >/dev/null || { printf 'docker is required\n' >&2; exit 1; }
command -v pgrep >/dev/null || { printf 'pgrep is required\n' >&2; exit 1; }

# A dirty product worktree is refused by default: an artifact built from
# uncommitted changes but labeled with the last committed HEAD's identity
# would misrepresent its own provenance. CONTAINER_ALLOW_DIRTY=1 is the
# explicit, narrow opt-in for local precommit QA; it does not silently
# pass the dirty tree off as clean -- both VERSION and COMMIT are
# suffixed "-dirty" so the resulting image is never confused for one
# built from the labeled commit alone.
if ! dirty_status="$(git -C "$source_root" status --porcelain --untracked-files=all 2>/dev/null)"; then
  printf 'cannot inspect source tree status: %s\n' "$source_root" >&2
  exit 1
fi
if ! ignored_sources="$(git -C "$source_root" ls-files --others --ignored --exclude-standard -- cmd internal migrations 2>/dev/null)"; then
  printf 'cannot inspect ignored source files: %s\n' "$source_root" >&2
  exit 1
fi
if [[ -n "$ignored_sources" ]]; then
  printf 'ignored files exist inside container source paths; remove them before building\n' >&2
  exit 1
fi
allow_dirty="${CONTAINER_ALLOW_DIRTY:-0}"
if [[ -n "$dirty_status" && "$allow_dirty" != "1" ]]; then
  printf 'refusing to build from a dirty source tree; set CONTAINER_ALLOW_DIRTY=1 for local precommit QA (labels are marked -dirty)\n' >&2
  exit 1
fi

image="${CONTAINER_IMAGE:-${target}:local}"
platforms="${CONTAINER_PLATFORMS:-}"
output="${CONTAINER_OUTPUT:-load}"
version="${CONTAINER_VERSION:-0.0.0-dev}"
commit="${CONTAINER_COMMIT:-$(git -C "$source_root" rev-parse HEAD)}"
build_date="${CONTAINER_BUILD_DATE:-$(git -C "$source_root" show -s --format=%cI HEAD)}"
source_date_epoch="${SOURCE_DATE_EPOCH:-$(git -C "$source_root" show -s --format=%ct HEAD)}"
build_cache_id="${CONTAINER_BUILD_CACHE_ID:-default}"
build_timeout="${CONTAINER_BUILD_TIMEOUT:-900}"
kill_grace="${CONTAINER_BUILD_KILL_GRACE:-5}"
[[ "$build_timeout" =~ ^[1-9][0-9]*$ ]] || { printf 'CONTAINER_BUILD_TIMEOUT must be a positive integer\n' >&2; exit 2; }
[[ "$kill_grace" =~ ^[1-9][0-9]*$ ]] || { printf 'CONTAINER_BUILD_KILL_GRACE must be a positive integer\n' >&2; exit 2; }
if [[ -n "$dirty_status" ]]; then
  version="${version}-dirty"
  commit="${commit}-dirty"
fi
if [[ "${CONTAINER_NO_CACHE:-0}" == "1" ]]; then
  build_cache_id="no-cache-${RANDOM}-${RANDOM}-$$"
fi

source_context="${CONTAINER_CONTEXT:-$source_root}"
test -d "$source_context" || { printf 'container source context is not a directory: %s\n' "$source_context" >&2; exit 1; }
context_state="$(mktemp -d)"
trap 'rm -rf "$context_state"' EXIT
"${repo_root}/scripts/container/create-context.sh" "$source_context" "$context_state"
build_context="$context_state"

wait_for_oci_archive() {
  local archive="$1"
  local deadline=$((SECONDS + 60))
  local validation_error=""

  while ((SECONDS < deadline)); do
    if [[ -s "$archive" ]]; then
      if validation_error="$("${repo_root}/scripts/container/validate-oci.sh" "$archive" 2>&1)"; then
        return 0
      fi
    fi
    sleep 1
  done

  printf 'OCI exporter did not produce a readable archive within 60 seconds: %s\n' "$archive" >&2
  if [[ -n "$validation_error" ]]; then
    printf '%s\n' "$validation_error" >&2
  fi
  return 1
}

active_build_pid=""
active_build_pgid=""
retained_pids=()
teardown_started=0

retain_pid() {
  local candidate="$1"
  local retained
  for retained in "${retained_pids[@]}"; do
    [[ "$retained" == "$candidate" ]] && return
  done
  retained_pids+=("$candidate")
}

retain_process_tree() {
  local parent="$1"
  local child
  while IFS= read -r child; do
    [[ -n "$child" ]] && retain_process_tree "$child"
  done < <(pgrep -P "$parent" 2>/dev/null || true)
  retain_pid "$parent"
}

signal_retained_pids() {
  local signal="$1"
  local retained
  for retained in "${retained_pids[@]}"; do
    kill "-${signal}" "$retained" 2>/dev/null || true
  done
}

signal_active_group() {
  local signal="$1"
  [[ -n "$active_build_pgid" ]] || return 0
  kill "-${signal}" -- "-${active_build_pgid}" 2>/dev/null || true
}

retain_live_descendants() {
  local retained
  local count="${#retained_pids[@]}"
  local index=0
  while ((index < count)); do
    retained="${retained_pids[$index]}"
    if kill -0 "$retained" 2>/dev/null; then
      retain_process_tree "$retained"
      count="${#retained_pids[@]}"
    fi
    index=$((index + 1))
  done
}

terminate_active_build() {
  [[ -n "$active_build_pid" ]] || return 0
  [[ "$teardown_started" == "0" ]] || return 0
  teardown_started=1
  trap '' INT TERM

  retain_process_tree "$active_build_pid"
  signal_active_group TERM
  signal_retained_pids TERM
  local grace_deadline=$((SECONDS + kill_grace))
  while ((SECONDS < grace_deadline)); do
    retain_live_descendants
    signal_active_group TERM
    signal_retained_pids TERM
    sleep 1
  done
  retain_live_descendants
  signal_active_group KILL
  signal_retained_pids KILL
  wait "$active_build_pid" 2>/dev/null || true
  active_build_pid=""
  active_build_pgid=""
  retained_pids=()
  teardown_started=0
}

handle_signal() {
  local status="$1"
  terminate_active_build
  exit "$status"
}

run_with_timeout() {
  local secs="$1"
  shift
  set -m
  "$@" &
  active_build_pid=$!
  active_build_pgid="$active_build_pid"
  set +m
  retained_pids=()
  teardown_started=0
  local deadline=$((SECONDS + secs))

  while kill -0 "$active_build_pid" 2>/dev/null; do
    if ((SECONDS >= deadline)); then
      terminate_active_build
      printf 'container build exceeded timeout of %s seconds\n' "$secs" >&2
      return 124
    fi
    sleep 1
  done
  local status=0
  wait "$active_build_pid" || status=$?
  active_build_pid=""
  active_build_pgid=""
  retained_pids=()
  return "$status"
}

trap 'handle_signal 130' INT
trap 'handle_signal 143' TERM

# CHAOS-4855: empty by default, so a local build still pulls the golang base
# image straight from Docker Hub unchanged; CI sets this to ghcr.io/full-chaos/
# so the one Docker Hub base image in Dockerfile resolves against the mirror
# instead. See docs/container-images.md.
args=(
  docker buildx build
  --file "${build_context}/Dockerfile"
  --target "$target"
  --tag "$image"
  --build-arg "VERSION=${version}"
  --build-arg "COMMIT=${commit}"
  --build-arg "BUILD_DATE=${build_date}"
  --build-arg "SOURCE_DATE_EPOCH=${source_date_epoch}"
  --build-arg "BUILD_CACHE_ID=${build_cache_id}"
  --build-arg "ACR_IMAGE_MIRROR_PREFIX=${ACR_IMAGE_MIRROR_PREFIX:-}"
  --provenance=false
  --sbom=false
)

# CHAOS-4855 R4 (codex round 2, executed): the `# syntax=` parser directive
# at the top of the Dockerfile is a SEPARATE Docker Hub pull from the `FROM`
# lines -- BuildKit fetches it before Dockerfile parsing even starts, and
# ARG substitution does not apply inside that comment (it must be a literal
# string), so ACR_IMAGE_MIRROR_PREFIX above cannot redirect it the way it
# redirects `FROM`. The `BUILDKIT_SYNTAX` build-arg is buildx's own
# documented override for exactly this: it takes precedence over the
# `# syntax=` comment. Read the digest from the Dockerfile itself (never
# restated as a bare string here) so the two can never drift; empty prefix
# reproduces the exact ref the comment already declares, so this is safe to
# always pass.
syntax_ref="$(grep -oE '^# syntax=(.+)$' "${source_root}/Dockerfile" | sed -E 's/^# syntax=//')"
[[ -n "$syntax_ref" ]] || { printf 'could not resolve the "# syntax=" directive from Dockerfile\n' >&2; exit 1; }
args+=(--build-arg "BUILDKIT_SYNTAX=${ACR_IMAGE_MIRROR_PREFIX:-}${syntax_ref}")

if [[ -n "$platforms" ]]; then
  args+=(--platform "$platforms")
fi
if [[ "${CONTAINER_NO_CACHE:-0}" == "1" ]]; then
  args+=(--no-cache)
fi

# OCI archives are exported to a unique, invocation-owned temp path and
# only renamed into place after wait_for_oci_archive confirms the
# exporter produced a complete, readable archive: a reader can never
# observe a partially-written file at the final destination, and a
# process killed mid-export (e.g. by run_with_timeout) never leaves
# truncated bytes at $oci_output.
oci_output=""
oci_temp=""
case "$output" in
  load)
    [[ "$platforms" != *,* ]] || { printf 'cannot load a multi-platform image\n' >&2; exit 2; }
    args+=(--load)
    ;;
  oci)
    oci_output="${CONTAINER_OCI_OUTPUT:?CONTAINER_OCI_OUTPUT is required for OCI output}"
    mkdir -p "$(dirname "$oci_output")"
    oci_temp="$(mktemp "${oci_output}.tmp.XXXXXX")"
    args+=(--output "type=oci,dest=${oci_temp}")
    ;;
  *)
    printf 'unsupported container output: %s\n' "$output" >&2
    exit 2
    ;;
esac

args+=("$build_context")

cleanup_oci_temp() {
  terminate_active_build
  if [[ -n "$oci_temp" && -e "$oci_temp" ]]; then
    rm -f "$oci_temp"
  fi
  rm -rf "$context_state"
}
trap cleanup_oci_temp EXIT

run_with_timeout "$build_timeout" "${args[@]}"

if [[ "$output" == oci ]]; then
  wait_for_oci_archive "$oci_temp"
  mv "$oci_temp" "$oci_output"
  oci_temp=""
fi
printf 'built target=%s image=%s platforms=%s output=%s\n' "$target" "$image" "${platforms:-native}" "$output"
