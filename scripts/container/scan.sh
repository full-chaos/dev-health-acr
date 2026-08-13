#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp_root="${repo_root}/.tmp"
stable_report_root="${tmp_root}/container-reports"
source_oci_root="${CONTAINER_SCAN_OCI_ROOT:-}"
lock_timeout="${CONTAINER_PUBLISH_LOCK_TIMEOUT:-60}"
work_root=""
exact_archives=false

# shellcheck source=scripts/container/lib/trivy-db-freshness.sh
source "${repo_root}/scripts/container/lib/trivy-db-freshness.sh"
# shellcheck source=scripts/container/lib/trivy-report-classify.sh
source "${repo_root}/scripts/container/lib/trivy-report-classify.sh"
# shellcheck source=scripts/container/lib/prune-stale-attempt-dirs.sh
source "${repo_root}/scripts/container/lib/prune-stale-attempt-dirs.sh"
# shellcheck source=scripts/container/lib/trivy-db-provenance.sh
source "${repo_root}/scripts/container/lib/trivy-db-provenance.sh"

trivy_image='aquasec/trivy:0.69.3@sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c'
syft_image='anchore/syft:v1.46.0@sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb'
# CHAOS-3772: the vulnerability DB is deliberately NOT pinned by digest in
# source. It is resolved fresh from this moving mirror tag on every run
# below, so no committed value can ever go stale by wall-clock alone -- only
# the scanner binary above is pinned. Code is pinned; threat-intel data that
# must stay current floats and is recorded for provenance instead.
trivy_db_mirror='ghcr.io/aquasecurity/trivy-db:2'
max_db_age_hours="${TRIVY_DB_MAX_AGE_HOURS:-168}"

require() { command -v "$1" >/dev/null || { printf '%s is required\n' "$1" >&2; exit 1; }; }
require awk
require date
require docker
require id
require jq
require tar
[[ "$max_db_age_hours" =~ ^[1-9][0-9]*$ ]] || { printf 'TRIVY_DB_MAX_AGE_HOURS must be a positive integer\n' >&2; exit 2; }
[[ "$lock_timeout" =~ ^[1-9][0-9]*$ ]] || { printf 'CONTAINER_PUBLISH_LOCK_TIMEOUT must be a positive integer\n' >&2; exit 2; }
scanner_uid="$(id -u)"
scanner_gid="$(id -g)"
[[ "$scanner_uid" =~ ^[1-9][0-9]*$ ]] || { printf 'container scanning requires a non-root invoking user\n' >&2; exit 2; }
[[ "$scanner_gid" =~ ^[0-9]+$ ]] || { printf 'container scanning requires a numeric invoking group\n' >&2; exit 2; }

mkdir -p "$tmp_root"
work_root="$(mktemp -d "${tmp_root}/container-scan.work.XXXXXX")"
scan_root="${work_root}/layouts"
trivy_cache="${work_root}/trivy-cache"
# report_root deliberately lives OUTSIDE work_root, so cleanup()'s rm -rf on
# any exit path never touches it: a run that fails before publication
# (mirror unreachable, stale DB, real findings) still leaves whatever
# reports it managed to write on disk for ci.yml's always() artifact
# upload (CHAOS-3772 F2). Stale attempt directories from a previous failed
# run are pruned here rather than accumulating forever on a long-lived
# machine; a successful run's own directory is moved away by the
# publication step below, so only failed attempts ever linger. Pruning is
# age-based (CHAOS-3772 R3): a run takes minutes, so anything older than
# the 6h default is safely stale, and two scan.sh invocations sharing this
# .tmp root can never race each other's brand-new directory.
report_root="$(mktemp -d "${tmp_root}/container-scan-attempt.XXXXXX")"
prune_stale_attempt_dirs "$tmp_root" 'container-scan-attempt.' "$report_root"
mkdir -p "$scan_root" "$report_root" "$trivy_cache"
if [[ -n "$source_oci_root" ]]; then
  source_oci_root="$(cd "$source_oci_root" && pwd -P)"
  test -f "$source_oci_root/acr-api.tar"
  test -f "$source_oci_root/acr-mcp.tar"
  exact_archives=true
fi

