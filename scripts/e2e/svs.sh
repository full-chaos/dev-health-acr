#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck disable=SC1091
source "${SCRIPT_DIR}/compose.sh"

# allow: SIZE_OK — one trap owns the real API, MCP, and browser acceptance lifecycle.

WEB_ROOT=""
COMPOSE_FILE=""
OVERLAY_FILE=""
PROJECT=""
SCENARIO="happy"
WEB_PORT=""
EVIDENCE_DIR=""
WEB_EMAIL=""
WEB_PASSWORD=""
WEB_AUTH_SECRET=""
TASK_GOAL="Verify CHAOS-2914 ACR SVS vertical slice"
TASK_REPOSITORY="acme/live-e2e"
TASK_BRANCH="main"
TASK_REFERENCE="CHAOS-2914"

usage() {
  printf 'usage: %s --web-root <dev-health-web> --compose <root-compose.yml> --overlay <acr.compose.yml> --project <acr-svs-name> [--scenario happy|foreign-repo|false-entitlement|bad-credential|malformed-evidence|assertion-confusion|default-writeback]\n' "$0" >&2
}

svs_die() { printf '[acr-svs] FAIL: %s\n' "$*" >&2; exit 1; }
svs_note() { printf '[acr-svs] %s\n' "$*" >&2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --web-root) WEB_ROOT="${2:-}"; shift 2 ;;
    --compose) COMPOSE_FILE="${2:-}"; shift 2 ;;
    --overlay) OVERLAY_FILE="${2:-}"; shift 2 ;;
    --project) PROJECT="${2:-}"; shift 2 ;;
    --scenario) SCENARIO="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

case "$SCENARIO" in happy|foreign-repo|false-entitlement|bad-credential|malformed-evidence|assertion-confusion|default-writeback) ;; *) usage; exit 2 ;; esac
[[ -d "$WEB_ROOT" && -f "$WEB_ROOT/package.json" && -f "$COMPOSE_FILE" && -f "$OVERLAY_FILE" ]] || { usage; exit 2; }
[[ "$PROJECT" =~ ^acr-svs-[a-z0-9][a-z0-9-]{2,40}$ ]] || svs_die 'project must be an isolated acr-svs-* name'
[[ "$PROJECT" != "dev-health" && "$PROJECT" != "default" ]] || svs_die 'refusing the operator default Compose project'
for tool in docker openssl curl git jq node python3; do command -v "$tool" >/dev/null || svs_die "$tool is required"; done
[[ -d "$WEB_ROOT/node_modules/@playwright/test" ]] || svs_die 'web dependencies with @playwright/test are required'

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

export CONTAINER_ALLOW_DIRTY="${CONTAINER_ALLOW_DIRTY:-1}"

write_web_assertion_material() {
  local key jwks public
  mkdir -p "$STATE/web"
  key="$STATE/web/web-assertion.key"
  jwks="$STATE/web/web-assertions.jwks.json"
  openssl genpkey -algorithm ED25519 -out "$key"
  chmod 600 "$key"
  public="$(openssl pkey -in "$key" -pubout -outform DER | python3 -c 'import base64,json,sys; print(base64.urlsafe_b64encode(sys.stdin.buffer.read()[-32:]).rstrip(b"=").decode())')"
  printf '{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"acr-svs-web","use":"sig","alg":"EdDSA","x":"%s"}]}\n' "$public" > "$jwks"
  chmod 600 "$jwks"
}

