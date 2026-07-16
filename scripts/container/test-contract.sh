#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

require() {
  local path="$1"
  test -e "${repo_root}/${path}" || {
    printf 'missing required container artifact: %s\n' "$path" >&2
    exit 1
  }
}

require Dockerfile
require .dockerignore
require docs/container-images.md
require scripts/container/build.sh
require scripts/container/create-context.sh
require scripts/container/oci.sh
require scripts/container/publish-directory.sh
require scripts/container/scan.sh
require scripts/container/test-config-validation.sh
require scripts/container/test-publication.sh
require scripts/container/validate-oci.sh
require scripts/container/verify-pins.sh
require scripts/container/verify.sh
require scripts/container/reproducible.sh

grep -q 'CGO_ENABLED=0' "${repo_root}/Dockerfile"
grep -q -- '-trimpath' "${repo_root}/Dockerfile"
grep -q 'acr-api' "${repo_root}/Dockerfile"
grep -q 'acr-mcp' "${repo_root}/Dockerfile"
grep -q '@sha256:' "${repo_root}/Dockerfile"
grep -qxF '*' "${repo_root}/.dockerignore"
grep -qxF '!Dockerfile' "${repo_root}/.dockerignore"
allow_count="$(grep -cE '^!' "${repo_root}/.dockerignore")"
test "$allow_count" -eq 1 || {
  printf '.dockerignore allowlist has %s entries, expected exactly 1\n' "$allow_count" >&2
  exit 1
}
grep -q "find .*cmd.*-name '\*.go'" "${repo_root}/scripts/container/create-context.sh"
grep -q "find .*internal.*-name '\*.json'" "${repo_root}/scripts/container/create-context.sh"
grep -q "find .*migrations.*-name '\*.sql'" "${repo_root}/scripts/container/create-context.sh"
grep -q 'create-context.sh' "${repo_root}/scripts/container/build.sh"

# 1. QEMU binfmt image and BuildKit driver image must be pinned by immutable
# digest; the Buildx CLI version must be pinned to an explicit release.
ci_workflow="${repo_root}/.github/workflows/ci.yml"
while IFS= read -r action_ref; do
  [[ "$action_ref" =~ @[0-9a-f]{40}$ ]] || {
    printf 'GitHub action is not pinned to a full commit SHA: %s\n' "$action_ref" >&2
    exit 1
  }
done < <(awk '/^[[:space:]]*- uses:/ { print $3 }' "$ci_workflow")
grep -Eq 'image: docker\.io/tonistiigi/binfmt:[^[:space:]]+@sha256:[0-9a-f]{64}' "$ci_workflow"
grep -q 'd41ece72044243b4f58b343441ae37446d9c29a7d6b5e11c61847bbcf8f7dfda' "$ci_workflow"
grep -Eq 'driver-opts: image=moby/buildkit:v0\.31\.0@sha256:[0-9a-f]{64}' "$ci_workflow"

# 2. The Dockerfile build-time frontend must be pinned by immutable digest,
# not a mutable tag alone.
grep -Eq '^# syntax=docker/dockerfile:[^[:space:]]+@sha256:[0-9a-f]{64}$' "${repo_root}/Dockerfile"

# 4. Both target architectures must be scanned and SBOMed; unfixed
# HIGH/CRITICAL findings must not be hidden via --ignore-unfixed.
grep -q 'bash scripts/container/oci.sh' "${repo_root}/Makefile"
grep -q 'CONTAINER_PLATFORMS=linux/amd64,linux/arm64' "${repo_root}/scripts/container/oci.sh"
test "$(grep -c 'CONTAINER_NO_CACHE=1 CONTAINER_OUTPUT=oci' "${repo_root}/scripts/container/oci.sh")" -eq 2
grep -q 'container-oci.work.XXXXXX' "${repo_root}/scripts/container/oci.sh"
grep -q 'publish-directory.sh' "${repo_root}/scripts/container/oci.sh"
grep -q 'publish-directory.sh' "${repo_root}/scripts/container/scan.sh"
grep -q '\.publish\.lock' "${repo_root}/scripts/container/publish-directory.sh"
if grep -q 'rm -rf .tmp/container-oci' "${repo_root}/Makefile"; then
  printf 'container-oci must not delete the shared stable directory before building\n' >&2
  exit 1
