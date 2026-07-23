#!/usr/bin/env bash
set -euo pipefail

repo="full-chaos/dev-health-acr"
[[ $# -eq 2 ]] || { printf 'usage: revoke-private-release.sh TAG INCIDENT_REFERENCE\n' >&2; exit 1; }
tag="$1"
incident="$2"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(dev|beta)\.(1|[1-9][0-9]*))?$ ]] || exit 1
[[ "$incident" =~ ^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$ ]] || exit 1
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