render_svs_override() {
  WEB_PORT="$(free_port)"
  WEB_EMAIL="admin@test.com"
  WEB_PASSWORD="default1234"
  WEB_AUTH_SECRET="$(random_secret)"
  cat > "$STATE/svs.override.yml" <<EOF
services:
  acr-api:
    environment:
      ACR_WEB_ASSERTION_ISSUER: dev-health-web
      ACR_WEB_ASSERTION_AUDIENCE: dev-health-acr
      ACR_WEB_ASSERTION_JWKS_FILE: /run/acr-e2e/web-assertions.jwks.json
    volumes:
      - ${STATE}/web/web-assertions.jwks.json:/run/acr-e2e/web-assertions.jwks.json:ro
  bugsink:
    container_name: ${PROJECT}-bugsink
    ports: []
    restart: "no"
  web-svs:
    build:
      context: ${WEB_ROOT}
      dockerfile: Dockerfile
      target: runner
    ports: ["127.0.0.1:${WEB_PORT}:3000"]
    environment:
      AUTH_SECRET: ${WEB_AUTH_SECRET}
      AUTH_URL: http://127.0.0.1:${WEB_PORT}
      BACKEND_URL: http://api:8000
      ACR_API_ORIGIN: https://acr-tls-proxy:8443
      ACR_WEB_ASSERTION_AUDIENCE: dev-health-acr
      ACR_WEB_ASSERTION_ISSUER: dev-health-web
      ACR_WEB_ASSERTION_KEY_FILE: /run/acr-e2e/web-assertion.key
      ACR_WEB_ASSERTION_KID: acr-svs-web
      NODE_EXTRA_CA_CERTS: /run/acr-e2e/ca.crt
      NEXT_PUBLIC_DEV_HEALTH_TEST_MODE: "false"
    volumes:
      - ${STATE}/web/web-assertion.key:/run/acr-e2e/web-assertion.key:ro
      - ${STATE}/pki/ca.crt:/run/acr-e2e/ca.crt:ro
    depends_on:
      api: { condition: service_healthy }
      bugsink: { condition: service_started }
      valkey: { condition: service_healthy }
    networks: [dev-health]
EOF
  if [[ "$SCENARIO" != 'happy' ]]; then
    cat >> "$STATE/svs.override.yml" <<'EOF'
  migrate:
    entrypoint: ["sh", "-c", "python -m dev_health_ops.cli migrate postgres"]
EOF
  fi
}

enable_cross_surface_request_id() {
  cat > "$STATE/nginx-api.conf" <<'EOF'
events {}
http {
  server {
    listen 8443 ssl;
    ssl_certificate /run/pki/acr.crt;
    ssl_certificate_key /run/pki/acr.key;
    location = /api/v1/agent-context/context-packets {
      proxy_set_header X-Request-ID req_00000000000000000000000000002914;
      proxy_pass http://acr-api:8080;
    }
    location / {
      proxy_pass http://acr-api:8080;
    }
  }
}
EOF
  compose up -d --force-recreate --no-deps acr-tls-proxy >/dev/null
  wait_https_ready
}

wait_web_ready() {
  local attempts=0
  until curl --fail --silent --show-error --noproxy '*' "http://127.0.0.1:${WEB_PORT}/health" >/dev/null; do
    attempts=$((attempts + 1))
    if [[ "$attempts" -ge 90 ]]; then
      svs_die 'web readiness timed out'
    fi
    sleep 2
  done
}

bootstrap_web_user() {
  compose exec -T api dev-hops admin users create --email "$WEB_EMAIL" --password "$WEB_PASSWORD" --full-name 'ACR SVS Canonical Account' >/dev/null
  compose exec -T api dev-hops admin users update --email "$WEB_EMAIL" --verified --org "$(<"$STATE/org-id")" --role owner >/dev/null
}

assert_canonical_account_login() {
  compose exec -T -e "SVS_WEB_EMAIL=$WEB_EMAIL" -e "SVS_WEB_PASSWORD=$WEB_PASSWORD" web-svs node -e '
const email = process.env.SVS_WEB_EMAIL;
const password = process.env.SVS_WEB_PASSWORD;
void (async () => {
  const response = await fetch("http://api:8000/api/v1/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email, password }),
  });
  const payload = await response.json();
  if (!response.ok || payload?.user?.email !== email || typeof payload?.access_token !== "string") {
    console.error(JSON.stringify({ status: response.status, login_status: payload?.status ?? null }));
    process.exit(1);
  }
})().catch(() => process.exit(1));
' || svs_die 'canonical account did not authenticate against the isolated Ops API'
}

