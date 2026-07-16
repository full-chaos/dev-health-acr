#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
COMPOSE_FILE=""
OVERLAY_FILE=""
PROJECT=""
SCENARIO="happy"
STATE=""
PORT=""
IMAGE="${ACR_E2E_IMAGE:-}"
COMPOSE=(docker compose)
SAFE_BOUNDARY=""

usage() { printf 'usage: %s --compose <root-compose.yml> --overlay <acr.compose.yml> --project <acr-e2e-name> [--scenario happy|existing-volume|missing-ops-token|invalid-ca|revoked-acr-token|clickhouse-read-denied|migration-failure]\n' "$0" >&2; }
die() { printf '[compose-e2e] FAIL: %s\n' "$*" >&2; exit 1; }
note() { printf '[compose-e2e] %s\n' "$*" >&2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --compose) COMPOSE_FILE="${2:-}"; shift 2 ;;
    --overlay) OVERLAY_FILE="${2:-}"; shift 2 ;;
    --project) PROJECT="${2:-}"; shift 2 ;;
    --scenario) SCENARIO="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

case "$SCENARIO" in happy|existing-volume|missing-ops-token|invalid-ca|revoked-acr-token|clickhouse-read-denied|migration-failure) ;; *) usage; exit 2 ;; esac
[[ -f "$COMPOSE_FILE" && -f "$OVERLAY_FILE" ]] || { usage; exit 2; }
[[ "$PROJECT" =~ ^acr-e2e-[a-z0-9][a-z0-9-]{2,40}$ ]] || die 'project must be an isolated acr-e2e-* name'
[[ "$PROJECT" != dev-health && "$PROJECT" != default ]] || die 'refusing the operator default Compose project'
for tool in docker openssl curl git; do command -v "$tool" >/dev/null || die "$tool is required"; done

compose() { "${COMPOSE[@]}" -p "$PROJECT" --project-directory "$STATE/stage" -f "$STATE/stage/compose.yml" -f "$OVERLAY_FILE" -f "$STATE/override.yml" "$@"; }

owned_receipt() {
  local containers volumes networks
  containers="$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}" | LC_ALL=C sort | tr '\n' ',')"
  volumes="$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}" | LC_ALL=C sort | tr '\n' ',')"
  networks="$(docker network ls -q --filter "label=com.docker.compose.project=${PROJECT}" | LC_ALL=C sort | tr '\n' ',')"
  printf 'containers=%s volumes=%s networks=%s' "${containers%,}" "${volumes%,}" "${networks%,}"
}

cleanup() {
  local status=$?
  [[ -n "$STATE" && -d "$STATE" ]] || exit "$status"
  note "cleanup receipt before: $(owned_receipt)"
  if ! compose down --volumes --remove-orphans >/dev/null 2>&1; then
    note 'cleanup failed; refusing to claim an isolated teardown'
    status=1
  fi
  if [[ -n "$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}")" || -n "$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}")" || -n "$(docker network ls -q --filter "label=com.docker.compose.project=${PROJECT}")" ]]; then
    note "cleanup residue: $(owned_receipt)"
    status=1
  else
    note 'cleanup receipt after: zero owned containers, volumes, and networks'
  fi
  rm -rf "$STATE"
  exit "$status"
}

trap cleanup EXIT

random_secret() { openssl rand -base64 36 | tr -d '\n' | tr '/+' '_-' | cut -c1-32; }
write_secret() { (umask 077; printf '%s' "$2" > "$1"); }
free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }

