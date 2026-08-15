#!/usr/bin/env bash
set -euo pipefail

api_image="${1:?usage: verify.sh <api-image> <mcp-image>}"
mcp_image="${2:?usage: verify.sh <api-image> <mcp-image>}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
sentinel="${ACR_CONTAINER_SECRET_SENTINEL:-ACR_CONTAINER_SECRET_SENTINEL_9b4f4fe1}"
# Must stay on the version ACR actually ships against -- the same digest the
# compose stack and the Helm chart's bundled PostgreSQL use. A harness pinned to
# an older major verifies migrations no deployment ever runs.
postgres_image='docker.io/library/postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44'
tmp_dir="$(mktemp -d)"
git_workspace=""
migration_network=""
# created_containers tracks every `docker create`/`docker run -d` result
# the moment it succeeds, before any later step (export, cp, health
# check) that could fail -- so a failed export/cp/probe path still gets
# its container removed by the EXIT trap below, instead of leaking a
# container that only the happy path ever cleaned up.
created_containers=()
track_container() { created_containers+=("$1"); }

cleanup() {
  local container
  if ((${#created_containers[@]})); then
    for container in "${created_containers[@]}"; do
      docker rm -f "$container" >/dev/null 2>&1 || true
    done
  fi
  if [[ -n "$migration_network" ]]; then
    docker network rm "$migration_network" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp_dir" "$git_workspace"
}
trap cleanup EXIT

require() { command -v "$1" >/dev/null || { printf '%s is required\n' "$1" >&2; exit 1; }; }
require curl
require cmp
require docker
require git
require jq
require tar

# Applied to every probe container: dropping all Linux capabilities and
# blocking privilege escalation (setuid/setgid/file-capability gain)
# narrows each run to no more than a read-only-root, non-root process
# already needs, even though every probe already runs as numeric UID
# 65532 with no bind-mounted write target.
readonly_probe_flags=(--read-only --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges)

assert_config() {
  local image="$1"
  local entrypoint="$2"
  docker image inspect "$image" | jq -e --arg entrypoint "$entrypoint" '
    .[0].Config.User == "65532:65532" and
    .[0].Config.Entrypoint == [$entrypoint] and
    ([.[0].Config.Env[] | select(test("(TOKEN|PASSWORD|SECRET|DSN)="; "i"))] | length == 0)
  ' >/dev/null
}

assert_config "$api_image" /usr/local/bin/acr-api
assert_config "$mcp_image" /usr/local/bin/acr-mcp

export_image() {
  local image="$1"
  local dest="$2"
  local probe
  probe="$(docker create "$image")"
  track_container "$probe"
  docker export "$probe" >"$dest"
}

api_export="${tmp_dir}/api.tar"
mcp_export="${tmp_dir}/mcp.tar"
export_image "$api_image" "$api_export"
export_image "$mcp_image" "$mcp_export"

package_manager_pattern='(^|/)(apk|apt|apt-get|dpkg|dpkg-deb|rpm|yum|dnf|microdnf)$'
shell_pattern='(^|/)(sh|bash|dash|ash|zsh|ksh|csh|tcsh|fish|busybox)$'

# tar_entries drains a full `tar -tf` listing to a temp file rather than
# piping it into `grep -q`: under `set -o pipefail`, a `grep -q` match
# closes its stdin early, killing the still-writing tar with SIGPIPE,
# which pipefail then reports as the pipeline's own nonzero status --
# masking a real positive match (a package manager or shell IS present)
# as a false negative.
tar_entries() {
  local archive="$1"
  local dest
  dest="$(mktemp "${tmp_dir}/entries.XXXXXX")"
  tar -tf "$archive" >"$dest"
  printf '%s\n' "$dest"
}

assert_no_package_manager() {
  local export_tar="$1"
  local label="$2"
  local listing
  listing="$(tar_entries "$export_tar")"
  if grep -Eq "$package_manager_pattern" "$listing"; then
    printf '%s runtime contains a package manager\n' "$label" >&2
    exit 1
  fi
}

assert_no_shell() {
  local export_tar="$1"
  local label="$2"
  local listing
  listing="$(tar_entries "$export_tar")"
  if grep -Eq "$shell_pattern" "$listing"; then
    printf '%s runtime contains a shell\n' "$label" >&2
    exit 1
  fi
}

assert_no_package_manager "$api_export" API
assert_no_shell "$api_export" API
assert_no_package_manager "$mcp_export" MCP
assert_no_shell "$mcp_export" MCP

# Sentinel content scans extract the full export to a temp file rather
# than piping into `grep -aFq`, for the same SIGPIPE/pipefail reason as
# tar_entries above, and additionally because a bash command
# substitution would silently truncate the captured content at the
# first embedded NUL byte in either binary's raw bytes, which could turn
# a real leak later in the stream into a false negative.
assert_no_sentinel() {
  local export_tar="$1"
  local label="$2"
  local dump
  dump="$(mktemp "${tmp_dir}/contents.XXXXXX")"
  tar -xOf "$export_tar" >"$dump" 2>/dev/null
  if grep -aFq "$sentinel" "$dump"; then
    printf '%s runtime contains the secret sentinel\n' "$label" >&2
    exit 1
  fi
}

assert_no_sentinel "$api_export" API
assert_no_sentinel "$mcp_export" MCP

docker run --rm "${readonly_probe_flags[@]}" "$api_image" --version >/dev/null
api_help="$(docker run --rm "${readonly_probe_flags[@]}" "$api_image" --help)"
grep -Fq 'Usage: acr-api' <<<"$api_help"
if docker run --rm --network none "${readonly_probe_flags[@]}" \
  -e ACR_ENVIRONMENT=production "$api_image" serve >"${tmp_dir}/missing-production-config" 2>&1; then
  printf 'API accepted incomplete production configuration\n' >&2
  exit 1
fi
grep -Fq 'configuration:' "${tmp_dir}/missing-production-config"
if docker run --rm --network none "${readonly_probe_flags[@]}" \
  --entrypoint /usr/local/bin/acr-migrate "$api_image" status >"${tmp_dir}/migration" 2>&1; then
  printf 'acr-migrate accepted a missing migration DSN\n' >&2
  exit 1
fi
grep -Fq 'ACR_POSTGRES_MIGRATION_DSN or ACR_POSTGRES_MIGRATION_DSN_FILE is required' "${tmp_dir}/migration"

migration_network="acr-container-verify-$$-${RANDOM}"
docker network create "$migration_network" >/dev/null
migration_password="acr-${RANDOM}-${RANDOM}-$$"
# PGDATA must be set explicitly and must be a strict subdirectory of the mount.
# Relying on the image default only ever worked by coincidence: through 17 that
# default was /var/lib/postgresql/data, which equalled the tmpfs path, and 18
# moved it to /var/lib/postgresql/<major>/docker, at which point the entrypoint
# refuses to start against what it now sees as an unused mount:
#
#   Error: ... there appears to be PostgreSQL data in:
#     /var/lib/postgresql/data (unused mount/volume)
#
# Mount the parent and name the subdirectory, matching compose.yml and the Ops
# Helm chart's bundled PostgreSQL.
postgres_container="$(docker run -d \
  --network "$migration_network" \
  --network-alias postgres \
  --tmpfs /var/lib/postgresql:rw,noexec,nosuid,size=512m \
  -e PGDATA=/var/lib/postgresql/pgdata \
  -e POSTGRES_USER=acr \
  -e "POSTGRES_PASSWORD=${migration_password}" \
  -e POSTGRES_DB=acr \
  "$postgres_image")"
track_container "$postgres_container"
postgres_ready=0
for _ in {1..60}; do
  if docker run --rm --network "$migration_network" "${readonly_probe_flags[@]}" \
    "$postgres_image" pg_isready --quiet \
    --host postgres --port 5432 \
    --username acr --dbname acr --timeout=1; then
    postgres_ready=1
    break
  fi
  sleep 1
done
if [[ "$postgres_ready" != "1" ]]; then
  printf 'PostgreSQL migration fixture did not become ready\n' >&2
  exit 1
fi
migration_dsn="postgres://acr:${migration_password}@postgres:5432/acr?sslmode=disable"
migration_files=("${repo_root}"/migrations/postgres/[0-9][0-9][0-9][0-9]_*.sql)
expected_migration_count="${#migration_files[@]}"
docker run --rm --network "$migration_network" "${readonly_probe_flags[@]}" \
  -e ACR_ENVIRONMENT=test \
  -e "ACR_POSTGRES_MIGRATION_DSN=${migration_dsn}" \
  --entrypoint /usr/local/bin/acr-migrate "$api_image" up >"${tmp_dir}/migration-first"
grep -qxF "applied ${expected_migration_count} migrations" "${tmp_dir}/migration-first"
docker run --rm --network "$migration_network" "${readonly_probe_flags[@]}" \
  -e ACR_ENVIRONMENT=test \
  -e "ACR_POSTGRES_MIGRATION_DSN=${migration_dsn}" \
  --entrypoint /usr/local/bin/acr-migrate "$api_image" up >"${tmp_dir}/migration-second"
grep -qxF 'no migrations applied' "${tmp_dir}/migration-second"
docker run --rm --network "$migration_network" "${readonly_probe_flags[@]}" \
  -e ACR_ENVIRONMENT=test \
  -e "ACR_POSTGRES_MIGRATION_DSN=${migration_dsn}" \
  --entrypoint /usr/local/bin/acr-migrate "$api_image" status >"${tmp_dir}/migration-status"
for migration_file in "${migration_files[@]}"; do
  migration_name="$(basename "$migration_file")"
  migration_version="${migration_name%%_*}"
  printf '%04d %s\n' "$((10#$migration_version))" "$migration_name"
done >"${tmp_dir}/migration-expected-status"
cmp "${tmp_dir}/migration-expected-status" "${tmp_dir}/migration-status"
unset migration_dsn migration_password

api_container="$(docker run -d "${readonly_probe_flags[@]}" -p 127.0.0.1::8080 "$api_image")"
track_container "$api_container"
api_port="$(docker port "$api_container" 8080/tcp | awk -F: '{print $NF}')"
curl --fail --silent --show-error --retry 20 --retry-connrefused \
  "http://127.0.0.1:${api_port}/healthz" | jq -e '.status == "ok"' >/dev/null

docker run --rm "${readonly_probe_flags[@]}" "$mcp_image" version >/dev/null
docker run --rm "${readonly_probe_flags[@]}" "$mcp_image" metadata | jq -e \
  '.transport == "stdio" and .status == "read-only"' >/dev/null
# Credential reads participate in the lifecycle lock even when no credential is
# present. Keep the root filesystem read-only while providing only the lock's
# canonical directory as an ephemeral tmpfs, then pin this probe to a known-
# absent file so it proves the missing-credential classification rather than an
# ambient keyring/default-home outcome.
docker run --rm "${readonly_probe_flags[@]}" \
  --tmpfs /var/tmp:rw,noexec,nosuid,nodev,size=1m,mode=1777 \
  -e ACR_API_TOKEN_FILE=/var/tmp/acr-container-verify-missing-token \
  "$mcp_image" doctor --offline | jq -e \
  '.status == "incomplete_configuration" and .api_url_set == false and .credential_set == false' >/dev/null
docker run --rm "${readonly_probe_flags[@]}" --entrypoint /usr/bin/git "$mcp_image" --version >/dev/null

# Real read-only mounted Git workspace: `git` must operate on an actual
# repository bind-mounted read-only under the container's own read-only
# root filesystem as the non-root UID -- not merely report --version.
mkdir -p "${repo_root}/.tmp"
git_workspace="$(mktemp -d "${repo_root}/.tmp/container-git-workspace.XXXXXX")"
git -C "$git_workspace" init --quiet
git -C "$git_workspace" \
  -c user.email=container-verify@example.invalid -c user.name="container verify" \
  commit --quiet --allow-empty -m "container verification fixture"
workspace_commit="$(git -C "$git_workspace" rev-parse HEAD)"
chmod -R a+rX "$git_workspace"

reported_commit="$(docker run --rm "${readonly_probe_flags[@]}" \
  -v "${git_workspace}:/workspace:ro" \
  --entrypoint /usr/bin/git "$mcp_image" -C /workspace rev-parse HEAD)"
test "$reported_commit" = "$workspace_commit"
docker run --rm "${readonly_probe_flags[@]}" \
  -v "${git_workspace}:/workspace:ro" \
  --entrypoint /usr/bin/git "$mcp_image" -C /workspace log -1 --format=%H >/dev/null

# Exercise acr-mcp's own workspace discovery/diagnostics command -- the
# same sidecar.DiscoverWorkspace path "context_for_task" relies on --
# against that same real read-only mounted workspace, rather than only
# direct git above. This also proves the image's protected
# `safe.directory=/workspace` configuration actually lets Git operate as
# UID 65532 on a directory owned by the host runner's UID (the normal
# Linux case): without it, Git would refuse with a dubious-ownership
# error and this command would report status "error" instead of "ok".
workspace_report="$(docker run --rm "${readonly_probe_flags[@]}" \
  -v "${git_workspace}:/workspace:ro" \
  "$mcp_image" workspace --path /workspace)"
jq -e --arg commit "$workspace_commit" '.status == "ok" and .commit_sha == $commit' \
  <<<"$workspace_report" >/dev/null

printf 'container runtime verification passed\n'