write_context_request() {
  jq -cn --arg goal "$TASK_GOAL" --arg repository "$TASK_REPOSITORY" --arg branch "$TASK_BRANCH" --arg task "$TASK_REFERENCE" '{schema_version:"context_packet_request.v1",request_id:"svs-request",goal:$goal,repository:{slug:$repository},scope:{branch:$branch,task_ref:$task},options:{max_items:10,max_output_tokens:500,max_serialized_bytes:8192},client:{name:"acr-svs",version:"1.0.0",sidecar_version:"1.0.0"}}' > "$STATE/request.json"
}

capture_api_surface() {
  local token evidence_id
  token="$(<"$STATE/secrets/acr-rotated-token")"
  curl --fail --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
    -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \
    --data-binary @"$STATE/request.json" "https://localhost:${PORT}/api/v1/agent-context/context-packets" > "$STATE/api-packet.json"
  jq -e '.schema_version == "context_packet.v1" and (.context_packet_id | type == "string")' "$STATE/api-packet.json" >/dev/null || svs_die 'API packet contract mismatch'
  evidence_id="$(jq -r '[.. | objects | .evidence_ref_ids? | arrays | .[] | select(type == "string")][0] // empty' "$STATE/api-packet.json")"
  [[ -n "$evidence_id" ]] || svs_die 'API packet did not expose evidence'
  printf '%s' "$evidence_id" > "$STATE/evidence-id"
  curl --fail --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
    -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" \
    "https://localhost:${PORT}/api/v1/agent-context/evidence/${evidence_id}" > "$STATE/api-evidence.json"
  jq -e '.schema_version == "expanded_evidence.v1"' "$STATE/api-evidence.json" >/dev/null || svs_die 'API evidence contract mismatch'
  curl --fail --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
    -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" \
    "https://localhost:${PORT}/api/v1/agent-context/capabilities" > "$STATE/api-capabilities.json"
  jq -e '.permissions.episode_write == false and ([.enabled_tools[]] | index("record_episode") | not)' "$STATE/api-capabilities.json" >/dev/null || svs_die 'writeback was enabled by default'
}

clear_packet_snapshot() {
  local packet_file="$1" packet_id
  packet_id="$(jq -r '.context_packet_id // .result.structuredContent.structured.context_packet_id // empty' "$packet_file")"
  [[ "$packet_id" =~ ^pkt_[0-9a-f]{24}$ ]] || svs_die 'packet ID was not safe for isolated snapshot reset'
  compose exec -T -e "PGPASSWORD=$(<"$STATE/secrets/runtime-password")" postgres \
    psql -h postgres -U "$ACR_RUNTIME_DB_USER" -d "$ACR_DB_NAME" -v ON_ERROR_STOP=1 \
    -c "DELETE FROM acr.context_packet_snapshots WHERE context_packet_id = '${packet_id}'" >/dev/null
}

mcp_stop() {
  local pid="$1" input="$2" output="$3"
  exec {input}>&- || true
  exec {output}<&- || true
  if kill -0 "$pid" 2>/dev/null; then kill -INT "$pid" 2>/dev/null || true; fi
  wait "$pid" 2>/dev/null || true
}

