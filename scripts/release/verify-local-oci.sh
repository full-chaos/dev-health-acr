#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
archive_dir="${ACR_LOCAL_OCI_DIR:-$root/.tmp/container-oci}"
archives=("$archive_dir/acr-api.tar" "$archive_dir/acr-mcp.tar")

if [[ ! -f "${archives[0]}" || ! -f "${archives[1]}" ]]; then
  printf 'skip: local OCI archives are not present at %s\n' "$archive_dir"
  exit 0
fi

bash "$root/scripts/container/verify-oci.sh" "${archives[@]}"