prepare_state() {
  mkdir -p "$REPO_ROOT/.tmp/e2e"
  STATE="$(mktemp -d "$REPO_ROOT/.tmp/e2e/compose-${PROJECT}.XXXXXX")"
  mkdir -p "$STATE/stage" "$STATE/pki" "$STATE/secrets" "$STATE/clickhouse"
  cp "$COMPOSE_FILE" "$STATE/stage/compose.yml"
  ln -s "$(cd "$(dirname "$COMPOSE_FILE")" && pwd)/ops" "$STATE/stage/ops"
  ln -s "$(cd "$(dirname "$COMPOSE_FILE")" && pwd)/web" "$STATE/stage/web"
  ln -s "$REPO_ROOT/deploy" "$STATE/stage/deploy"
  "$REPO_ROOT/scripts/deploy/local-pki.sh" --out "$STATE/pki" --dns 'localhost,acr-api,acr-ops-tls,clickhouse,postgres,127.0.0.1'
  PORT="$(free_port)"
  write_secret "$STATE/secrets/postgres-password" "$(random_secret)"
  write_secret "$STATE/secrets/runtime-password" "$(random_secret)"
  write_secret "$STATE/secrets/migration-password" "$(random_secret)"
  write_secret "$STATE/secrets/clickhouse-password" "$(random_secret)"
  write_secret "$STATE/secrets/evidence-kid" 'acr-e2e-kid'
  write_secret "$STATE/secrets/evidence-keys" '{}'
  : > "$STATE/secrets/ops-token"; chmod 600 "$STATE/secrets/ops-token"
  cat > "$STATE/clickhouse/tls.xml" <<EOF
<clickhouse><tcp_port_secure>9440</tcp_port_secure><openSSL><server><certificateFile>/run/acr-tls/acr.crt</certificateFile><privateKeyFile>/run/acr-tls/acr.key</privateKeyFile><caConfig>/run/acr-tls/ca.crt</caConfig><verificationMode>none</verificationMode></server></openSSL></clickhouse>
EOF
  cat > "$STATE/nginx-api.conf" <<'EOF'
events {}
http { server { listen 8443 ssl; ssl_certificate /run/pki/acr.crt; ssl_certificate_key /run/pki/acr.key; location / { proxy_pass http://acr-api:8080; } } }
EOF
  cat > "$STATE/nginx-ops.conf" <<'EOF'
events {}
http { server { listen 8443 ssl; ssl_certificate /run/pki/acr.crt; ssl_certificate_key /run/pki/acr.key; location / { proxy_pass http://api:8000; } } }
EOF
}

ensure_image() {
  if [[ -z "$IMAGE" ]]; then
    local tag="${PROJECT}-acr-api:local"
    CONTAINER_IMAGE="$tag" "$REPO_ROOT/scripts/container/build.sh" acr-api >/dev/null
    IMAGE="${tag}@$(docker image inspect "$tag" --format '{{.Id}}')"
  fi
  [[ "$IMAGE" == *@sha256:* ]] || die 'ACR_E2E_IMAGE must be an immutable digest reference'
}

render_override() {
  local pg runtime migration ch db postgres_port clickhouse_http_port clickhouse_native_port redis_port
  pg="$(<"$STATE/secrets/postgres-password")"; runtime="$(<"$STATE/secrets/runtime-password")"; migration="$(<"$STATE/secrets/migration-password")"; ch="$(<"$STATE/secrets/clickhouse-password")"
  db="acr_${PROJECT//-/}_e2e"
  write_secret "$STATE/secrets/runtime-dsn" "postgres://acr_runtime:${runtime}@postgres:5432/${db}?sslmode=verify-full&sslrootcert=/run/secrets/acr_ca"
  write_secret "$STATE/secrets/migration-dsn" "postgres://acr_migration:${migration}@postgres:5432/${db}?sslmode=verify-full&sslrootcert=/run/secrets/acr_ca"
  write_secret "$STATE/secrets/clickhouse-dsn" "clickhouse://acr_reader:${ch}@clickhouse:9440/${db}?secure=true&skip_verify=false"
  postgres_port="$(free_port)"
  clickhouse_http_port="$(free_port)"
  clickhouse_native_port="$(free_port)"
  redis_port="$(free_port)"
  cat > "$STATE/override.yml" <<EOF
services:
  postgres:
    ports: []
    environment: { POSTGRES_USER: devhealth, POSTGRES_PASSWORD: "${pg}", POSTGRES_DB: devhealth }
    command: ["postgres", "-c", "ssl=on", "-c", "ssl_cert_file=/run/acr-tls/acr.crt", "-c", "ssl_key_file=/run/acr-tls/acr.key", "-c", "ssl_ca_file=/run/acr-tls/ca.crt"]
    volumes: ["postgres_data:/var/lib/postgresql/data", "${STATE}/stage/ops/docker/init-extra-dbs.sh:/docker-entrypoint-initdb.d/init-extra-dbs.sh:ro", "acr_e2e_tls:/run/acr-tls:ro"]
    depends_on: { acr-pki-init: { condition: service_completed_successfully } }
  clickhouse:
    ports: []
    environment: { CLICKHOUSE_USER: ch, CLICKHOUSE_PASSWORD: ch, CLICKHOUSE_DB: default }
    volumes: ["clickhouse_data:/var/lib/clickhouse", "acr_e2e_tls:/run/acr-tls:ro", "${STATE}/clickhouse/tls.xml:/etc/clickhouse-server/config.d/acr-e2e-tls.xml:ro"]
    depends_on: { acr-pki-init: { condition: service_completed_successfully } }
  valkey: { ports: [] }
  traefik: { ports: [] }
  acr-pki-init:
    image: postgres:18-alpine
    user: "0:0"
    entrypoint: ["/bin/sh", "-ec"]
    command: "cp /input/acr.crt /input/acr.key /input/ca.crt /tls/; chown -R 999:999 /tls; chmod 600 /tls/acr.key"
    volumes: ["${STATE}/pki:/input:ro", "acr_e2e_tls:/tls"]
  acr-api:
    image: "${IMAGE}"
    ports: []
    environment:
      ACR_ENVIRONMENT: development
      ACR_POSTGRES_DSN_FILE: /run/secrets/acr_runtime_dsn
      ACR_CLICKHOUSE_DSN_FILE: /run/secrets/acr_clickhouse_dsn
      ACR_CLICKHOUSE_CA_BUNDLE: /run/secrets/acr_ca
      ACR_DEV_HEALTH_ENTITLEMENT_URL: https://acr-ops-tls:8443
      ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE: /run/secrets/acr_ca
  acr-migrate: { image: "${IMAGE}" }
  acr-tls-proxy:
    image: nginx:1.27-alpine
    ports: ["127.0.0.1:${PORT}:8443"]
    volumes: ["${STATE}/pki:/run/pki:ro", "${STATE}/nginx-api.conf:/etc/nginx/nginx.conf:ro"]
    depends_on: { acr-api: { condition: service_healthy } }
    networks: [dev-health]
  acr-ops-tls:
    image: nginx:1.27-alpine
    volumes: ["${STATE}/pki:/run/pki:ro", "${STATE}/nginx-ops.conf:/etc/nginx/nginx.conf:ro"]
    depends_on: { api: { condition: service_healthy } }
    networks: [dev-health]
volumes:
  acr_e2e_tls: {}
EOF
  export ACR_IMAGE="$IMAGE" POSTGRES_USER=devhealth POSTGRES_PASSWORD="$pg" POSTGRES_PORT="$postgres_port" CLICKHOUSE_HTTP_PORT="$clickhouse_http_port" CLICKHOUSE_NATIVE_PORT="$clickhouse_native_port" REDIS_PORT="$redis_port" ACR_DB_NAME="$db" ACR_RUNTIME_DB_USER=acr_runtime ACR_RUNTIME_DB_PASSWORD="$runtime" ACR_MIGRATION_DB_USER=acr_migration ACR_MIGRATION_DB_PASSWORD="$migration" ACR_RUNTIME_DSN_FILE="$STATE/secrets/runtime-dsn" ACR_MIGRATION_DSN_FILE="$STATE/secrets/migration-dsn" ACR_CLICKHOUSE_DSN_FILE="$STATE/secrets/clickhouse-dsn" ACR_OPS_TOKEN_FILE="$STATE/secrets/ops-token" ACR_CA_FILE="$STATE/pki/ca.crt" ACR_EVIDENCE_ACTIVE_KID_FILE="$STATE/secrets/evidence-kid" ACR_EVIDENCE_KEYS_FILE="$STATE/secrets/evidence-keys"
}

assert_safe_render() {
  compose config > "$STATE/rendered.yml"
  grep -q '^  acr-api:' "$STATE/rendered.yml" || die 'ACR API missing from rendered stack'
  ! grep -q 'acr-mcp:' "$STATE/rendered.yml" || die 'MCP must remain host-local'
  ! grep -Eq 'name:[[:space:]]*(postgres_data|clickhouse_data|valkey_data)$' "$STATE/rendered.yml" || die 'refusing shared default volume name'
  if ! grep -q 'host_ip: 127.0.0.1' "$STATE/rendered.yml" || ! grep -q 'target: 8443' "$STATE/rendered.yml" || ! grep -q "published: \"${PORT}\"" "$STATE/rendered.yml"; then
    die 'direct localhost TLS endpoint missing'
  fi
}

wait_https() { curl --fail --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' "https://localhost:${PORT}$1" >/dev/null; }

bootstrap_ops() {
  local output org_id token db
  compose up -d postgres clickhouse valkey pgbouncer mailpit migrate api acr-ops-tls >/dev/null
  output="$(compose exec -T api dev-hops admin orgs create --name "${PROJECT} E2E" --slug "$PROJECT" --description 'isolated compose E2E' --tier community --owner-email 'owner@example.test' 2>/dev/null)"
  org_id="$(printf '%s\n' "$output" | sed -nE 's/.*id:[[:space:]]*([0-9a-fA-F-]{36}).*/\1/p')"
  [[ "$org_id" =~ ^[0-9a-fA-F-]{36}$ ]] || die 'Ops org provisioning did not return an ID'
  compose exec -T api dev-hops admin bundles assign-org --org-id "$org_id" --feature-key agent_context_runtime --reason 'isolated compose E2E' --expires-days 1 >/dev/null
  token="$(compose exec -T api dev-hops service-credentials create --service acr --scope entitlements:read)"
  [[ "$token" == svc_acr_* ]] || die 'Ops credential provisioning returned an invalid token shape'
  write_secret "$STATE/secrets/ops-token" "$token"
  db="acr_${PROJECT//-/}_e2e"
  compose exec -T api dev-hops migrate clickhouse >/dev/null
  compose exec -T api sh -ec "CLICKHOUSE_URI=clickhouse://ch:ch@clickhouse:8123/${db} dev-hops fixtures generate --sink \"\$CLICKHOUSE_URI\" --db-type clickhouse --repo-name acme/live-e2e --provider synthetic --days 14 --commits-per-day 6 --pr-count 24 --seed 20260219 --with-metrics --with-work-graph" >/dev/null
  compose exec -T clickhouse clickhouse-client --user ch --password ch --multiquery <<EOF >/dev/null
CREATE USER IF NOT EXISTS acr_reader IDENTIFIED BY '$(<"$STATE/secrets/clickhouse-password")';
GRANT SELECT ON ${db}.* TO acr_reader;
EOF
  printf '%s' "$org_id" > "$STATE/org-id"
}

create_acr_credential() {
  local token
  token="$(compose run --rm --no-deps acr-api credentials create --org-id "$(<"$STATE/org-id")" --repository-scope acme/live-e2e --scope context:read,evidence:read --name compose-e2e --actor compose-e2e)"
  [[ "$token" == fcacr_* ]] || die 'ACR credential provisioning returned an invalid token shape'
  write_secret "$STATE/secrets/acr-token" "$token"
}

run_mcp() {
  local token="$1" evidence_id output
  ACR_API_URL="https://localhost:${PORT}" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$STATE/pki/ca.crt" ACR_SIDECAR_VERSION=1.0.0 ACR_SIDECAR_CLIENT_VERSION=1.0.0 "$STATE/acr-mcp" doctor --live > "$STATE/doctor.json"
  grep -q '"status": "ok"' "$STATE/doctor.json" || die 'host MCP doctor did not report healthy TLS capability'
  output="$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"compose-e2e","version":"1.0.0"}}}' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"context_for_task","arguments":{"goal":"inspect live evidence","repository":{"slug":"acme/live-e2e"},"scope":{"branch":"main"}}}}' | ACR_API_URL="https://localhost:${PORT}" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$STATE/pki/ca.crt" ACR_SIDECAR_VERSION=1.0.0 ACR_SIDECAR_CLIENT_VERSION=1.0.0 "$STATE/acr-mcp" serve)"
  evidence_id="$(printf '%s\n' "$output" | sed -nE 's/.*"evidence_ref_id":"([^"]+)".*/\1/p' | head -1)"
  [[ -n "$evidence_id" ]] || die 'context_for_task returned no evidence reference'
  printf '%s\n' "$output" | grep -q 'context_packet' || die 'context_for_task did not return a packet'
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"compose-e2e","version":"1.0.0"}}}' "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"source_evidence\",\"arguments\":{\"evidence_ref_id\":\"${evidence_id}\"}}}" | ACR_API_URL="https://localhost:${PORT}" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$STATE/pki/ca.crt" ACR_SIDECAR_VERSION=1.0.0 ACR_SIDECAR_CLIENT_VERSION=1.0.0 "$STATE/acr-mcp" serve | grep -q 'expanded_evidence' || die 'source_evidence did not return evidence'
}

start_happy() {
  bootstrap_ops
  compose up -d acr-db-init acr-migrate acr-api acr-tls-proxy >/dev/null
  until wait_https /readyz; do sleep 2; done
  create_acr_credential
  go build -o "$STATE/acr-mcp" ./cmd/acr-mcp
  run_mcp "$(<"$STATE/secrets/acr-token")"
  SAFE_BOUNDARY='verified TLS readiness, host-local MCP, non-empty packet and evidence'
}

expect_failure() { "$@" >/dev/null 2>&1 && die "expected failure: $*"; }

run_failure() {
  case "$SCENARIO" in
    missing-ops-token)
      rm -f "$STATE/secrets/ops-token"; expect_failure compose config; SAFE_BOUNDARY='ACR services were not started without an Ops token file' ;;
    invalid-ca)
      start_happy; printf 'not a certificate\n' > "$STATE/pki/invalid-ca.crt"; expect_failure curl --fail --silent --cacert "$STATE/pki/invalid-ca.crt" --noproxy '*' "https://localhost:${PORT}/readyz"; SAFE_BOUNDARY='TLS verification failed; no insecure curl option was used' ;;
    revoked-acr-token)
      start_happy; compose run --rm --no-deps acr-api credentials revoke --org-id "$(<"$STATE/org-id")" --credential-id "$(compose run --rm --no-deps acr-api credentials list --org-id "$(<"$STATE/org-id")" --json | sed -nE 's/.*"credential_id":"([^"]+)".*/\1/p' | head -1)" --actor compose-e2e >/dev/null; expect_failure curl --fail --silent --cacert "$STATE/pki/ca.crt" --noproxy '*' -H "Authorization: Bearer $(<"$STATE/secrets/acr-token")" "https://localhost:${PORT}/api/v1/agent-context/capabilities"; SAFE_BOUNDARY='revoked ACR credential was denied before protected reads' ;;
    clickhouse-read-denied)
      start_happy; expect_failure compose exec -T clickhouse clickhouse-client --user acr_reader --password "$(<"$STATE/secrets/clickhouse-password")" --query "INSERT INTO acr_${PROJECT//-/}_e2e.system_metrics VALUES ()"; SAFE_BOUNDARY='read-only ClickHouse user rejected a write' ;;
    migration-failure)
      bootstrap_ops; expect_failure compose run --rm --no-deps -e ACR_POSTGRES_MIGRATION_DSN_FILE=/missing acr-migrate; SAFE_BOUNDARY='migration stopped before connecting with an unreadable DSN secret' ;;
  esac
  note "expected failure verified: ${SAFE_BOUNDARY}"
  return 1
}

prepare_state
ensure_image
render_override
assert_safe_render

if [[ "$SCENARIO" == happy ]]; then
  start_happy
elif [[ "$SCENARIO" == existing-volume ]]; then
  start_happy
  compose down --remove-orphans >/dev/null
  compose up -d postgres clickhouse valkey pgbouncer mailpit migrate api acr-ops-tls acr-db-init acr-migrate acr-api acr-tls-proxy >/dev/null
  until wait_https /readyz; do sleep 2; done
  run_mcp "$(<"$STATE/secrets/acr-token")"
  SAFE_BOUNDARY='existing project-scoped volumes survived a complete application restart'
else
  run_failure
fi
note "PASS: ${SCENARIO}: ${SAFE_BOUNDARY}"