capture_mcp_surface() {
  local token mcp_pid mcp_input mcp_output context_response evidence_response
  token="$(<"$STATE/secrets/acr-rotated-token")"
  coproc SVS_MCP { ACR_API_URL="https://localhost:${PORT}" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$STATE/pki/ca.crt" ACR_SIDECAR_VERSION=1.0.0 ACR_SIDECAR_CLIENT_VERSION=1.0.0 "$STATE/acr-mcp" serve; }
  mcp_pid="$SVS_MCP_PID"
  mcp_input="${SVS_MCP[1]}"
  mcp_output="${SVS_MCP[0]}"
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"acr-svs","version":"1.0.0"}}}' >&"$mcp_input"
  if ! IFS= read -r -t 30 response <&"$mcp_output"; then mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"; svs_die 'MCP initialize timed out'; fi
  printf '%s\n' "$response" > "$STATE/mcp-initialize.json"
  jq -e '(.result.protocolVersion | type) == "string"' "$STATE/mcp-initialize.json" >/dev/null || { mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"; svs_die 'MCP initialize contract mismatch'; }
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' >&"$mcp_input"
  jq -cn --arg goal "$TASK_GOAL" --arg repository "$TASK_REPOSITORY" --arg branch "$TASK_BRANCH" --arg task "$TASK_REFERENCE" '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:"context_for_task",arguments:{goal:$goal,repository:{slug:$repository},scope:{branch:$branch,task_ref:$task}}}}' >&"$mcp_input"
  if ! IFS= read -r -t 30 context_response <&"$mcp_output"; then mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"; svs_die 'MCP context_for_task timed out'; fi
  printf '%s\n' "$context_response" > "$STATE/mcp-packet.json"
  jq -e '(.result.isError // false) == false and .result.structuredContent.schema_version == "mcp_context_for_task_response.v1"' "$STATE/mcp-packet.json" >/dev/null || { mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"; svs_die 'MCP context packet contract mismatch'; }
  jq -cn --arg evidence "$(<"$STATE/evidence-id")" '{jsonrpc:"2.0",id:3,method:"tools/call",params:{name:"source_evidence",arguments:{evidence_ref_id:$evidence}}}' >&"$mcp_input"
  if ! IFS= read -r -t 30 evidence_response <&"$mcp_output"; then mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"; svs_die 'MCP source_evidence timed out'; fi
  printf '%s\n' "$evidence_response" > "$STATE/mcp-evidence.json"
  jq -e '(.result.isError // false) == false and .result.structuredContent.schema_version == "mcp_source_evidence_response.v1"' "$STATE/mcp-evidence.json" >/dev/null || { mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"; svs_die 'MCP evidence contract mismatch'; }
  printf '%s\n' '{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}' >&"$mcp_input"
  if ! IFS= read -r -t 30 response <&"$mcp_output"; then mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"; svs_die 'MCP tools/list timed out'; fi
  printf '%s\n' "$response" > "$STATE/mcp-tools.json"
  jq -e '([.result.tools[].name] | sort) == ["context_for_task","source_evidence"]' "$STATE/mcp-tools.json" >/dev/null || { mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"; svs_die 'MCP exposed writeback by default'; }
  mcp_stop "$mcp_pid" "$mcp_input" "$mcp_output"
}

capture_browser_surface() {
  SVS_WEB_URL="http://127.0.0.1:${WEB_PORT}" \
  SVS_WEB_EMAIL="$WEB_EMAIL" \
  SVS_WEB_PASSWORD="$WEB_PASSWORD" \
  SVS_GOAL="$TASK_GOAL" \
  SVS_REPOSITORY="$TASK_REPOSITORY" \
  SVS_BRANCH="$TASK_BRANCH" \
  SVS_TASK_REFERENCE="$TASK_REFERENCE" \
  SVS_BROWSER_PACKET="$STATE/browser-packet.json" \
  SVS_BROWSER_EVIDENCE="$STATE/browser-evidence.json" \
  SVS_BROWSER_SCREENSHOT="$STATE/canonical-account-context-packet.png" \
  SVS_PLAYWRIGHT_MODULE="$WEB_ROOT/node_modules/@playwright/test/index.js" \
  node "$SCRIPT_DIR/svs-browser.mjs" || {
    compose logs --no-color web-svs 2>&1 | redact_log >&2 || true
    return 1
  }
}

assert_cross_surface_agreement() {
  jq -e -s '
    .[0] as $api | .[1].result.structuredContent.structured as $mcp | .[2] as $browser |
    ($api.context_packet_id == $mcp.context_packet_id and $api.context_packet_id == $browser.context_packet_id) and
    ($api.goal == $mcp.goal and $api.goal == $browser.goal) and
    ($api.repository.slug == $mcp.repository.slug and $api.repository.slug == $browser.repository.slug) and
    ([ $api | .. | objects | .evidence_ref_ids? | arrays | .[] ] | sort) == ([ $mcp | .. | objects | .evidence_ref_ids? | arrays | .[] ] | sort) and
    ([ $api | .. | objects | .evidence_ref_ids? | arrays | .[] ] | sort) == ([ $browser | .. | objects | .evidence_ref_ids? | arrays | .[] ] | sort)
  ' "$STATE/api-packet.json" "$STATE/mcp-packet.json" "$STATE/browser-packet.json" >/dev/null || svs_die 'packet IDs or semantics diverged across API, MCP, and browser'
  jq -e -s '
    .[0] as $api | .[1].result.structuredContent.structured as $mcp | .[2] as $browser |
    ($api.evidence_ref_id == $mcp.evidence_ref_id and $api.evidence_ref_id == $browser.evidence_ref_id) and
    ($api.repository.slug == $mcp.repository.slug and $api.repository.slug == $browser.repository.slug)
  ' "$STATE/api-evidence.json" "$STATE/mcp-evidence.json" "$STATE/browser-evidence.json" >/dev/null || svs_die 'evidence IDs or semantics diverged across API, MCP, and browser'
}

preserve_evidence() {
  EVIDENCE_DIR="$REPO_ROOT/.omo/evidence/task-23-acr-project-completion/$PROJECT"
  mkdir -p "$EVIDENCE_DIR"
  cp "$STATE"/api-*.json "$STATE"/mcp-*.json "$STATE"/browser-*.json "$STATE"/canonical-account-context-packet.png "$EVIDENCE_DIR/"
  jq -n --arg project "$PROJECT" --arg scenario "$SCENARIO" --arg api_packet "$(jq -r '.context_packet_id' "$STATE/api-packet.json")" --arg evidence "$(<"$STATE/evidence-id")" '{project:$project,scenario:$scenario,packet_id:$api_packet,evidence_ref_id:$evidence,writeback:"disabled"}' > "$EVIDENCE_DIR/manifest.json"
}

acr_table_count() {
  local table
  case "$1" in
    snapshots) table='acr.context_packet_snapshots' ;;
    episodes) table='acr.agent_episodes' ;;
    *) svs_die 'unknown ACR persistence probe' ;;
  esac
  compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres \
    psql -h postgres -U "$POSTGRES_USER" -d "$ACR_DB_NAME" -At -v ON_ERROR_STOP=1 \
    -c "SELECT count(*) FROM ${table}"
}

