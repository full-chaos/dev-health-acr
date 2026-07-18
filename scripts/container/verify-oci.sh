#!/usr/bin/env bash
set -euo pipefail

require() { command -v "$1" >/dev/null || { printf '%s is required\n' "$1" >&2; exit 1; }; }
require jq
require tar
(($# > 0)) || { printf 'at least one OCI archive is required\n' >&2; exit 1; }

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

binary_for_archive() {
  case "$(basename "$1")" in
    acr-api.tar) printf '/usr/local/bin/acr-api\n' ;;
    acr-mcp.tar) printf '/usr/local/bin/acr-mcp\n' ;;
    *) printf 'cannot determine target binary from OCI archive: %s\n' "$1" >&2; exit 2 ;;
  esac
}

expected_machine() {
  case "$1" in
    amd64) printf '3e00\n' ;;
    arm64) printf 'b700\n' ;;
    *) printf 'unsupported OCI architecture: %s\n' "$1" >&2; exit 2 ;;
  esac
}

elf_machine() {
  od -An -tx1 -j18 -N2 "$1" | tr -d ' \n'
}

# fetch_descriptor extracts the blob referenced by a "sha256:<hex>"
# digest to a fresh temp file, and recursively validates that the blob
# actually exists in the archive and that its real size and sha256
# match the descriptor's own declared "size"/"digest" fields -- rather
# than trusting the index/manifest/config JSON's claims about content it
# never itself confirmed. Prints the dump file path on success.
fetch_descriptor() {
  local archive="$1"
  local digest="$2"
  local want_size="$3"
  local label="$4"
  local blob_path="blobs/sha256/${digest#sha256:}"
  local dump
  dump="$(mktemp "${tmp_dir}/blob.XXXXXX")"
  if ! tar -xOf "$archive" "$blob_path" >"$dump" 2>/dev/null; then
    printf '%s: OCI archive is missing descriptor blob %s: %s\n' "$label" "$digest" "$archive" >&2
    exit 1
  fi
  local actual_size
  actual_size="$(wc -c <"$dump" | tr -d ' ')"
  if [[ -n "$want_size" && "$want_size" != "null" && "$actual_size" != "$want_size" ]]; then
    printf '%s: descriptor %s size mismatch in %s: want %s got %s\n' "$label" "$digest" "$archive" "$want_size" "$actual_size" >&2
    exit 1
  fi
  local actual_sha
  actual_sha="$(shasum -a 256 "$dump" | awk '{print $1}')"
  if [[ "sha256:${actual_sha}" != "$digest" ]]; then
    printf '%s: descriptor %s sha256 mismatch in %s: computed sha256:%s\n' "$label" "$digest" "$archive" "$actual_sha" >&2
    exit 1
  fi
  printf '%s\n' "$dump"
}

layer_deletes_target() {
  local listing="$1"
  local target="$2"
  local path="$target"

  while [[ "$path" != "." && "$path" != "/" ]]; do
    local parent base prefix
    parent="$(dirname "$path")"
    base="$(basename "$path")"
    prefix=""
    if [[ "$parent" != "." ]]; then
      prefix="${parent}/"
    fi
    if grep -qxF "${prefix}.wh.${base}" "$listing" || grep -qxF "${prefix}.wh..wh..opq" "$listing"; then
      return 0
    fi
    path="$parent"
  done
  return 1
}

# extract_binary implements OCI-aware, whiteout-respecting extraction:
# every declared layer is descriptor-verified (fetch_descriptor above),
# then layers are walked in the manifest's own bottom-to-top order and
# the target path's *last* known state wins -- an OCI whiteout marker
# (".wh.<name>") for the target in a later layer discards any earlier
# match, and a later real entry for the same path replaces an earlier
# one -- rather than naively accepting the first layer that happens to
# contain a same-named entry, which could report a file as present that
# a later layer actually deleted from the final merged filesystem.
extract_binary() {
  local archive="$1"
  local manifest_dump="$2"
  local binary="$3"
  local output="$4"
  local target="${binary#/}"
  local winning_dump=""
  local layer_digest layer_size descriptors

  descriptors="$(mktemp "${tmp_dir}/layer-descriptors.XXXXXX")"
  jq -er '.layers[] | [.digest, .size] | @tsv' <<<"$manifest_dump" >"$descriptors"

  while IFS=$'\t' read -r layer_digest layer_size; do
    local layer_dump listing
    layer_dump="$(fetch_descriptor "$archive" "$layer_digest" "$layer_size" layer)"
    listing="$(mktemp "${tmp_dir}/listing.XXXXXX")"
    tar -tf "$layer_dump" >"$listing" 2>/dev/null
    if layer_deletes_target "$listing" "$target"; then
      winning_dump=""
    fi
    if grep -qxF "$target" "$listing"; then
      winning_dump="$layer_dump"
    fi
  done <"$descriptors"

  test -n "$winning_dump" || {
    printf 'OCI image does not contain %s in the final merged filesystem: %s\n' "$binary" "$archive" >&2
    exit 1
  }
  tar -xO -f "$winning_dump" "$target" >"$output"
}

