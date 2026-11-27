#!/usr/bin/env bash
set -euo pipefail

source_root="${1:?usage: create-context.sh <source-root> <destination>}"
destination="${2:?usage: create-context.sh <source-root> <destination>}"
file_list="$(mktemp)"
trap 'rm -f "$file_list"' EXIT

test -d "$source_root" || { printf 'container source root is not a directory\n' >&2; exit 1; }
mkdir -p "$destination"
test -z "$(find "$destination" -mindepth 1 -maxdepth 1 -print -quit)" || {
  printf 'container context destination must be empty\n' >&2
  exit 1
}

copy_source() {
  local source="$1"
  local relative="${source#"${source_root}/"}"
  if [[ ! -f "$source" || -L "$source" ]]; then
    printf 'container source must be a regular non-symlink file: %s\n' "$relative" >&2
    exit 1
  fi
  mkdir -p "${destination}/$(dirname "$relative")"
  cp -p "$source" "${destination}/${relative}"
}

copy_source "${source_root}/Dockerfile"
copy_source "${source_root}/go.mod"
copy_source "${source_root}/go.sum"

find "${source_root}/cmd" -type f -name '*.go' -print0 >"$file_list"
find "${source_root}/internal" -type f \( -name '*.go' -o -name '*.json' \) -print0 >>"$file_list"
# The vendored MCP skills acr-mcp embeds (internal/mcp/skills_vendored.go);
# a tree without them fails the Go build on its embed, not here.
if [[ -d "${source_root}/internal/mcp/skills" ]]; then
  find "${source_root}/internal/mcp/skills" -type f ! -name '*.go' ! -name '*.json' -print0 >>"$file_list"
fi
# The MCP App view acr-mcp embeds (internal/mcp/app_view.go).
if [[ -d "${source_root}/internal/mcp/appview" ]]; then
  find "${source_root}/internal/mcp/appview" -type f -name '*.html' -print0 >>"$file_list"
fi
find "${source_root}/migrations" -type f \( -name '*.go' -o -name '*.sql' \) -print0 >>"$file_list"

while IFS= read -r -d '' source; do
  copy_source "$source"
done <"$file_list"