assert_no_forbidden_persistence() {
  local snapshots_before="$1" episodes_before="$2" snapshots_after episodes_after
  snapshots_after="$(acr_table_count snapshots)"
  episodes_after="$(acr_table_count episodes)"
  [[ "$snapshots_after" == "$snapshots_before" ]] || svs_die "${SCENARIO} persisted a context packet despite denial"
  [[ "$episodes_after" == "$episodes_before" ]] || svs_die "${SCENARIO} persisted an agent episode despite denial"
}

remove_runtime_entitlement() {
  local remaining
  compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres \
    psql -h postgres -U "$POSTGRES_USER" -d devhealth -v ON_ERROR_STOP=1 \
    -c "DELETE FROM org_feature_overrides USING feature_flags WHERE org_feature_overrides.feature_id = feature_flags.id AND org_feature_overrides.org_id = '$(<"$STATE/org-id")' AND feature_flags.key = 'agent_context_runtime'" >/dev/null
  remaining="$(compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres \
    psql -h postgres -U "$POSTGRES_USER" -d devhealth -At -v ON_ERROR_STOP=1 \
    -c "SELECT count(*) FROM org_feature_overrides JOIN feature_flags ON feature_flags.id = org_feature_overrides.feature_id WHERE org_feature_overrides.org_id = '$(<"$STATE/org-id")' AND feature_flags.key = 'agent_context_runtime'")"
  [[ "$remaining" == '0' ]] || svs_die 'failed to remove the isolated runtime entitlement override'
  compose exec -T valkey valkey-cli FLUSHDB >/dev/null
}

