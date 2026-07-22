#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
state_dir="${CONTEXT_FABRIC_LOCAL_STATE_DIR:-$repo_root/.local/context-fabric}"
env_file="$state_dir/compose.env"
org_file="$state_dir/runtime/org-id"
token_file="$state_dir/secrets/acr-client-token"

[[ -f "$env_file" && -s "$org_file" ]] || {
  printf 'Context Fabric is not bootstrapped under %s\n' "$state_dir" >&2
  exit 1
}

read_env_value() {
  local key="$1" line
  line="$(grep -E "^${key}=" "$env_file" | tail -1)"
  [[ -n "$line" ]] || return 1
  printf '%s' "${line#*=}"
}

repository="$(read_env_value ACR_LOCAL_REPOSITORY)"
org_id="$(cat "$org_file")"
case "$org_id" in
  ????????-????-????-????-????????????) ;;
  *) printf 'invalid local organization ID\n' >&2; exit 1 ;;
esac

cd "$repo_root"
token="$(docker compose --env-file "$env_file" run --rm --no-deps acr-credentials \
  credentials create \
  --org-id "$org_id" \
  --repository-scope "$repository" \
  --scope context:read,evidence:read \
  --name local-compose \
  --actor local-compose)"
case "$token" in
  fcacr_*) ;;
  *) printf 'ACR credential command returned an invalid token shape\n' >&2; exit 1 ;;
esac

umask 077
printf '%s' "$token" >"$token_file"
printf 'ACR client credential written to %s (mode 0600)\n' "$token_file"
printf 'Next: source %q and run acr-mcp doctor --live\n' "$state_dir/client.env"
