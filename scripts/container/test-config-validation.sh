#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

for tool in dd jq shasum tar; do
  command -v "$tool" >/dev/null || { printf '%s is required\n' "$tool" >&2; exit 1; }
done

store_blob() {
  local source="$1"
  local layout="$2"
  local digest size
  digest="$(shasum -a 256 "$source" | awk '{print $1}')"
  size="$(wc -c <"$source" | tr -d ' ')"
  cp "$source" "${layout}/blobs/sha256/${digest}"
  printf 'sha256:%s %s\n' "$digest" "$size"
}

build_archive() {
  local name="$1"
  local env_json="$2"
  local layout="${tmp_dir}/${name}"
  local descriptors=()
  local architecture machine binary_root binary layer descriptor layer_digest layer_size
  local config config_digest config_size manifest manifest_digest manifest_size descriptor_file
  mkdir -p "${layout}/blobs/sha256"
  printf '{"imageLayoutVersion":"1.0.0"}\n' >"${layout}/oci-layout"

  for architecture in amd64 arm64; do
    binary_root="${tmp_dir}/${name}-${architecture}-root"
    binary="${binary_root}/usr/local/bin/acr-api"
    mkdir -p "$(dirname "$binary")"
    dd if=/dev/zero of="$binary" bs=20 count=1 2>/dev/null
    printf '\177ELF' | dd of="$binary" bs=1 seek=0 conv=notrunc 2>/dev/null
    if [[ "$architecture" == amd64 ]]; then
      machine='\076\000'
    else
      machine='\267\000'
    fi
    printf '%b' "$machine" | dd of="$binary" bs=1 seek=18 conv=notrunc 2>/dev/null
    chmod +x "$binary"

    layer="${tmp_dir}/${name}-${architecture}-layer.tar"
    tar -cf "$layer" -C "$binary_root" usr/local/bin/acr-api
    descriptor="$(store_blob "$layer" "$layout")"
    layer_digest="${descriptor%% *}"
    layer_size="${descriptor##* }"

    config="${tmp_dir}/${name}-${architecture}-config.json"
    jq -cn --arg architecture "$architecture" --argjson env "$env_json" '
      {architecture: $architecture, os: "linux", config: {
        User: "65532:65532",
        Entrypoint: ["/usr/local/bin/acr-api"],
        Env: $env
      }}
    ' >"$config"
    descriptor="$(store_blob "$config" "$layout")"
    config_digest="${descriptor%% *}"
    config_size="${descriptor##* }"

    manifest="${tmp_dir}/${name}-${architecture}-manifest.json"
    jq -cn \
      --arg config_digest "$config_digest" --argjson config_size "$config_size" \
      --arg layer_digest "$layer_digest" --argjson layer_size "$layer_size" '
      {schemaVersion: 2, mediaType: "application/vnd.oci.image.manifest.v1+json",
       config: {mediaType: "application/vnd.oci.image.config.v1+json", digest: $config_digest, size: $config_size},
       layers: [{mediaType: "application/vnd.oci.image.layer.v1.tar", digest: $layer_digest, size: $layer_size}]}
    ' >"$manifest"
    descriptor="$(store_blob "$manifest" "$layout")"
    manifest_digest="${descriptor%% *}"
    manifest_size="${descriptor##* }"

    descriptor_file="${tmp_dir}/${name}-${architecture}-descriptor.json"
    jq -cn --arg architecture "$architecture" --arg digest "$manifest_digest" --argjson size "$manifest_size" '
      {mediaType: "application/vnd.oci.image.manifest.v1+json", digest: $digest, size: $size,
       platform: {os: "linux", architecture: $architecture}}
    ' >"$descriptor_file"
    descriptors+=("$descriptor_file")
  done

  image_index="${tmp_dir}/${name}-image-index.json"
  jq -cs '{schemaVersion: 2, mediaType: "application/vnd.oci.image.index.v1+json", manifests: .}' "${descriptors[@]}" >"$image_index"
  descriptor="$(store_blob "$image_index" "$layout")"
  image_index_digest="${descriptor%% *}"
  image_index_size="${descriptor##* }"
  jq -cn --arg digest "$image_index_digest" --argjson size "$image_index_size" '
    {schemaVersion: 2, manifests: [{mediaType: "application/vnd.oci.image.index.v1+json", digest: $digest, size: $size}]}
  ' >"${layout}/index.json"
  built_archive="${tmp_dir}/acr-api.tar"
  tar -cf "$built_archive" -C "$layout" oci-layout index.json blobs
}

for malformed_case in scalar mixed; do
  if [[ "$malformed_case" == scalar ]]; then
    env_json='"PATH=/usr/bin"'
  else
    env_json='["PATH=/usr/bin",42]'
  fi
  build_archive "$malformed_case" "$env_json"
  if bash "${repo_root}/scripts/container/validate-oci.sh" "$built_archive" >/dev/null 2>&1; then
    printf 'OCI validator accepted malformed Env case: %s\n' "$malformed_case" >&2
    exit 1
  fi
  verify_error="${tmp_dir}/${malformed_case}-verify-error.log"
  if bash "${repo_root}/scripts/container/verify-oci.sh" "$built_archive" >/dev/null 2>"$verify_error"; then
    printf 'OCI target verifier accepted malformed Env case: %s\n' "$malformed_case" >&2
    exit 1
  fi
  grep -q 'OCI config .* failed validation' "$verify_error" || {
    printf 'OCI target verifier did not reach Env validation for case: %s\n' "$malformed_case" >&2
    cat "$verify_error" >&2
    exit 1
  }
done
