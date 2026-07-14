#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
sentinel="ACR_CONTAINER_SECRET_SENTINEL_9b4f4fe1"
invocation_id="$$-${RANDOM}"
tmp_dir="$(mktemp -d)"
snapshot="${tmp_dir}/source"
worktree_patch="${tmp_dir}/worktree.patch"
untracked_files="${tmp_dir}/untracked-files"
api_image="acr-api:smoke-${invocation_id}"
mcp_image="acr-mcp:smoke-${invocation_id}"
prepared_context="${tmp_dir}/context"
raw_build_log="${tmp_dir}/raw-build.log"
ignored_build_log="${tmp_dir}/ignored-build.log"

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

cleanup() {
  rm -rf "$tmp_dir"
  docker image rm -f "$api_image" "$mcp_image" >/dev/null 2>&1 || true
}
trap cleanup EXIT

git clone --quiet --no-hardlinks "$repo_root" "$snapshot"
git -C "$repo_root" diff --binary HEAD -- >"$worktree_patch"
if [[ -s "$worktree_patch" ]]; then
  git -C "$snapshot" apply --binary "$worktree_patch"
fi
git -C "$repo_root" ls-files --others --exclude-standard -z >"$untracked_files"
while IFS= read -r -d '' relative; do
  mkdir -p "${snapshot}/$(dirname "$relative")"
  cp -Pp "${repo_root}/${relative}" "${snapshot}/${relative}"
done <"$untracked_files"

unmatched_file_one="$(mktemp "${snapshot}/unmatched-${invocation_id}-XXXXXX")"
unmatched_file_two="$(mktemp "${snapshot}/unmatched-${invocation_id}-XXXXXX")"
ignored_nested_file="$(mktemp "${snapshot}/internal/.env.container-proof-${invocation_id}-XXXXXX")"
printf '%s\n' "$sentinel" >"$unmatched_file_one"
printf '%s\n' "$sentinel" >"$unmatched_file_two"
printf '%s\n' "$sentinel" >"$ignored_nested_file"

"${repo_root}/scripts/container/create-context.sh" "$snapshot" "$prepared_context"
for fixture in "$unmatched_file_one" "$unmatched_file_two" "$ignored_nested_file"; do
  relative_fixture="${fixture#"${snapshot}/"}"
  [[ ! -e "${prepared_context}/${relative_fixture}" ]] || fail "sentinel reached prepared BuildKit context: ${relative_fixture}"
done
for required_source in Dockerfile go.mod go.sum cmd/acr-api/main.go internal/api/app.go; do
  [[ -f "${prepared_context}/${required_source}" ]] || fail "prepared context omitted required source: ${required_source}"
done
if grep -aFRq "$sentinel" "$prepared_context"; then
  fail 'sentinel reached prepared BuildKit context content'
fi

set +e
docker buildx build --target build --load "$snapshot" >"$raw_build_log" 2>&1
raw_build_status=$?
set -e
[[ "$raw_build_status" -ne 0 ]] || fail 'raw repository build unexpectedly received product sources'
grep -Fq "$sentinel" "$raw_build_log" && fail 'sentinel reached raw BuildKit context'

set +e
CONTAINER_SOURCE_ROOT="$snapshot" CONTAINER_CONTEXT="$snapshot" CONTAINER_ALLOW_DIRTY=1 \
  "${repo_root}/scripts/container/build.sh" acr-api >"$ignored_build_log" 2>&1
ignored_build_status=$?
set -e
[[ "$ignored_build_status" -ne 0 ]] || fail 'container build accepted an ignored source-tree file'
grep -Fq 'ignored files exist inside container source paths' "$ignored_build_log" || fail 'ignored source rejection did not report its cause'

rm -f "$unmatched_file_one" "$unmatched_file_two" "$ignored_nested_file"
CONTAINER_SOURCE_ROOT="$snapshot" CONTAINER_CONTEXT="$snapshot" CONTAINER_ALLOW_DIRTY=1 \
  CONTAINER_IMAGE="$api_image" "${repo_root}/scripts/container/build.sh" acr-api
CONTAINER_SOURCE_ROOT="$snapshot" CONTAINER_CONTEXT="$snapshot" CONTAINER_ALLOW_DIRTY=1 \
  CONTAINER_IMAGE="$mcp_image" "${repo_root}/scripts/container/build.sh" acr-mcp
ACR_CONTAINER_SECRET_SENTINEL="$sentinel" "${repo_root}/scripts/container/verify.sh" "$api_image" "$mcp_image"