cleanup() {
  if [[ -n "$work_root" ]]; then
    rm -rf "$work_root"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

build_layout() {
  local target="$1"
  local architecture="$2"
  local name="${target}-${architecture}"
  local archive="${scan_root}/${name}.tar"
  local layout="${scan_root}/${name}"

  CONTAINER_NO_CACHE=1 \
    CONTAINER_OUTPUT=oci \
    CONTAINER_PLATFORMS="linux/${architecture}" \
    CONTAINER_OCI_OUTPUT="$archive" \
    "${repo_root}/scripts/container/build.sh" "$target"
  mkdir -p "$layout"
  tar -xf "$archive" -C "$layout"
}

materialize_archive_layouts() {
  local product="$1"
  local archive="${source_oci_root}/${product}.tar"
  local source_layout="${scan_root}/${product}-source"
  local root_index image_index_digest image_index
  local architecture layout

  mkdir -p "$source_layout"
  tar -xf "$archive" -C "$source_layout"
  root_index="$(<"${source_layout}/index.json")"
  image_index_digest="$(jq -er '.manifests | select(length == 1) | .[0].digest' <<<"$root_index")"
  image_index="${source_layout}/blobs/sha256/${image_index_digest#sha256:}"
  test -f "$image_index"

  for architecture in amd64 arm64; do
    layout="${scan_root}/${product}-${architecture}"
    mkdir -p "$layout"
    cp "${source_layout}/oci-layout" "$layout/oci-layout"
    ln -s "../${product}-source/blobs" "$layout/blobs"
    jq -e --arg architecture "$architecture" '
      {
        schemaVersion: 2,
        mediaType: "application/vnd.oci.image.index.v1+json",
        manifests: [.manifests[] | select(.platform.os == "linux" and .platform.architecture == $architecture)]
      } |
      select(.manifests | length == 1)
    ' "$image_index" >"$layout/index.json"
  done
}

if "$exact_archives"; then
  materialize_archive_layouts acr-api
  materialize_archive_layouts acr-mcp
else
  build_layout acr-api amd64
  build_layout acr-api arm64
  build_layout acr-mcp amd64
  build_layout acr-mcp arm64
fi

docker pull "$trivy_image" >/dev/null || {
  printf 'trivy scanner image unreachable: could not pull %s -- transient registry outage, retry the job (not a vulnerability finding)\n' "$trivy_image" >&2
  exit 1
}
docker pull "$syft_image" >/dev/null

# Resolve the moving trivy-db mirror tag to one immutable digest up front,
# so the download below and the provenance record both act on the exact
# same snapshot instead of racing a tag that can move mid-run.
trivy_db_inspection="$(docker buildx imagetools inspect "$trivy_db_mirror" 2>/dev/null)" || {
  printf 'trivy-db mirror unreachable: could not resolve %s -- transient registry/mirror outage, retry the job (this is not a vulnerability finding and not a stale pin)\n' "$trivy_db_mirror" >&2
  exit 1
}
trivy_db_digest="$(awk '$1 == "Digest:" { digest = $2 } END { print digest }' <<<"$trivy_db_inspection")"
[[ -n "$trivy_db_digest" ]] || {
  printf 'trivy-db mirror returned no digest for %s\n' "$trivy_db_mirror" >&2
  exit 1
}
trivy_db_ref="${trivy_db_mirror%%:*}@${trivy_db_digest}"

docker run --rm --pull=never \
  --user "${scanner_uid}:${scanner_gid}" \
  --read-only --tmpfs /tmp:rw,noexec,nosuid,nodev,size=512m,mode=1777 \
  --cap-drop ALL --security-opt no-new-privileges \
  -e HOME=/tmp \
  -v "${trivy_cache}:/tmp/trivy-cache" \
  "$trivy_image" image \
  --cache-dir /tmp/trivy-cache \
  --db-repository "$trivy_db_ref" \
  --download-db-only \
  --no-progress || {
  printf 'trivy-db download failed for resolved snapshot %s -- transient registry/mirror outage, retry the job\n' "$trivy_db_ref" >&2
  exit 1
}

metadata="${trivy_cache}/db/metadata.json"
test -f "$metadata" || { printf 'trivy-db metadata was not downloaded\n' >&2; exit 1; }
now="$(date -u +%s)"
resolved_at="$(date -u -d "@${now}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$now" +%Y-%m-%dT%H:%M:%SZ)"
# Record before judging (CHAOS-3772 F2): if the freshness check below
# rejects this snapshot, the run still needs to show what it rejected. A
# tripped alarm without its own evidence is unauditable. The write itself
# is explicitly rc-checked, not left to `set -e` (CHAOS-3772 R2-3): a full
# or unwritable report_root must fail loudly as its own distinct case,
# never silently skip the freshness judgment below.
record_trivy_db_provenance "$metadata" "$report_root" "$trivy_db_mirror" "$resolved_at" "$trivy_db_ref" || exit 1
check_trivy_db_freshness "$metadata" "$max_db_age_hours" "$now" || exit 1

failures=0
for name in acr-api-amd64 acr-api-arm64 acr-mcp-amd64 acr-mcp-arm64; do
  trivy_input="/scan/$name"
  syft_source="oci-dir:/scan/$name"
  trivy_report="${report_root}/${name}-trivy.json"
  if ! docker run --rm --pull=never --network none \
    --user "${scanner_uid}:${scanner_gid}" \
    --read-only --tmpfs /tmp:rw,noexec,nosuid,nodev,size=512m,mode=1777 \
    --cap-drop ALL --security-opt no-new-privileges \
    -e HOME=/tmp \
    -v "${scan_root}:/scan:ro" \
    -v "${report_root}:/reports" \
    -v "${trivy_cache}:/tmp/trivy-cache" \
    "$trivy_image" image \
    --cache-dir /tmp/trivy-cache \
    --skip-db-update \
    --skip-version-check \
    --input "$trivy_input" \
    --severity HIGH,CRITICAL \
    --exit-code 1 \
    --format json \
    --output "/reports/${name}-trivy.json"; then
    # Lead the failure with exactly what changed, not a generic exit code,
    # so this can never be mistaken for an infra failure or the removed
    # pin-expiry bug. trivy_scan_findings only succeeds for a report that
    # is valid AND has at least one HIGH/CRITICAL entry (CHAOS-3772 F4): a
    # syntactically valid but empty or sub-threshold report is an
    # execution failure, not a finding, and must not be printed as one.
    if scan_findings="$(trivy_scan_findings "$trivy_report")"; then
      printf 'HIGH/CRITICAL vulnerabilities in %s:\n' "$name" >&2
      printf '%s\n' "$scan_findings" | sed 's/^/  /' >&2
    else
      printf 'trivy scan failed to execute for %s (no valid report, or a nonzero exit with no HIGH/CRITICAL findings) -- scanner/infra failure, not a vulnerability finding\n' "$name" >&2
    fi
    failures=1
  fi

  if ! docker run --rm --pull=never --network none \
    --user "${scanner_uid}:${scanner_gid}" \
    --read-only --tmpfs /tmp:rw,noexec,nosuid,nodev,size=512m,mode=1777 \
    --cap-drop ALL --security-opt no-new-privileges \
    -e HOME=/tmp -e SYFT_CHECK_FOR_APP_UPDATE=false \
    -v "${scan_root}:/scan:ro" \
    -v "${report_root}:/reports" \
    "$syft_image" "$syft_source" \
    -o "spdx-json=/reports/${name}.spdx.json"; then
    failures=1
  fi
done

for name in acr-api-amd64 acr-api-arm64 acr-mcp-amd64 acr-mcp-arm64; do
  if ! jq -e '.spdxVersion == "SPDX-2.3" and (.packages | length > 0)' \
    "${report_root}/${name}.spdx.json" >/dev/null; then
    printf 'invalid or missing SPDX SBOM: %s\n' "$name" >&2
    failures=1
  fi
done

test "$failures" -eq 0 || { printf 'one or more image scan or SBOM gates failed\n' >&2; exit 1; }
bash "${repo_root}/scripts/container/publish-directory.sh" "$stable_report_root" "$report_root" "$lock_timeout"
report_root=""
if "$exact_archives"; then
  printf 'four exact-archive scans and four immutable Syft SBOMs passed\n'
else
  printf 'four offline image scans and four immutable Syft SBOMs passed\n'
fi