fi
test "$(grep -c '^build_layout acr-' "${repo_root}/scripts/container/scan.sh")" -eq 4
scan_failure_line="$(grep -n 'one or more image scan or SBOM gates failed' "${repo_root}/scripts/container/scan.sh" | cut -d: -f1)"
scan_publish_line="$(grep -n 'publish-directory.sh' "${repo_root}/scripts/container/scan.sh" | cut -d: -f1)"
test "$scan_failure_line" -lt "$scan_publish_line" || {
  printf 'container scan must reject failures before publication\n' >&2
  exit 1
}
grep -q 'wait_for_oci_archive' "${repo_root}/scripts/container/build.sh"
if grep -q $'^\tsync$' "${repo_root}/Makefile"; then
  printf 'container scan must wait for each OCI exporter, not use a global sync\n' >&2
  exit 1
fi
if grep -q -- '--ignore-unfixed' "${repo_root}/Makefile"; then exit 1; fi
if grep -q -- '--ignore-unfixed' "${repo_root}/docs/container-images.md"; then exit 1; fi
grep -q 'acr-api-amd64' "${repo_root}/docs/container-images.md"
grep -q 'acr-mcp-arm64' "${repo_root}/docs/container-images.md"
for transitive_pin in 'docker/dockerfile:1.20@sha256:' 'tonistiigi/binfmt:qemu-v10.2.3@sha256:' 'moby/buildkit:v0.31.0@sha256:' 'anchore/syft:v1.46.0@sha256:' 'aquasecurity/trivy-db@sha256:' 'postgres:17-alpine@sha256:' 'actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0' 'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16' 'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a'; do
  grep -qF "$transitive_pin" "${repo_root}/docs/container-images.md" || {
    printf 'container documentation is missing transitive pin: %s\n' "$transitive_pin" >&2
    exit 1
  }
done

# 5. OCI verification must inspect the actual ELF machine of each
# extracted binary, not only manifest platform labels.
grep -q 'elf_machine' "${repo_root}/scripts/container/verify-oci.sh"
grep -q 'platform.os' "${repo_root}/scripts/container/verify-oci.sh"
if grep -q 'tac' "${repo_root}/scripts/container/verify-oci.sh"; then exit 1; fi

# 6. Reproducibility must refuse a dirty worktree and build from the
# actual clean committed tree, not the working directory.
grep -q 'status --porcelain' "${repo_root}/scripts/container/reproducible.sh"
grep -q 'archive ' "${repo_root}/scripts/container/reproducible.sh"

# 7. Runtime verification must exercise a real read-only mounted git
# workspace and assert package managers/extra shells are absent by
# actual exported filesystem contents, not merely a --version smoke.
grep -q '/workspace' "${repo_root}/scripts/container/verify.sh"
grep -q 'assert_no_package_manager' "${repo_root}/scripts/container/verify.sh"
test "$(grep -c 'assert_no_shell .* MCP' "${repo_root}/scripts/container/verify.sh")" -eq 1
grep -q 'migration-first' "${repo_root}/scripts/container/verify.sh"
grep -q 'migration-second' "${repo_root}/scripts/container/verify.sh"
verify_script="${repo_root}/scripts/container/verify.sh"
if grep -q 'docker exec .*pg_isready' "$verify_script"; then
  printf 'PostgreSQL readiness must not use the container-local socket\n' >&2
  exit 1
fi
grep -qF "docker run --rm --network \"\$migration_network\" \"\${readonly_probe_flags[@]}\"" "$verify_script" || {
  printf 'PostgreSQL readiness must use a hardened peer on the migration network\n' >&2
  exit 1
}
grep -qF "\"\$postgres_image\" pg_isready --quiet" "$verify_script" || {
  printf 'PostgreSQL readiness must use the pinned PostgreSQL client\n' >&2
  exit 1
}
grep -qF -- '--host postgres --port 5432' "$verify_script" || {
  printf 'PostgreSQL readiness must probe the migration DSN host and port\n' >&2
  exit 1
}
grep -qF -- '--timeout=1' "$verify_script" || {
  printf 'PostgreSQL readiness attempts must have a bounded client timeout\n' >&2
  exit 1
}

# 8. Local artifact builds must refuse a dirty product worktree by
# default and only label metadata as dirty under an explicit,
# narrowly-named local opt-in -- never build silently as if the
# uncommitted tree were the labeled commit.
grep -q 'CONTAINER_ALLOW_DIRTY' "${repo_root}/scripts/container/build.sh"
grep -q -- '-dirty' "${repo_root}/scripts/container/build.sh"
grep -q 'cannot inspect source tree status' "${repo_root}/scripts/container/build.sh"
grep -q 'ignored files exist inside container source paths' "${repo_root}/scripts/container/build.sh"

