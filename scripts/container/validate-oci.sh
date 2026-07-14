#!/usr/bin/env bash
set -euo pipefail

archive="${1:?usage: validate-oci.sh <archive.tar>}"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

require() { command -v "$1" >/dev/null || { printf '%s is required\n' "$1" >&2; exit 1; }; }
require jq
require shasum
require tar

test -s "$archive" || { printf 'OCI archive is missing or empty: %s\n' "$archive" >&2; exit 1; }

extract_entry() {
  local entry="$1"
  local destination="$2"
  tar -xOf "$archive" "$entry" >"$destination" 2>/dev/null || {
    printf 'OCI archive is missing %s: %s\n' "$entry" "$archive" >&2
    exit 1
  }
}

validate_blob() {
  local digest="$1"
  local expected_size="$2"
  local destination="$3"
  local blob_path="blobs/sha256/${digest#sha256:}"
  local actual_size actual_digest

  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || {
    printf 'OCI descriptor has an invalid digest: %s\n' "$digest" >&2
    exit 1
  }
  extract_entry "$blob_path" "$destination"

  actual_size="$(wc -c <"$destination" | tr -d ' ')"
  test "$actual_size" = "$expected_size" || {
    printf 'OCI descriptor size mismatch for %s: expected %s, got %s\n' "$digest" "$expected_size" "$actual_size" >&2
    exit 1
  }
  actual_digest="$(shasum -a 256 "$destination" | awk '{print $1}')"
  test "sha256:${actual_digest}" = "$digest" || {
    printf 'OCI descriptor digest mismatch for %s\n' "$digest" >&2
    exit 1
  }
}

validate_descriptor() {
  local digest="$1"
  local size="$2"
  local media_type="$3"
  local blob config_blob layer_blob descriptors
  blob="$(mktemp "${tmp_dir}/blob.XXXXXX")"
  validate_blob "$digest" "$size" "$blob"

  case "$media_type" in
    application/vnd.oci.image.index.v1+json|application/vnd.docker.distribution.manifest.list.v2+json)
      jq -e '.schemaVersion == 2 and (.manifests | length > 0)' "$blob" >/dev/null
      descriptors="$(mktemp "${tmp_dir}/descriptors.XXXXXX")"
      jq -er '.manifests[] | [.digest, .size, .mediaType] | @tsv' "$blob" >"$descriptors"
      while IFS=$'\t' read -r child_digest child_size child_media_type; do
        validate_descriptor "$child_digest" "$child_size" "$child_media_type"
      done <"$descriptors"
      ;;
    application/vnd.oci.image.manifest.v1+json|application/vnd.docker.distribution.manifest.v2+json)
      jq -e '.schemaVersion == 2 and (.layers | length > 0)' "$blob" >/dev/null
      config_blob="$(mktemp "${tmp_dir}/config.XXXXXX")"
      validate_blob \
        "$(jq -er '.config.digest' "$blob")" \
        "$(jq -er '.config.size' "$blob")" \
        "$config_blob"
      jq -e '
        type == "object" and
        (.config | type == "object") and
        (.config |
          if has("Env") then
            ((.Env | type) == "array" and all(.Env[]; type == "string"))
          else
            true
          end)
      ' "$config_blob" >/dev/null
      descriptors="$(mktemp "${tmp_dir}/descriptors.XXXXXX")"
      jq -er '.layers[] | [.digest, .size] | @tsv' "$blob" >"$descriptors"
      while IFS=$'\t' read -r layer_digest layer_size; do
        layer_blob="$(mktemp "${tmp_dir}/layer.XXXXXX")"
        validate_blob "$layer_digest" "$layer_size" "$layer_blob"
        tar -tf "$layer_blob" >/dev/null
      done <"$descriptors"
      ;;
    *)
      printf 'OCI descriptor uses an unsupported media type: %s\n' "$media_type" >&2
      exit 1
      ;;
  esac
}

layout_file="${tmp_dir}/oci-layout"
index_file="${tmp_dir}/index.json"
extract_entry oci-layout "$layout_file"
extract_entry index.json "$index_file"
jq -e '.imageLayoutVersion == "1.0.0"' "$layout_file" >/dev/null
jq -e '.schemaVersion == 2 and (.manifests | length > 0)' "$index_file" >/dev/null

root_descriptors="$(mktemp "${tmp_dir}/root-descriptors.XXXXXX")"
jq -er '.manifests[] | [.digest, .size, .mediaType] | @tsv' "$index_file" >"$root_descriptors"
while IFS=$'\t' read -r digest size media_type; do
  validate_descriptor "$digest" "$size" "$media_type"
done <"$root_descriptors"

printf 'OCI archive descriptors and tar structure are valid: %s\n' "$archive"
