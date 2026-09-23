#!/usr/bin/env bash
# Print the per-product manifest derived from a combined SHA256SUMS:
#   product-manifest.sh <product> <combined-SHA256SUMS>
# A line belongs to <product> when its filename starts with "<product>_"
# (archives, OCI archive, .spdx.json) or "<product>-" (per-image SBOM/scans).
# Lines are copied byte-for-byte in their original order. Shared files
# (release-manifest.json, trivy-db-*) match no product. Prints nothing and
# exits 1 when no line matches. Single source of truth for the assembly step
# (which writes the manifest) and the publish step (which requires the
# published manifest to be byte-identical to this output).
set -euo pipefail
(($# == 2)) || { printf 'usage: %s <product> <SHA256SUMS>\n' "$0" >&2; exit 2; }
[[ "$1" =~ ^[a-z][a-z0-9]*(-[a-z0-9]+)*$ ]] || { printf 'invalid product: %s\n' "$1" >&2; exit 2; }
test -f "$2" || { printf 'missing manifest: %s\n' "$2" >&2; exit 2; }
awk -v a="${1}_" -v b="${1}-" '
  { n = substr($0, index($0, "  ") + 2)
    if (index(n, a) == 1 || index(n, b) == 1) { print; found = 1 } }
  END { exit found ? 0 : 1 }' "$2"
