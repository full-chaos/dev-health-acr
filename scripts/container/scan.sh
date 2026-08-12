#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp_root="${repo_root}/.tmp"
stable_report_root="${tmp_root}/container-reports"
source_oci_root="${CONTAINER_SCAN_OCI_ROOT:-}"
lock_timeout="${CONTAINER_PUBLISH_LOCK_TIMEOUT:-60}"
work_root=""
exact_archives=false

syft_image='anchore/syft:v1.46.0@sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb'

require() { command -v "$1" >/dev/null || { printf '%s is required\n' "$1" >&2; exit 1; }; }
require date
require docker
require id
require jq
require tar
[[ "$lock_timeout" =~ ^[1-9][0-9]*$ ]] || { printf 'CONTAINER_PUBLISH_LOCK_TIMEOUT must be a positive integer\n' >&2; exit 2; }
scanner_uid="$(id -u)"
scanner_gid="$(id -g)"
[[ "$scanner_uid" =~ ^[1-9][0-9]*$ ]] || { printf 'container SBOM generation requires a non-root invoking user\n' >&2; exit 2; }
[[ "$scanner_gid" =~ ^[0-9]+$ ]] || { printf 'container SBOM generation requires a numeric invoking group\n' >&2; exit 2; }

mkdir -p "$tmp_root"
work_root="$(mktemp -d "${tmp_root}/container-scan.work.XXXXXX")"
scan_root="${work_root}/layouts"
report_root="${work_root}/reports"
mkdir -p "$scan_root" "$report_root"
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

docker pull "$syft_image" >/dev/null

failures=0
for name in acr-api-amd64 acr-api-arm64 acr-mcp-amd64 acr-mcp-arm64; do
  syft_source="oci-dir:/scan/$name"
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

test "$failures" -eq 0 || { printf 'one or more SBOM gates failed\n' >&2; exit 1; }
bash "${repo_root}/scripts/container/publish-directory.sh" "$stable_report_root" "$report_root" "$lock_timeout"
report_root=""
if "$exact_archives"; then
  printf 'four exact-archive immutable Syft SBOMs passed\n'
else
  printf 'four freshly built immutable Syft SBOMs passed\n'
fi
