#!/usr/bin/env bash
set -euo pipefail

repo="full-chaos/dev-health-acr"
root="$(cd "$(dirname "$0")/../.." && pwd -P)"
source "$root/scripts/release/approval-receipt.sh"

approval_parse_options "$@" || { printf 'usage: verify-mcp-binary-approval.sh --approval-receipt RECEIPT --digest sha256:DIGEST [--dry-run] RELEASE_MANIFEST TAG\n' >&2; exit 1; }
((${#APPROVAL_ARGS[@]} == 2)) || exit 1
manifest="${APPROVAL_ARGS[0]}"
tag="${APPROVAL_ARGS[1]}"
version="${tag#v}"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]] || exit 1
[[ -f "$manifest" ]] || exit 1
target="mcp-binaries:${repo}:${tag}"

mcp_artifacts="$(jq -cSe --arg version "$version" '
  select(.schema_version == "release_manifest.v1" and .version == $version) |
  [.artifacts[] |
    select(.product == "acr-mcp") |
    {name, product, goos, goarch, sha256}] |
  sort_by(.name) |
  select(length == 5) |
  select(all(.[];
    (.name | type == "string") and
    (.sha256 | test("^[0-9a-f]{64}$")))) |
  select(([.[] | .goos + "/" + .goarch] | sort) ==
    ["darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"])
' "$manifest")"
if command -v sha256sum >/dev/null; then
  mcp_digest="sha256:$(printf '%s' "$mcp_artifacts" | sha256sum | awk '{print $1}')"
else
  mcp_digest="sha256:$(printf '%s' "$mcp_artifacts" | shasum -a 256 | awk '{print $1}')"
fi
test "$mcp_digest" = "$APPROVAL_DIGEST"
approval_verify "$APPROVAL_RECEIPT" approve_mcp_binaries "$repo" "$target" "$version" "$APPROVAL_DIGEST" || exit 1
if "$APPROVAL_DRY_RUN"; then
  printf 'dry-run approved: optional MCP binary approval verified without consuming the receipt\n'
else
  printf 'MCP binary approval verified: %s@%s\n' "$tag" "$APPROVAL_DIGEST"
fi
