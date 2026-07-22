#!/usr/bin/env sh
set -eu

: "${ACR_LOCAL_STATE_DIR:=/var/lib/context-fabric}"
: "${ACR_LOCAL_ORG_SLUG:?ACR_LOCAL_ORG_SLUG is required}"
: "${ACR_OPS_TOKEN_FILE:?ACR_OPS_TOKEN_FILE is required}"

marker="$ACR_LOCAL_STATE_DIR/ops-bootstrap.complete"
org_file="$ACR_LOCAL_STATE_DIR/org-id"
if [ -s "$marker" ] && [ -s "$org_file" ] && [ -s "$ACR_OPS_TOKEN_FILE" ]; then
  exit 0
fi

output="$(dev-hops admin orgs create \
  --name 'Context Fabric local' \
  --slug "$ACR_LOCAL_ORG_SLUG" \
  --description 'Containerized Context Fabric local fixture' \
  --tier community)"
org_id="$(printf '%s\n' "$output" | sed -nE 's/.*id:[[:space:]]*([0-9a-fA-F-]{36}).*/\1/p' | head -1)"
case "$org_id" in
  ????????-????-????-????-????????????) ;;
  *) printf 'Ops organization provisioning did not return a UUID\n' >&2; exit 1 ;;
esac

dev-hops admin bundles assign-org \
  --org-id "$org_id" \
  --feature-key agent_context_runtime \
  --reason 'Context Fabric local Compose' \
  --expires-days 7 >/dev/null

token="$(dev-hops service-credentials create --service acr --scope entitlements:read)"
case "$token" in
  svc_acr_*) ;;
  *) printf 'Ops service credential had an invalid shape\n' >&2; exit 1 ;;
esac

umask 077
printf '%s' "$org_id" >"$org_file"
printf '%s' "$token" >"$ACR_OPS_TOKEN_FILE"
printf 'ok\n' >"$marker"