bootstrap_failure_ops() {
  local output org_id token db
  compose up -d postgres clickhouse valkey pgbouncer mailpit migrate api acr-ops-tls >/dev/null
  if ! output="$(compose exec -T api dev-hops admin orgs create --name "${PROJECT} SVS" --slug "$PROJECT" --description 'isolated SVS control plane' --tier community)"; then
    printf '%s\n' "$output" >&2
    svs_die 'Ops organization provisioning failed'
  fi
  org_id="$(printf '%s\n' "$output" | sed -nE 's/.*id:[[:space:]]*([0-9a-fA-F-]{36}).*/\1/p')"
  [[ "$org_id" =~ ^[0-9a-fA-F-]{36}$ ]] || svs_die 'Ops organization provisioning did not return an ID'
  compose exec -T api dev-hops admin bundles assign-org --org-id "$org_id" --feature-key agent_context_runtime --reason 'isolated SVS control plane' --expires-days 1 >/dev/null
  token="$(compose exec -T api dev-hops service-credentials create --service acr --scope entitlements:read)"
  [[ "$token" == svc_acr_* ]] || svs_die 'Ops credential provisioning returned an invalid token shape'
  write_secret "$STATE/secrets/ops-token" "$token"
  printf '%s' "$org_id" > "$STATE/org-id"
  db="acr_${PROJECT//-/}_e2e"
  compose exec -T clickhouse clickhouse-client --user default --password ch --query "CREATE DATABASE IF NOT EXISTS ${db}" >/dev/null
  compose exec -T api sh -ec "CLICKHOUSE_URI=clickhouse://default:ch@clickhouse:8123/${db} dev-hops migrate clickhouse" >/dev/null
  compose exec -T clickhouse clickhouse-client --user default --password ch --multiquery <<EOF >/dev/null
CREATE USER IF NOT EXISTS acr_reader IDENTIFIED BY '$(<"$STATE/secrets/clickhouse-password")';
ALTER USER acr_reader SETTINGS readonly = 2;
GRANT SELECT ON ${db}.* TO acr_reader;
EOF
}

start_failure_runtime() {
  bootstrap_failure_ops
  if [[ "$SCENARIO" == 'false-entitlement' ]]; then
    remove_runtime_entitlement
  fi
  compose up -d acr-db-init acr-migrate acr-api acr-tls-proxy >/dev/null
  wait_https_ready
  record_acl_probe
  create_acr_credential
  if [[ "$SCENARIO" != 'false-entitlement' ]]; then
    rotate_acr_credential
  fi
}

preserve_failure_evidence() {
  local expected_status="$1" expected_code="$2"
  EVIDENCE_DIR="$REPO_ROOT/.omo/evidence/task-23-acr-project-completion/$PROJECT"
  mkdir -p "$EVIDENCE_DIR"
  cp "$STATE/${SCENARIO}.response.json" "$EVIDENCE_DIR/"
  jq -n \
    --arg project "$PROJECT" \
    --arg scenario "$SCENARIO" \
    --arg status "$expected_status" \
    --arg code "$expected_code" \
    --arg boundary "$SAFE_BOUNDARY" \
    '{project:$project,scenario:$scenario,expected_http_status:($status | tonumber),expected_error_code:$code,safe_boundary:$boundary,forbidden_persistence:"unchanged"}' \
    > "$EVIDENCE_DIR/manifest.json"
}

