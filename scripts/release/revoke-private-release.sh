#!/usr/bin/env bash
set -euo pipefail

repo="full-chaos/dev-health-acr"
root="$(cd "$(dirname "$0")/../.." && pwd -P)"
source "$root/scripts/release/approval-receipt.sh"
approval_parse_options "$@" || { printf 'usage: revoke-private-release.sh --approval-receipt RECEIPT --digest sha256:DIGEST [--dry-run] TAG INCIDENT_REFERENCE\n' >&2; exit 1; }
((${#APPROVAL_ARGS[@]} == 2)) || exit 1
tag="${APPROVAL_ARGS[0]}"
incident="${APPROVAL_ARGS[1]}"
version="${tag#v}"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]] || exit 1
approval_verify "$APPROVAL_RECEIPT" revoke_private_release "$repo" "github-release:$repo:$tag" "$version" "$APPROVAL_DIGEST" || exit 1
if "$APPROVAL_DRY_RUN"; then
  printf 'dry-run approved: release revocation remains blocked before GitHub access\n'
  exit 0
fi
actor="$(gh api user --jq .login)"
operators="$(gh variable get ACR_RELEASE_OPERATORS --repo "$repo")"
[[ ",$operators," == *",$actor,"* ]]
read -r -p "Type REVOKE $tag: " confirmation
test "$confirmation" = "REVOKE $tag"
assets=()
asset_list="$(gh release view "$tag" --repo "$repo" --json assets --jq '.assets[].name')"
while IFS= read -r asset; do
  [[ -n "$asset" ]] && assets+=("$asset")
done <<< "$asset_list"
if ((${#assets[@]})); then
  for asset in "${assets[@]}"; do
    gh release delete-asset "$tag" "$asset" --repo "$repo" --yes
  done
fi
gh release edit "$tag" --repo "$repo" --prerelease --latest=false --notes "REVOKED: do not download, install, or deploy this release. Incident/audit reference: $incident. All Release assets were removed. Credential revocation is a separate security operation; server version denylisting is a separate compatibility control."