# 9. The buildx build invocation must be bounded by a configurable
# process timeout, and OCI export must be atomic: write to a unique temp
# path and rename into place only after the archive is confirmed intact.
grep -q 'CONTAINER_BUILD_TIMEOUT' "${repo_root}/scripts/container/build.sh"
grep -q 'CONTAINER_BUILD_KILL_GRACE' "${repo_root}/scripts/container/build.sh"
grep -q 'run_with_timeout' "${repo_root}/scripts/container/build.sh"
grep -q 'signal_retained_pids KILL' "${repo_root}/scripts/container/build.sh"
grep -q "trap 'handle_signal 130' INT" "${repo_root}/scripts/container/build.sh"
grep -q "trap 'handle_signal 143' TERM" "${repo_root}/scripts/container/build.sh"
grep -q 'validate-oci.sh' "${repo_root}/scripts/container/build.sh"
grep -q 'oci_temp' "${repo_root}/scripts/container/build.sh"
# shellcheck disable=SC2016
grep -q 'mv "$oci_temp" "$oci_output"' "${repo_root}/scripts/container/build.sh"

# 10. Smoke proof filenames and image tags must be unique per invocation
# so concurrent invocations never collide and cleanup never races another
# invocation's own resources.
if grep -qxF 'api_image="acr-api:smoke"' "${repo_root}/scripts/container/smoke.sh"; then
  printf 'smoke.sh must not use a fixed, shared image tag\n' >&2
  exit 1
fi
grep -q 'invocation_id' "${repo_root}/scripts/container/smoke.sh"
grep -q 'internal/.env.container-proof' "${repo_root}/scripts/container/smoke.sh"
grep -q 'git clone --quiet --no-hardlinks' "${repo_root}/scripts/container/smoke.sh"
grep -q "CONTAINER_SOURCE_ROOT=\"\$snapshot\"" "${repo_root}/scripts/container/smoke.sh"
if grep -Eq 'mktemp .*\$\{?repo_root' "${repo_root}/scripts/container/smoke.sh"; then
  printf 'smoke.sh must not create fixtures in the shared source worktree\n' >&2
  exit 1
fi

# 11. tar output must never be piped directly into grep -q (or any
# other early-exit grep mode): under pipefail, a match closes grep's
# stdin early, the still-writing tar is killed by SIGPIPE, and pipefail
# reports that SIGPIPE as the pipeline's own failure even though grep
# found exactly the match it was looking for -- a false negative for
# whichever security property the check exists to enforce.
tar_grep_q_pipe='tar[^|]*\|[^|]*grep[^|]*-[a-zA-Z]*q'
for script in verify.sh verify-oci.sh smoke.sh; do
  if grep -Eq "$tar_grep_q_pipe" "${repo_root}/scripts/container/${script}"; then
    printf '%s must not pipe tar output directly into an early-exit grep\n' "$script" >&2
    exit 1
  fi
done

# 12. The MCP image must configure a protected (non-wildcard)
# safe.directory for the documented /workspace mount point, and
# verification must exercise acr-mcp's own workspace discovery command --
# not only direct git -- against a real read-only mounted workspace.
grep -Eq 'directory = /workspace' "${repo_root}/Dockerfile"
if grep -Eq 'directory = \*' "${repo_root}/Dockerfile"; then
  printf 'Dockerfile must not use a wildcard safe.directory\n' >&2
  exit 1
fi
# shellcheck disable=SC2016
grep -q '"$mcp_image" workspace' "${repo_root}/scripts/container/verify.sh"

# 13. Compilation must run network-isolated after module download; only
# the module-download step may reach the network.
test "$(grep -c -- '--network=none' "${repo_root}/Dockerfile")" -eq 1

# 14. Runtime probes must drop all Linux capabilities and block privilege
# escalation, in addition to the existing read-only-root/non-root user.
grep -q -- '--cap-drop ALL' "${repo_root}/scripts/container/verify.sh"
grep -q -- '--security-opt no-new-privileges' "${repo_root}/scripts/container/verify.sh"
if grep -Ev '^[[:space:]]*#' "${repo_root}/scripts/container/verify.sh" | grep 'docker run' | grep -v 'postgres_container=' | grep -qv 'readonly_probe_flags'; then
  printf 'every docker run probe must apply readonly_probe_flags (--cap-drop ALL --security-opt no-new-privileges)\n' >&2
  exit 1
fi

# 15. A failed docker create/export/cp path must still remove its own
# container: every created container is tracked for cleanup the moment
# it exists, not only after its own happy-path steps succeed.
grep -q 'created_containers' "${repo_root}/scripts/container/verify.sh"
grep -q 'created_containers' "${repo_root}/scripts/container/reproducible.sh"