run_failure_scenario() {
  local token snapshots_before episodes_before expected_status expected_code
  svs_note "starting isolated ${SCENARIO} fail-closed scenario"
  start_failure_runtime
  if [[ "$SCENARIO" == 'false-entitlement' ]]; then
    token="$(<"$STATE/secrets/acr-token")"
  else
    token="$(<"$STATE/secrets/acr-rotated-token")"
  fi
  write_context_request
  snapshots_before="$(acr_table_count snapshots)"
  episodes_before="$(acr_table_count episodes)"

  case "$SCENARIO" in
    foreign-repo)
      jq '.repository.slug = "acme/foreign"' "$STATE/request.json" > "$STATE/foreign-request.json"
      expected_status=403
      expected_code=repo_forbidden
      expect_typed_http_error "$expected_status" "$expected_code" curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
        -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \
        --data-binary @"$STATE/foreign-request.json" "https://localhost:${PORT}/api/v1/agent-context/context-packets"
      SAFE_BOUNDARY='repository authorization rejected an out-of-scope repository before packet assembly'
      ;;
    false-entitlement)
      expected_status=403
      expected_code=feature_not_enabled
      expect_typed_http_error "$expected_status" "$expected_code" curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
        -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \
        --data-binary @"$STATE/request.json" "https://localhost:${PORT}/api/v1/agent-context/context-packets"
      SAFE_BOUNDARY='the entitlement boundary rejected an organization without agent_context_runtime before packet assembly'
      ;;
    bad-credential)
      expected_status=401
      expected_code=invalid_token
      expect_typed_http_error "$expected_status" "$expected_code" curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
        -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer $(<"$STATE/secrets/acr-token")" \
        "https://localhost:${PORT}/api/v1/agent-context/capabilities"
      SAFE_BOUNDARY='the immediately revoked credential returned a typed authentication denial before protected reads'
      ;;
    malformed-evidence)
      expected_status=404
      expected_code=not_found
      expect_typed_http_error "$expected_status" "$expected_code" curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
        -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" \
        "https://localhost:${PORT}/api/v1/agent-context/evidence/bad"
      SAFE_BOUNDARY='the evidence boundary collapsed a malformed reference to the typed not-found response'
      ;;
    assertion-confusion)
      expected_status=401
      expected_code=invalid_token
      expect_typed_http_error "$expected_status" "$expected_code" curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
        -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" -H 'X-ACR-Web-Assertion: invalid.jwt.parts' -H 'Content-Type: application/json' \
        --data-binary @"$STATE/request.json" "https://localhost:${PORT}/api/v1/agent-context/context-packets"
      SAFE_BOUNDARY='a malformed web assertion took precedence and could not fall back to the otherwise valid bearer credential'
      ;;
    default-writeback)
      expected_status=403
      expected_code=insufficient_scope
      expect_typed_http_error "$expected_status" "$expected_code" curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
        -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \
        --data-binary '{}' "https://localhost:${PORT}/api/v1/agent-context/episodes"
      SAFE_BOUNDARY='the default read-only credential rejected episode writeback before request handling'
      ;;
  esac

  assert_no_forbidden_persistence "$snapshots_before" "$episodes_before"
  preserve_failure_evidence "$expected_status" "$expected_code"
}

assert_project_unused
prepare_state
write_web_assertion_material
ensure_image
render_override
render_svs_override
assert_safe_render
if [[ "$SCENARIO" == 'happy' ]]; then
  svs_note 'starting isolated API, MCP, and web happy path'
  start_happy
  bootstrap_web_user
  compose up -d bugsink web-svs >/dev/null
  wait_web_ready
  assert_canonical_account_login
  enable_cross_surface_request_id
  write_context_request
  capture_api_surface
  clear_packet_snapshot "$STATE/api-packet.json"
  capture_mcp_surface
  clear_packet_snapshot "$STATE/mcp-packet.json"
  capture_browser_surface
  assert_cross_surface_agreement
  preserve_evidence
  SAFE_BOUNDARY='real TLS API, host-local MCP, and authenticated browser BFF agreed on packet and evidence IDs; writeback remained disabled'
else
  run_failure_scenario
fi
svs_note "PASS: ${SCENARIO}: ${SAFE_BOUNDARY}"