# assert_config_descriptor recursively validates the referenced config
# blob (existence/size/sha256 via fetch_descriptor) and then checks the
# image config's OS/architecture, non-root numeric user, entrypoint, and
# absence of any secret-shaped environment variable.
assert_config_descriptor() {
  local archive="$1"
  local manifest_dump="$2"
  local architecture="$3"
  local binary="$4"
  local config_digest config_size config_dump config_json
  config_digest="$(jq -r '.config.digest' <<<"$manifest_dump")"
  config_size="$(jq -r '.config.size' <<<"$manifest_dump")"
  config_dump="$(fetch_descriptor "$archive" "$config_digest" "$config_size" config)"
  config_json="$(cat "$config_dump")"
  jq -e --arg arch "$architecture" --arg entrypoint "$binary" '
    .os == "linux" and
    .architecture == $arch and
    .config.User == "65532:65532" and
    .config.Entrypoint == [$entrypoint] and
    (.config |
      if has("Env") then
        ((.Env | type) == "array" and
          all(.Env[];
            if type == "string" then
              (test("(TOKEN|PASSWORD|SECRET|DSN)="; "i") | not)
            else
              false
            end))
      else
        true
      end)
  ' <<<"$config_json" >/dev/null || {
    printf 'OCI config for %s (%s) failed validation: %s\n' "$archive" "$architecture" "$binary" >&2
    exit 1
  }
}

for archive in "$@"; do
  test -f "$archive" || { printf 'missing OCI archive: %s\n' "$archive" >&2; exit 1; }
  binary="$(binary_for_archive "$archive")"

  root_index_dump="$(mktemp "${tmp_dir}/root-index.XXXXXX")"
  tar -xOf "$archive" index.json >"$root_index_dump" 2>/dev/null
  root_index="$(cat "$root_index_dump")"
  image_index_digest="$(jq -er '.manifests | select(length == 1) | .[0].digest' <<<"$root_index")"
  image_index_size="$(jq -er '.manifests[0].size' <<<"$root_index")"
  index_dump="$(fetch_descriptor "$archive" "$image_index_digest" "$image_index_size" "image index")"
  index="$(cat "$index_dump")"
  platforms="$(jq -r '.manifests[] | select(.platform.os == "linux") | .platform.architecture' <<<"$index" | sort)"
  test "$platforms" = $'amd64\narm64' || {
    printf 'OCI archive must contain exactly linux/amd64 and linux/arm64 manifests: %s\n' "$archive" >&2
    exit 1
  }

  manifest_descriptors="$(mktemp "${tmp_dir}/manifest-descriptors.XXXXXX")"
  jq -er '.manifests[] | [.platform.os, .platform.architecture, .digest, .size] | @tsv' <<<"$index" >"$manifest_descriptors"
  while IFS=$'\t' read -r os architecture manifest_digest manifest_size; do
    test "$os" = linux || { printf 'OCI manifest is not linux: %s\n' "$archive" >&2; exit 1; }
    expected="$(expected_machine "$architecture")"
    manifest_dump_file="$(fetch_descriptor "$archive" "$manifest_digest" "$manifest_size" manifest)"
    manifest_dump="$(cat "$manifest_dump_file")"

    assert_config_descriptor "$archive" "$manifest_dump" "$architecture" "$binary"

    extracted="${tmp_dir}/$(basename "$archive")-${architecture}"
    extract_binary "$archive" "$manifest_dump" "$binary" "$extracted"
    test "$(LC_ALL=C od -An -tx1 -N4 "$extracted" | tr -d ' \n')" = 7f454c46 || {
      printf 'target binary is not ELF: %s (%s)\n' "$archive" "$architecture" >&2
      exit 1
    }
    test "$(elf_machine "$extracted")" = "$expected" || {
      printf 'ELF machine does not match OCI architecture: %s (%s)\n' "$archive" "$architecture" >&2
      exit 1
    }
  done <"$manifest_descriptors"
done

printf 'OCI archives contain linux/amd64 and linux/arm64 images with matching, descriptor-verified ELF binaries and config\n'
