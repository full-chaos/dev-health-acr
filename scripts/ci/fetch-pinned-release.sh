#!/usr/bin/env bash
# Downloads one pinned GitHub release asset and checks its sha256. Run as
#   scripts/ci/fetch-pinned-release.sh OWNER/REPO TAG ASSET SHA256 OUT
# With GH_TOKEN (else GITHUB_TOKEN) set, the asset comes through the REST API with that token: an anonymous
# github.com/.../releases/download fetch can get a 504 in a GitHub incident and red the PR. The token goes to curl in a
# config on stdin only: never argv, never curl's environment, never the log. curl drops it on the cross-host redirect to
# the asset storage. With no token (a local run) the anonymous URL is used. Each request runs under scripts/ci/retry.sh
# (its tries and waits). retry.sh runs a command, not a shell function, so a request is this script run again in its
# one-request mode. A SHA256 that is not 64 hex characters, or a file that does not match it, fails and leaves no OUT
# file; a mismatch is never retried.
set +x
set -euo pipefail

if [ "${1:-}" = __get ]; then
  [ "$#" -eq 4 ] || exit 2
  token="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
  unset GH_TOKEN GITHUB_TOKEN
  printf 'header = "Authorization: Bearer %s"\nheader = "Accept: %s"\nheader = "X-GitHub-Api-Version: 2022-11-28"\n' \
    "${token}" "$4" | curl --config - --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
    --output "$3" "$2"
  exit
fi

if [ "$#" -ne 5 ]; then
  printf 'fetch-pinned-release: usage: fetch-pinned-release.sh OWNER/REPO TAG ASSET SHA256 OUT\n' >&2
  exit 2
fi
repo="$1" tag="$2" asset="$3" sha="$4" out="$5"
if ! printf '%s' "${sha}" | grep -qxE '[0-9a-f]{64}'; then
  printf 'fetch-pinned-release: %s: the pinned sha256 must be 64 lowercase hex characters\n' "${asset}" >&2
  exit 2
fi
self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
retry="$(dirname "${self}")/retry.sh"

rm -f "${out}"
part="${out}.part"
trap 'rm -f "${part}" "${part}.json"' EXIT

if [ -n "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]; then
  api="https://api.github.com/repos/${repo}/releases"
  "${retry}" "${self}" __get "${api}/tags/${tag}" "${part}.json" application/vnd.github+json
  id="$(jq -r --arg n "${asset}" '[.assets[] | select(.name == $n) | .id] | if length == 1 then .[0] else empty end' "${part}.json")"
  if [ -z "${id}" ]; then
    printf 'fetch-pinned-release: %s %s has no single asset named %s\n' "${repo}" "${tag}" "${asset}" >&2
    exit 1
  fi
  "${retry}" "${self}" __get "${api}/assets/${id}" "${part}" application/octet-stream
else
  printf 'fetch-pinned-release: no GH_TOKEN or GITHUB_TOKEN: anonymous download of %s\n' "${asset}" >&2
  "${retry}" curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 --output "${part}" \
    "https://github.com/${repo}/releases/download/${tag}/${asset}"
fi

if ! printf '%s  %s\n' "${sha}" "${part}" | sha256sum --check --strict --status; then
  printf 'fetch-pinned-release: %s does not match its pinned sha256\n' "${asset}" >&2
  exit 1
fi
mv "${part}" "${out}"
printf 'fetch-pinned-release: %s %s %s sha256 ok\n' "${repo}" "${tag}" "${asset}" >&2
