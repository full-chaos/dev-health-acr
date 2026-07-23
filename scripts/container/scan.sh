#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp_root="${repo_root}/.tmp"
stable_report_root="${tmp_root}/container-reports"
source_oci_root="${CONTAINER_SCAN_OCI_ROOT:-}"
lock_timeout="${CONTAINER_PUBLISH_LOCK_TIMEOUT:-60}"
work_root=""
exact_archives=false

trivy_image='aquasec/trivy:0.69.3@sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c'
syft_image='anchore/syft:v1.46.0@sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb'
trivy_db='ghcr.io/aquasecurity/trivy-db@sha256:ada5860f7d7b96affdd0ba2cd27f5fdfc8a366f1999539f4fc0cac6402a27c1f'
trivy_db_layer='sha256:fb88d0d82803bf208bd69f197e12fc50d2232f8917284b88fc86bf0ac0b9e546'
max_db_age_hours="${TRIVY_DB_MAX_AGE_HOURS:-168}"

require() { command -v "$1" >/dev/null || { printf '%s is required\n' "$1" >&2; exit 1; }; }
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
report_root="${work_root}/reports"
trivy_cache="${work_root}/trivy-cache"
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

docker pull "$trivy_image" >/dev/null
docker pull "$syft_image" >/dev/null

db_manifest="$(docker buildx imagetools inspect "$trivy_db" --raw)"
jq -e --arg layer "$trivy_db_layer" '.layers | any(.digest == $layer)' <<<"$db_manifest" >/dev/null || {
  printf 'pinned Trivy DB manifest does not contain the expected layer\n' >&2
  exit 1
}

docker run --rm --pull=never \
  --user "${scanner_uid}:${scanner_gid}" \
  --read-only --tmpfs /tmp:rw,noexec,nosuid,nodev,size=512m,mode=1777 \
  --cap-drop ALL --security-opt no-new-privileges \
  -e HOME=/tmp \
  -v "${trivy_cache}:/tmp/trivy-cache" \
  "$trivy_image" image \
  --cache-dir /tmp/trivy-cache \
  --db-repository "$trivy_db" \
  --download-db-only \
  --no-progress

metadata="${trivy_cache}/db/metadata.json"
test -f "$metadata" || { printf 'Trivy DB metadata was not downloaded\n' >&2; exit 1; }
now="$(date -u +%s)"
max_age_seconds="$((max_db_age_hours * 60 * 60))"
jq -e --argjson now "$now" --argjson max_age "$max_age_seconds" '
  def epoch: sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601;
  .Version == 2
  and (.UpdatedAt | epoch) <= $now
  and (.DownloadedAt | epoch) <= $now
  and (.NextUpdate | epoch) > (.UpdatedAt | epoch)
  and (($now - (.UpdatedAt | epoch)) <= $max_age)
' "$metadata" >/dev/null || {
  printf 'pinned Trivy DB metadata is invalid or older than %s hours\n' "$max_db_age_hours" >&2
  exit 1
}
cp "$metadata" "${report_root}/trivy-db-metadata.json"
printf '%s\n%s\n' "$trivy_db" "$trivy_db_layer" >"${report_root}/trivy-db-snapshot.txt"

failures=0
for name in acr-api-amd64 acr-api-arm64 acr-mcp-amd64 acr-mcp-arm64; do
  trivy_input="/scan/$name"
  syft_source="oci-dir:/scan/$name"
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
  jq -e '[.Results[]?.Vulnerabilities[]? | select(.Severity == "HIGH" or .Severity == "CRITICAL")] | length == 0' \
    "${report_root}/${name}-trivy.json" >/dev/null || failures=1
  jq -e '.spdxVersion == "SPDX-2.3" and (.packages | length > 0)' \
    "${report_root}/${name}.spdx.json" >/dev/null || failures=1
done

test "$failures" -eq 0 || { printf 'one or more image scan or SBOM gates failed\n' >&2; exit 1; }
bash "${repo_root}/scripts/container/publish-directory.sh" "$stable_report_root" "$report_root" "$lock_timeout"
report_root=""
if "$exact_archives"; then
  printf 'four exact-archive scans and four immutable Syft SBOMs passed\n'
else
  printf 'four offline image scans and four immutable Syft SBOMs passed\n'
fi
