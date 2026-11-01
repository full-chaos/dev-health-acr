#!/usr/bin/env bash
set -euo pipefail

# Usage: check-vendored-ops-contract.sh <ops-contract-root> [vendored-dir]
#
# <ops-contract-root> is a checkout of the ops commit ci/ops-contract.pin
# names. The vendored copy of the ops-owned acr credential lifecycle event
# contract under contracts/auth/v1/ops-vendored must equal the same paths
# there, byte for byte, with no file added or missing on either side.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ops_root="${1:?usage: check-vendored-ops-contract.sh <ops-contract-root>}"
vendored="${2:-$repo_root/contracts/auth/v1/ops-vendored}"
ops_base="$ops_root/contracts/auth/v1"

schema_rel=jsonschema/acr-credential-lifecycle-event.v1.schema.json
examples_rel=examples/acr-credential-lifecycle-event

status=0
if [ ! -f "$ops_base/$schema_rel" ]; then
  printf 'the ops tree has no %s\n' "$schema_rel" >&2
  exit 1
fi
if [ ! -d "$ops_base/$examples_rel" ]; then
  printf 'the ops tree has no %s\n' "$examples_rel" >&2
  exit 1
fi
if ! diff -u "$ops_base/$schema_rel" "$vendored/$schema_rel" >&2; then
  printf 'vendored %s differs from the pinned ops copy\n' "$schema_rel" >&2
  status=1
fi
if ! diff -ru "$ops_base/$examples_rel" "$vendored/$examples_rel" >&2; then
  printf 'vendored %s differs from the pinned ops copy\n' "$examples_rel" >&2
  status=1
fi
if [ "$status" -ne 0 ]; then
  printf 'Re-vendor from the commit in ci/ops-contract.pin, or move the pin with the copy in one change.\n' >&2
fi
exit "$status"
