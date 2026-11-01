#!/usr/bin/env bash
# Render-only contract of deploy/compose/acr-auth-workload.compose.yml (no daemon, no container).
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
base="$root/deploy/compose/acr.compose.yml"
overlay="$root/deploy/compose/acr-auth-workload.compose.yml"
stub="$root/scripts/deploy/testdata/auth-workload-root.compose.yml"
work="$(mktemp -d)"
trap 'rm -rf "${work:?}"' EXIT

for name in ACR_POSTGRES_ADMIN_PASSWORD_FILE ACR_RUNTIME_DB_PASSWORD_FILE ACR_MIGRATION_DB_PASSWORD_FILE ACR_RUNTIME_DSN_FILE \
  ACR_MIGRATION_DSN_FILE ACR_CLICKHOUSE_DSN_FILE ACR_EVIDENCE_ACTIVE_KID_FILE ACR_EVIDENCE_KEYS_FILE; do
  : >"$work/$name"
done
render() {
  # $1 = extra -f files (space separated names: base|overlay), rest = extra env assignments
  local files=(-f "$stub" -f "$base")
  local mode="$1"
  shift
  [[ "$mode" == overlay ]] && files+=(-f "$overlay")
  env -i PATH="$PATH" HOME="$work" DOCKER_HOST=unix:///nonexistent/docker.sock \
    POSTGRES_USER=u ACR_RUNTIME_DB_USER=r ACR_MIGRATION_DB_USER=m \
    ACR_IMAGE=registry.test/acr@sha256:1111111111111111111111111111111111111111111111111111111111111111 \
    ACR_POSTGRES_ADMIN_PASSWORD_FILE="$work/ACR_POSTGRES_ADMIN_PASSWORD_FILE" \
    ACR_RUNTIME_DB_PASSWORD_FILE="$work/ACR_RUNTIME_DB_PASSWORD_FILE" \
    ACR_MIGRATION_DB_PASSWORD_FILE="$work/ACR_MIGRATION_DB_PASSWORD_FILE" \
    ACR_RUNTIME_DSN_FILE="$work/ACR_RUNTIME_DSN_FILE" ACR_MIGRATION_DSN_FILE="$work/ACR_MIGRATION_DSN_FILE" \
    ACR_CLICKHOUSE_DSN_FILE="$work/ACR_CLICKHOUSE_DSN_FILE" \
    ACR_EVIDENCE_ACTIVE_KID_FILE="$work/ACR_EVIDENCE_ACTIVE_KID_FILE" ACR_EVIDENCE_KEYS_FILE="$work/ACR_EVIDENCE_KEYS_FILE" \
    "$@" docker compose --profile auth "${files[@]}" config --format json
}
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

issuer=https://auth.test.example
render base >"$work/base.json" || fail 'default render failed'
render overlay AUTH_TOKEN_ISSUER_URL="$issuer" >"$work/overlay.json" || fail 'overlay render failed'

# The default render has no workload client: no env, no volume, no network, no one-shot.
if grep -Eq 'ACR_AUTH_WORKLOAD|acr_workload_bootstrap|acr_auth_workload|acr-workload-' "$work/base.json"; then
  fail 'overlay content leaked into the default render'
fi
jq -e '.services["acr-api"].networks | keys == ["dev-health"]' "$work/base.json" >/dev/null || fail 'default acr-api networks changed'
jq -e '.services["auth-service"].networks | keys == ["auth_plane"]' "$work/base.json" >/dev/null || fail 'default auth-service networks changed'

# Overlay: the four env values and the read-only volume mount.
for pair in \
  'ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE=/run/workload/acr-api.bootstrap' \
  'ACR_AUTH_WORKLOAD_EXCHANGE_URL=http://auth-service:8095' \
  'ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP=true' \
  "ACR_AUTH_WORKLOAD_ISSUER=$issuer"; do
  key="${pair%%=*}"
  want="${pair#*=}"
  jq -e --arg k "$key" --arg w "$want" '.services["acr-api"].environment[$k] == $w' "$work/overlay.json" >/dev/null \
    || fail "acr-api $key is not $want"
done
jq -e '.services["acr-api"].environment.ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL == "service_token"' "$work/overlay.json" >/dev/null \
  || fail 'the overlay must keep the service token credential unless the flag is set'
render overlay AUTH_TOKEN_ISSUER_URL="$issuer" ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL=workload >"$work/overlay-workload.json" || fail 'overlay render with the flag failed'
jq -e '.services["acr-api"].environment.ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL == "workload"' "$work/overlay-workload.json" >/dev/null \
  || fail 'the flag must reach acr-api'
jq -e '.services["acr-api"].volumes | map(select(.target == "/run/workload")) | length == 1
  and .[0].type == "volume" and .[0].source == "acr_workload_bootstrap" and .[0].read_only == true' \
  "$work/overlay.json" >/dev/null || fail 'acr-api must mount the credential volume read-only at /run/workload'

# Networks: one new network, only auth-service and acr-api, and acr-api never joins auth_plane.
jq -e '.services["acr-api"].networks | has("acr_auth_workload") and (has("auth_plane") | not)' "$work/overlay.json" >/dev/null \
  || fail 'acr-api must join acr_auth_workload and must not join auth_plane'
jq -e '.services["auth-service"].networks | keys == ["acr_auth_workload","auth_plane"]' "$work/overlay.json" >/dev/null \
  || fail 'auth-service must gain only acr_auth_workload'
jq -e '[.services | to_entries[] | select(.value.networks | has("acr_auth_workload")) | .key] | sort == ["acr-api","auth-service"]' \
  "$work/overlay.json" >/dev/null || fail 'only acr-api and auth-service may join acr_auth_workload'
jq -e '.networks | keys == ["acr_auth_workload","auth_plane","dev-health"] or keys == ["acr_auth_workload","auth_plane","default","dev-health"]' \
  "$work/overlay.json" >/dev/null || fail 'overlay must add exactly one network'

# The credential is written into the volume by the bootstrap one-shot as acr-api, never copied from the host.
jq -e '.services["acr-workload-bootstrap"].command | index("acr-api") != null and index("-apply") != null' "$work/overlay.json" >/dev/null \
  || fail 'the bootstrap one-shot must create the acr-api credential'
jq -e '.services["acr-workload-bootstrap"].volumes | all(.source != null and .type == "volume")' "$work/overlay.json" >/dev/null \
  || fail 'the bootstrap one-shot must write to the volume only'
if jq -e '.services["acr-api"].secrets // [] | map(.source) | any(test("workload"))' "$work/overlay.json" >/dev/null; then
  fail 'no workload secret file may be mounted'
fi

# acr-api never depends on the bootstrap one-shot: a later `up` would run bootstrap-create again, and it refuses a live credential.
if jq -e '.services["acr-api"].depends_on // {} | has("acr-workload-bootstrap") or has("acr-workload-volume")' "$work/overlay.json" >/dev/null; then
  fail 'acr-api must not depend on the workload one-shots'
fi

# The issuer is required: no value and no default.
if render overlay >"$work/noissuer.json" 2>"$work/noissuer.err"; then fail 'a render without AUTH_TOKEN_ISSUER_URL must refuse'; fi
grep -Fq 'AUTH_TOKEN_ISSUER_URL is required' "$work/noissuer.err" || fail 'the refusal must name AUTH_TOKEN_ISSUER_URL'
if render overlay AUTH_TOKEN_ISSUER_URL= >"$work/emptyissuer.json" 2>/dev/null; then fail 'an empty AUTH_TOKEN_ISSUER_URL must refuse'; fi

printf 'compose auth workload overlay contract passed\n'