# 16. OCI verification must recursively validate descriptor existence,
# size, and sha256 for the index/manifest/config/layers, validate the
# image config (OS/arch/user/entrypoint/env), and merge layers with
# OCI whiteout semantics rather than accepting the first matching layer.
grep -q 'fetch_descriptor' "${repo_root}/scripts/container/verify-oci.sh"
grep -q 'assert_config_descriptor' "${repo_root}/scripts/container/verify-oci.sh"
grep -qi 'whiteout' "${repo_root}/scripts/container/verify-oci.sh"
if grep -Eq 'done < <\(jq' "${repo_root}/scripts/container/validate-oci.sh" "${repo_root}/scripts/container/verify-oci.sh"; then
  printf 'OCI descriptor jq failures must not be hidden by process substitution\n' >&2
  exit 1
fi

# 17. Scanning uses one immutable, freshness-checked Trivy DB snapshot for
# four network-disabled scans and an immutable Syft image for four SBOMs.
grep -q 'aquasecurity/trivy-db@sha256:' "${repo_root}/scripts/container/scan.sh"
grep -q 'trivy_db_layer=' "${repo_root}/scripts/container/scan.sh"
grep -q 'TRIVY_DB_MAX_AGE_HOURS' "${repo_root}/scripts/container/scan.sh"
test "$(grep -c -- '--skip-db-update' "${repo_root}/scripts/container/scan.sh")" -eq 1
grep -q 'anchore/syft:v1.46.0@sha256:' "${repo_root}/scripts/container/scan.sh"
grep -q 'container-scan.work.XXXXXX' "${repo_root}/scripts/container/scan.sh"
scan_script="${repo_root}/scripts/container/scan.sh"
grep -qF "scanner_uid=\"\$(id -u)\"" "$scan_script" || {
  printf 'container scanners must derive the invoking host UID\n' >&2
  exit 1
}
grep -qF "scanner_gid=\"\$(id -g)\"" "$scan_script" || {
  printf 'container scanners must derive the invoking host GID\n' >&2
  exit 1
}
test "$(grep -cF -- "--user \"\${scanner_uid}:\${scanner_gid}\"" "$scan_script")" -eq 3 || {
  printf 'DB download, Trivy scans, and Syft SBOMs must run as the invoking non-root user\n' >&2
  exit 1
}
test "$(grep -c -- '--read-only' "$scan_script")" -eq 3 || {
  printf 'every scanner container must use a read-only root filesystem\n' >&2
  exit 1
}
test "$(grep -cF -- '--tmpfs /tmp:rw,noexec,nosuid,nodev,size=512m,mode=1777' "$scan_script")" -eq 3 || {
  printf 'every scanner container must provide bounded non-root scratch space\n' >&2
  exit 1
}
if grep -q '/root/.cache/trivy' "$scan_script"; then
  printf 'Trivy cache must not depend on root-home traversal or ownership\n' >&2
  exit 1
fi
if grep -q "rm -rf \"\$scan_root\" \"\$report_root\" \"\$trivy_cache\"" "${repo_root}/scripts/container/scan.sh"; then
  printf 'container scan must not delete shared work roots before generating reports\n' >&2
  exit 1
fi

# 18. The MCP runtime must contain Git but no shell at all; build-only shell
# use is pruned before the final scratch target.
grep -q 'FROM scratch AS acr-mcp' "${repo_root}/Dockerfile"
grep -q 'rm -f /mcp-root/usr/bin/sh /mcp-root/usr/bin/dash' "${repo_root}/Dockerfile"
if grep -q 'documented base-image exception' "${repo_root}/docs/container-images.md"; then
  printf 'MCP documentation must not retain a runtime shell exception\n' >&2
  exit 1
fi

timeout_fixture="$(mktemp -d)"
timeout_parent_pid="${timeout_fixture}/parent.pid"
timeout_child_pid="${timeout_fixture}/child.pid"
timeout_late_child_pid="${timeout_fixture}/late-child.pid"
cleanup_timeout_fixture() {
  local pid_file pid
  for pid_file in "$timeout_parent_pid" "$timeout_child_pid" "$timeout_late_child_pid"; do
    if [[ -f "$pid_file" ]]; then
      pid="$(cat "$pid_file")"
      kill -KILL "$pid" 2>/dev/null || true
    fi
  done
  rm -rf "$timeout_fixture"
}
trap cleanup_timeout_fixture EXIT
cat >"${timeout_fixture}/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$$" >"${CONTAINER_TEST_PARENT_PID:?}"
sh -c 'trap "" TERM INT; printf "%s\n" "$$" >"$1"; while :; do sleep 30 || true; done' sh "${CONTAINER_TEST_CHILD_PID:?}" &
spawn_late_child() {
  sh -c 'trap "" TERM INT; printf "%s\n" "$$" >"$1"; while :; do sleep 30 || true; done' sh "${CONTAINER_TEST_LATE_CHILD_PID:?}" &
  exit 0
}
trap spawn_late_child TERM
wait
EOF
chmod +x "${timeout_fixture}/docker"
set +e
PATH="${timeout_fixture}:$PATH" \
  CONTAINER_TEST_PARENT_PID="$timeout_parent_pid" \
  CONTAINER_TEST_CHILD_PID="$timeout_child_pid" \
  CONTAINER_TEST_LATE_CHILD_PID="$timeout_late_child_pid" \
  CONTAINER_ALLOW_DIRTY=1 \
  CONTAINER_BUILD_TIMEOUT=1 \
  CONTAINER_BUILD_KILL_GRACE=1 \
  "${repo_root}/scripts/container/build.sh" acr-api >"${timeout_fixture}/timeout.log" 2>&1
timeout_status=$?
set -e
test "$timeout_status" -eq 124 || {
  printf 'build timeout test returned %s, expected 124\n' "$timeout_status" >&2
  cat "${timeout_fixture}/timeout.log" >&2
  exit 1
}
for pid_file in "$timeout_parent_pid" "$timeout_child_pid" "$timeout_late_child_pid"; do
  test -s "$pid_file" || { printf 'timeout fixture did not record a process ID\n' >&2; exit 1; }
  pid="$(cat "$pid_file")"
  if kill -0 "$pid" 2>/dev/null; then
    printf 'build timeout left process %s running\n' "$pid" >&2
    exit 1
  fi
done

rm -f "$timeout_parent_pid" "$timeout_child_pid" "$timeout_late_child_pid"
PATH="${timeout_fixture}:$PATH" \
  CONTAINER_TEST_PARENT_PID="$timeout_parent_pid" \
  CONTAINER_TEST_CHILD_PID="$timeout_child_pid" \
  CONTAINER_TEST_LATE_CHILD_PID="$timeout_late_child_pid" \
  CONTAINER_ALLOW_DIRTY=1 \
  CONTAINER_BUILD_TIMEOUT=30 \
  CONTAINER_BUILD_KILL_GRACE=1 \
  "${repo_root}/scripts/container/build.sh" acr-api >/dev/null 2>&1 &
signaled_build_pid=$!
for _ in {1..200}; do
  [[ -s "$timeout_child_pid" ]] && break
  sleep 0.1
done
test -s "$timeout_child_pid" || { printf 'signal fixture did not start its child\n' >&2; exit 1; }
kill -TERM "$signaled_build_pid"
set +e
wait "$signaled_build_pid"
signal_status=$?
set -e
test "$signal_status" -eq 143 || {
  printf 'externally terminated build returned %s, expected 143\n' "$signal_status" >&2
  exit 1
}
for pid_file in "$timeout_parent_pid" "$timeout_child_pid" "$timeout_late_child_pid"; do
  pid="$(cat "$pid_file")"
  if kill -0 "$pid" 2>/dev/null; then
    printf 'external termination left process %s running\n' "$pid" >&2
    exit 1
  fi
done

malformed_layout="${timeout_fixture}/malformed-layout"
mkdir -p "${malformed_layout}/blobs/sha256"
printf '{"imageLayoutVersion":"1.0.0"}\n' >"${malformed_layout}/oci-layout"
printf '%s\n' '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","size":0},"layers":"invalid"}' >"${timeout_fixture}/manifest.json"
manifest_digest="$(shasum -a 256 "${timeout_fixture}/manifest.json" | awk '{print $1}')"
manifest_size="$(wc -c <"${timeout_fixture}/manifest.json" | tr -d ' ')"
cp "${timeout_fixture}/manifest.json" "${malformed_layout}/blobs/sha256/${manifest_digest}"
printf '{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:%s","size":%s}]}\n' \
  "$manifest_digest" "$manifest_size" >"${malformed_layout}/index.json"
tar -cf "${timeout_fixture}/malformed.tar" -C "$malformed_layout" oci-layout index.json blobs
if "${repo_root}/scripts/container/validate-oci.sh" "${timeout_fixture}/malformed.tar" >/dev/null 2>&1; then
  printf 'OCI validator accepted a malformed layers descriptor\n' >&2
  exit 1
fi
"${repo_root}/scripts/container/test-config-validation.sh"
bash "${repo_root}/scripts/container/test-publication.sh"
cleanup_timeout_fixture
trap - EXIT

printf 'container contract files are present\n'
