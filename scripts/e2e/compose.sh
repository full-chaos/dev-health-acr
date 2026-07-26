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
ACR_CLIENT_VERSION_HEADER="X-ACR-Client-Version: 1.0.0"
# Repository the scoped ACR credential and the built-in MCP probe target. Suites that seed a
# different corpus override this before create_acr_credential runs.
ACR_E2E_REPOSITORY_SCOPE="${ACR_E2E_REPOSITORY_SCOPE:-acme/live-e2e}"
POSTGRES_IMAGE="postgres:18-alpine@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15"
CLICKHOUSE_IMAGE="clickhouse/clickhouse-server:latest@sha256:d7556a3841027651307b5aa08d72b5c467d0241d3db5b67d9e158ef3975626f5"
PGBOUNCER_IMAGE="edoburu/pgbouncer:latest@sha256:4c1ca296ef525f108f5d3552cc337c0c09587cf8dae7f0067fd93349e47dc1cd"
VALKEY_IMAGE="valkey/valkey:9-alpine@sha256:ee91f7a174ac4d6a6b0685b3a60e321f0a9dbbb691f9b0e285be2ba1d1be8328"
MAILPIT_IMAGE="axllent/mailpit:latest@sha256:b868afa176bfd6cce2323ea316cd99ccad77915e51e595748f6d786700ecf109"
NGINX_IMAGE="nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10"

# allow: SIZE_OK — the isolated scenario lifecycle shares one trap-owned state directory.

usage() { printf 'usage: %s --compose <root-compose.yml> --overlay <acr.compose.yml> --project <acr-e2e-name> [--scenario happy|existing-volume|missing-ops-token|invalid-ca|revoked-acr-token|clickhouse-read-denied|migration-failure]\n' "$0" >&2; }
die() { printf '[compose-e2e] FAIL: %s\n' "$*" >&2; exit 1; }
note() { printf '[compose-e2e] %s\n' "$*" >&2; }
redact_log() { sed -E -e 's/(fcacr|svc_acr)_[[:alnum:]_-]+/REDACTED/g' -e 's#(postgresql?|clickhouse)://[^[:space:]]+#REDACTED_DSN#g'; }

parse_args() {
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
  [[ "$PROJECT" =~ ^acr-(e2e|svs)-[a-z0-9][a-z0-9-]{2,40}$ ]] || die 'project must be an isolated acr-e2e-* or acr-svs-* name'
  [[ "$PROJECT" != dev-health && "$PROJECT" != default ]] || die 'refusing the operator default Compose project'
  for tool in docker openssl curl git jq; do command -v "$tool" >/dev/null || die "$tool is required"; done
}

compose_files() {
  printf '%s\0' -f "$STATE/stage/compose.yml" -f "$OVERLAY_FILE" -f "$STATE/override.yml"
  if [[ -f "$STATE/svs.override.yml" ]]; then
    printf '%s\0' -f "$STATE/svs.override.yml"
  fi
}

compose() {
  local files=()
  while IFS= read -r -d '' entry; do files+=("$entry"); done < <(compose_files)
  "${COMPOSE[@]}" -p "$PROJECT" --project-directory "$STATE/stage" "${files[@]}" "$@"
}

# compose_argv emits the fully-resolved argv that compose() runs, NUL-separated so a path
# containing spaces survives. It exists because compose is a shell function: a tool that has
# to invoke the same command from its own process — the fixture verifier shells out to run
# ClickHouse probes — cannot be handed the word "compose", as no such executable exists.
compose_argv() {
  local files=()
  while IFS= read -r -d '' entry; do files+=("$entry"); done < <(compose_files)
  printf '%s\0' "${COMPOSE[@]}" -p "$PROJECT" --project-directory "$STATE/stage" "${files[@]}"
}

owned_receipt() {
  local containers volumes networks
  containers="$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}" | LC_ALL=C sort | tr '\n' ',')"
  volumes="$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}" | LC_ALL=C sort | tr '\n' ',')"
  networks="$(docker network ls -q --filter "label=com.docker.compose.project=${PROJECT}" | LC_ALL=C sort | tr '\n' ',')"
  printf 'containers=%s volumes=%s networks=%s' "${containers%,}" "${volumes%,}" "${networks%,}"
}

assert_project_unused() {
  local existing
  existing="$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}")$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}")$(docker network ls -q --filter "label=com.docker.compose.project=${PROJECT}")"
  if [[ -n "$existing" ]]; then
    die 'refusing pre-existing Compose project resources'
  fi
  if docker compose ls --all --format json | jq -e --arg project "$PROJECT" '.[] | select(.Name == $project)' >/dev/null; then
    die 'refusing pre-existing Compose project name'
  fi
}

cleanup() {
  local status=$? health_container service
  local cleanup_failed=0
  trap '' INT TERM HUP
  [[ -n "$STATE" && -d "$STATE" ]] || exit "$status"
  if [[ ! -f "$STATE/override.yml" ]]; then
    rm -rf "$STATE" || cleanup_failed=1
    if [[ "$status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
      status=1
    fi
    exit "$status"
  fi
  note "cleanup receipt before: $(owned_receipt)"
  if [[ "$status" -ne 0 ]]; then
    compose logs --no-color acr-pki-init clickhouse migrate acr-ops-tls acr-migrate acr-api acr-tls-proxy 2>&1 | redact_log || true
    for service in clickhouse api; do
      health_container="$(compose ps -q "$service" 2>/dev/null || true)"
      if [[ -n "$health_container" ]]; then
        docker inspect --format '{{range .State.Health.Log}}{{.Output}}{{end}}' "$health_container" 2>&1 | redact_log || true
      fi
    done
  fi
  if ! compose down --volumes --remove-orphans >/dev/null 2>&1; then
    note 'cleanup failed; refusing to claim an isolated teardown'
    cleanup_failed=1
  fi
  if [[ -n "$(docker ps -aq --filter "label=com.docker.compose.project=${PROJECT}")" || -n "$(docker volume ls -q --filter "label=com.docker.compose.project=${PROJECT}")" || -n "$(docker network ls -q --filter "label=com.docker.compose.project=${PROJECT}")" ]]; then
    note "cleanup residue: $(owned_receipt)"
    cleanup_failed=1
  else
    note 'cleanup receipt after: zero owned containers, volumes, and networks'
  fi
  rm -rf "$STATE" || cleanup_failed=1
  if [[ "$status" -eq 0 && "$cleanup_failed" -ne 0 ]]; then
    status=1
  fi
  exit "$status"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  trap 'exit 129' HUP
fi

random_secret() { openssl rand -base64 36 | tr -d '\n' | tr '/+' '_-' | cut -c1-32; }
random_base64() { openssl rand -base64 32 | tr -d '\n'; }
write_secret() { (umask 077; printf '%s' "$2" > "$1"); }
xml_escape() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g"; }
write_clickhouse_readonly_client_config() {
  local password escaped
  password="$(<"$STATE/secrets/clickhouse-password")"
  escaped="$(printf '%s' "$password" | xml_escape)"
  (umask 077; cat > "$STATE/clickhouse/client-readonly.xml" <<EOF
<clickhouse><password>${escaped}</password><openSSL><client><loadDefaultCAFile>false</loadDefaultCAFile><caConfig>/run/acr-tls/ca.crt</caConfig><verificationMode>strict</verificationMode><invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler></client></openSSL></clickhouse>
EOF
)
}
free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }

prepare_state() {
  mkdir -p "$REPO_ROOT/.tmp/e2e"
  STATE="$(mktemp -d "$REPO_ROOT/.tmp/e2e/compose-${PROJECT}.XXXXXX")"
  mkdir -p "$STATE/stage" "$STATE/pki" "$STATE/secrets" "$STATE/clickhouse"
  cp "$COMPOSE_FILE" "$STATE/stage/compose.yml"
  ln -s "$(cd "$(dirname "$COMPOSE_FILE")" && pwd)/ops" "$STATE/stage/ops"
  ln -s "$(cd "$(dirname "$COMPOSE_FILE")" && pwd)/web" "$STATE/stage/web"
  ln -s "$REPO_ROOT/deploy" "$STATE/stage/deploy"
  "$REPO_ROOT/scripts/deploy/local-pki.sh" --out "$STATE/pki" --dns 'localhost,acr-api,acr-tls-proxy,acr-ops-tls,clickhouse,postgres,127.0.0.1'
  PORT="$(free_port)"
  write_secret "$STATE/secrets/postgres-password" "$(random_secret)"
  write_secret "$STATE/secrets/runtime-password" "$(random_secret)"
  write_secret "$STATE/secrets/migration-password" "$(random_secret)"
  write_secret "$STATE/secrets/clickhouse-password" "$(random_secret)"
  write_secret "$STATE/secrets/evidence-kid" 'acr-e2e-kid'
  write_secret "$STATE/secrets/evidence-keys" "acr-e2e-kid=$(random_base64)"
  : > "$STATE/secrets/ops-token"; chmod 600 "$STATE/secrets/ops-token"
  cat > "$STATE/clickhouse/tls.xml" <<EOF
<clickhouse><tcp_port_secure>9440</tcp_port_secure><openSSL><server><certificateFile>/run/acr-tls/acr.crt</certificateFile><privateKeyFile>/run/acr-tls/acr.key</privateKeyFile><caConfig>/run/acr-tls/ca.crt</caConfig><verificationMode>relaxed</verificationMode></server></openSSL></clickhouse>
EOF
  cat > "$STATE/clickhouse/client-tls.xml" <<EOF
<clickhouse><openSSL><client><loadDefaultCAFile>false</loadDefaultCAFile><caConfig>/run/acr-tls/ca.crt</caConfig><verificationMode>strict</verificationMode><invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler></client></openSSL></clickhouse>
EOF
  write_clickhouse_readonly_client_config
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
  local pg runtime migration ch jwt db postgres_port clickhouse_http_port clickhouse_native_port redis_port
  pg="$(<"$STATE/secrets/postgres-password")"; runtime="$(<"$STATE/secrets/runtime-password")"; migration="$(<"$STATE/secrets/migration-password")"; ch="$(<"$STATE/secrets/clickhouse-password")"
  jwt="$(random_secret)"
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
    image: "${POSTGRES_IMAGE}"
    ports: !override []
    environment: { POSTGRES_USER: devhealth, POSTGRES_PASSWORD: "${pg}", POSTGRES_DB: devhealth }
    command: ["postgres", "-c", "ssl=on", "-c", "ssl_cert_file=/run/acr-tls/acr.crt", "-c", "ssl_key_file=/run/acr-tls/acr.key", "-c", "ssl_ca_file=/run/acr-tls/ca.crt"]
    volumes: ["postgres_data:/var/lib/postgresql/data", "${STATE}/stage/ops/docker/init-extra-dbs.sh:/docker-entrypoint-initdb.d/init-extra-dbs.sh:ro", "acr_e2e_postgres_tls:/run/acr-tls:ro"]
    depends_on: { acr-pki-init: { condition: service_completed_successfully } }
  clickhouse:
    image: "${CLICKHOUSE_IMAGE}"
    ports: !override []
    environment: { CLICKHOUSE_USER: default, CLICKHOUSE_PASSWORD: ch, CLICKHOUSE_DB: default, CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT: "1" }
    volumes: ["clickhouse_data:/var/lib/clickhouse", "acr_e2e_clickhouse_tls:/run/acr-tls:ro", "${STATE}/clickhouse/tls.xml:/etc/clickhouse-server/config.d/acr-e2e-tls.xml:ro", "${STATE}/clickhouse/client-tls.xml:/etc/clickhouse-client/config.d/acr-e2e-tls.xml:ro", "${STATE}/clickhouse/client-readonly.xml:/run/acr-e2e/clickhouse-client-readonly.xml:ro"]
    healthcheck: { test: ["CMD-SHELL", "clickhouse-client --config-file=/etc/clickhouse-client/config.d/acr-e2e-tls.xml --secure --host clickhouse --port 9440 --user default --password ch --query 'SELECT 1'"], interval: 5s, timeout: 5s, retries: 12, start_period: 10s }
    depends_on: { acr-pki-init: { condition: service_completed_successfully } }
  pgbouncer: { image: "${PGBOUNCER_IMAGE}" }
  valkey:
    image: "${VALKEY_IMAGE}"
    ports: !override []
  mailpit: { image: "${MAILPIT_IMAGE}" }
  api:
    environment: { SETUPTOOLS_SCM_PRETEND_VERSION: "0.0.0", JWT_SECRET_KEY: "${jwt}" }
    healthcheck: { test: ["CMD", "wget", "--spider", "http://localhost:8000/ready"], interval: 5s, timeout: 5s, retries: 36, start_period: 120s }
  traefik: { ports: [] }
  acr-pki-init:
    image: "${POSTGRES_IMAGE}"
    user: "0:0"
    entrypoint: ["/bin/sh", "-ec"]
    command: ["cp /input/acr.crt /input/acr.key /input/ca.crt /postgres-tls/; chown -R 70:70 /postgres-tls; chmod 600 /postgres-tls/acr.key; cp /input/acr.crt /input/acr.key /input/ca.crt /clickhouse-tls/; chown -R 101:101 /clickhouse-tls; chmod 600 /clickhouse-tls/acr.key"]
    volumes: ["${STATE}/pki:/input:ro", "acr_e2e_postgres_tls:/postgres-tls", "acr_e2e_clickhouse_tls:/clickhouse-tls"]
  acr-api:
    image: "${IMAGE}"
    ports: !override []
    healthcheck: { test: ["NONE"] }
    environment: !override
      ACR_ADDR: :8080
      ACR_ENVIRONMENT: development
      ACR_LOG_LEVEL: debug
      ACR_POSTGRES_CONNECTION_KIND: direct
      ACR_ENABLE_EPISODE_WRITEBACK: "${ACR_ENABLE_EPISODE_WRITEBACK:-false}"
      ACR_REQUIRE_BACKING_STORES: "true"
      ACR_POSTGRES_DSN_FILE: /run/secrets/acr_runtime_dsn
      ACR_CLICKHOUSE_DSN_FILE: /run/secrets/acr_clickhouse_dsn
      ACR_CLICKHOUSE_CA_BUNDLE: /run/secrets/acr_ca
      ACR_DEV_HEALTH_ENTITLEMENT_URL: https://acr-ops-tls:8443
      ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE: /run/secrets/acr_ops_token
      ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE: /run/secrets/acr_ca
      ACR_DEVICE_VERIFICATION_URL: "${ACR_E2E_DEVICE_VERIFICATION_URL:-https://device.invalid/acr/device}"
      ACR_EVIDENCE_ID_ACTIVE_KID_FILE: /run/secrets/acr_evidence_active_kid
      ACR_EVIDENCE_ID_KEYS_FILE: /run/secrets/acr_evidence_keys
      # Defaults to the service's own default (60/min), so existing consumers are unchanged.
      # A suite that legitimately issues more reads than a production client would raises it
      # explicitly rather than being throttled into an unrelated-looking failure.
      ACR_REQUESTS_PER_MINUTE: "${ACR_E2E_REQUESTS_PER_MINUTE:-60}"
    volumes: !override []
    depends_on: !override
      postgres: { condition: service_healthy }
      clickhouse: { condition: service_healthy }
      api: { condition: service_healthy }
      acr-db-acl: { condition: service_completed_successfully }
      acr-ops-tls: { condition: service_started }
  acr-credentials:
    image: "${IMAGE}"
    environment:
      ACR_ENVIRONMENT: development
      ACR_POSTGRES_DSN_FILE: /run/secrets/acr_runtime_dsn
    secrets: [acr_runtime_dsn, acr_ca]
    networks: [dev-health]
  acr-migrate:
    image: "${IMAGE}"
    environment: !override
      ACR_ENVIRONMENT: development
      ACR_POSTGRES_CONNECTION_KIND: direct
      ACR_POSTGRES_MIGRATION_DSN_FILE: /run/secrets/acr_migration_dsn
  acr-tls-proxy:
    image: "${NGINX_IMAGE}"
    ports: ["127.0.0.1:${PORT}:8443"]
    volumes: ["${STATE}/pki:/run/pki:ro", "${STATE}/nginx-api.conf:/etc/nginx/nginx.conf:ro"]
    depends_on: { acr-api: { condition: service_started } }
    networks: [dev-health]
  acr-ops-tls:
    image: "${NGINX_IMAGE}"
    volumes: ["${STATE}/pki:/run/pki:ro", "${STATE}/nginx-ops.conf:/etc/nginx/nginx.conf:ro"]
    depends_on: { api: { condition: service_healthy } }
    networks: [dev-health]
volumes:
  acr_e2e_postgres_tls: {}
  acr_e2e_clickhouse_tls: {}
EOF
  # CLICKHOUSE_DB is load-bearing and was missing: every ops service's CLICKHOUSE_URI in the
  # product compose file resolves ${CLICKHOUSE_DB:-default}, so without it the long-running
  # `api` container reads the literal `default` database for its whole lifetime, while every
  # migration and seed in this driver targets the isolated per-project database. The
  # application therefore saw an empty-but-valid schema and answered 200 {"repositories": []}.
  # Nothing caught it because ACR's own ClickHouse access uses a separately parameterized DSN
  # that always named the right database, and the fixture probes queried that database
  # directly rather than through the application.
  export ACR_IMAGE="$IMAGE" POSTGRES_USER=devhealth POSTGRES_PASSWORD="$pg" POSTGRES_PORT="$postgres_port" CLICKHOUSE_USER=default CLICKHOUSE_PASSWORD=ch CLICKHOUSE_DB="$db" CLICKHOUSE_HTTP_PORT="$clickhouse_http_port" CLICKHOUSE_NATIVE_PORT="$clickhouse_native_port" REDIS_PORT="$redis_port" ACR_DB_NAME="$db" ACR_RUNTIME_DB_USER=acr_runtime ACR_MIGRATION_DB_USER=acr_migration ACR_POSTGRES_ADMIN_PASSWORD_FILE="$STATE/secrets/postgres-password" ACR_RUNTIME_DB_PASSWORD_FILE="$STATE/secrets/runtime-password" ACR_MIGRATION_DB_PASSWORD_FILE="$STATE/secrets/migration-password" ACR_RUNTIME_DSN_FILE="$STATE/secrets/runtime-dsn" ACR_MIGRATION_DSN_FILE="$STATE/secrets/migration-dsn" ACR_CLICKHOUSE_DSN_FILE="$STATE/secrets/clickhouse-dsn" ACR_OPS_TOKEN_FILE="$STATE/secrets/ops-token" ACR_CA_FILE="$STATE/pki/ca.crt" ACR_EVIDENCE_ACTIVE_KID_FILE="$STATE/secrets/evidence-kid" ACR_EVIDENCE_KEYS_FILE="$STATE/secrets/evidence-keys"
}

assert_safe_render() {
  compose config > "$STATE/rendered.yml"
  compose config --format json > "$STATE/rendered.json"
  grep -q '^  acr-api:' "$STATE/rendered.yml" || die 'ACR API missing from rendered stack'
  ! grep -q 'acr-mcp:' "$STATE/rendered.yml" || die 'MCP must remain host-local'
  ! grep -Eq 'name:[[:space:]]*(postgres_data|clickhouse_data|valkey_data)$' "$STATE/rendered.yml" || die 'refusing shared default volume name'
  if ! grep -q 'host_ip: 127.0.0.1' "$STATE/rendered.yml" || ! grep -q 'target: 8443' "$STATE/rendered.yml" || ! grep -q "published: \"${PORT}\"" "$STATE/rendered.yml"; then
    die 'direct localhost TLS endpoint missing'
  fi
  jq -e '.services["acr-api"].depends_on | keys == ["acr-db-acl","acr-ops-tls","api","clickhouse","postgres"]' "$STATE/rendered.json" >/dev/null \
    || die 'ACR API inherited an unexpected local-dev dependency'
  jq -e '(.services["acr-api"].volumes // []) | all(.[]; ((.source // "") | contains("/.acr-dev/") | not))' "$STATE/rendered.json" >/dev/null \
    || die 'ACR API inherited a local-dev bind mount'
  if [[ -f "$STATE/svs.override.yml" ]]; then
    jq -e '[.services["acr-api"].volumes[]?.target] | sort == ["/run/acr-e2e/web-assertions.jwks.json"]' "$STATE/rendered.json" >/dev/null \
      || die 'web agreement added an unexpected ACR API bind mount'
  else
    jq -e '(.services["acr-api"].volumes // []) | length == 0' "$STATE/rendered.json" >/dev/null \
      || die 'baseline ACR API retained a bind mount'
  fi
  jq -e '.services["acr-migrate"].environment | keys == ["ACR_ENVIRONMENT","ACR_POSTGRES_CONNECTION_KIND","ACR_POSTGRES_MIGRATION_DSN_FILE"]' "$STATE/rendered.json" >/dev/null \
    || die 'ACR migration environment inherited local-dev configuration'
  jq -e '(.services.api.environment.JWT_SECRET_KEY // "") | length >= 32' "$STATE/rendered.json" >/dev/null \
    || die 'Ops API is missing its per-run JWT secret'
  jq -e '.services["acr-api"].environment as $environment | ($environment | has("ACR_POSTGRES_DSN") | not) and ($environment | has("ACR_CLICKHOUSE_DSN") | not) and ($environment | has("ACR_ALLOW_INSECURE_POSTGRES") | not)' "$STATE/rendered.json" >/dev/null \
    || die 'ACR API environment inherited direct or insecure configuration'
  jq -e '[.services.postgres, .services.clickhouse, .services.valkey] | all(.[]; ((.ports // []) | length == 0))' "$STATE/rendered.json" >/dev/null \
    || die 'acceptance infrastructure retained a host port publication'
  jq -e '.services["acr-db-init"].entrypoint == ["/usr/local/bin/acr-db-init"] and .services["acr-db-init"].command == ["roles"] and .services["acr-db-acl"].entrypoint == ["/usr/local/bin/acr-db-init"] and .services["acr-db-acl"].command == ["runtime-acl"]' "$STATE/rendered.json" >/dev/null \
    || die 'ACR database jobs inherited local-dev process configuration'
}

wait_https() { curl --fail --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' "https://localhost:${PORT}$1" >/dev/null; }
wait_https_ready() {
  local attempts=0
  until wait_https /readyz; do
    attempts=$((attempts + 1))
    if [[ "$attempts" -ge 90 ]]; then
      die 'HTTPS readiness timed out'
    fi
    sleep 2
  done
}

# Evidence seeding is the only part of Ops bootstrap that differs between suites, so it is
# a named hook. The default reproduces the historical synthetic corpus byte for byte;
# scripts/e2e/fullstack-opencode.sh substitutes a versioned deterministic projection.
ACR_E2E_SEED_HOOK="${ACR_E2E_SEED_HOOK:-seed_synthetic_evidence}"

ops_clickhouse_database() { printf 'acr_%s_e2e' "${PROJECT//-/}"; }

clickhouse_query() { compose exec -T clickhouse clickhouse-client --user default --password ch --query "$1"; }

provision_ops_control_plane() {
  local output org_id token
  # The isolated ClickHouse database is created before the ops services start, not in
  # provision_evidence_database below: those services resolve CLICKHOUSE_DB at container start
  # and connect immediately, so a database that does not exist yet is a boot failure rather
  # than a later one.
  compose up -d --wait clickhouse >/dev/null
  clickhouse_query "CREATE DATABASE IF NOT EXISTS $(ops_clickhouse_database)" >/dev/null
  compose up -d postgres valkey pgbouncer mailpit migrate api acr-ops-tls >/dev/null
  if ! output="$(compose exec -T api dev-hops admin orgs create --name "${PROJECT} E2E" --slug "$PROJECT" --description 'isolated compose E2E' --tier community)"; then
    printf '%s\n' "$output" >&2
    die 'Ops organization provisioning failed'
  fi
  org_id="$(printf '%s\n' "$output" | sed -nE 's/.*id:[[:space:]]*([0-9a-fA-F-]{36}).*/\1/p')"
  [[ "$org_id" =~ ^[0-9a-fA-F-]{36}$ ]] || die 'Ops org provisioning did not return an ID'
  compose exec -T api dev-hops admin bundles assign-org --org-id "$org_id" --feature-key agent_context_runtime --reason 'isolated compose E2E' --expires-days 1 >/dev/null
  token="$(compose exec -T api dev-hops service-credentials create --service acr --scope entitlements:read)"
  [[ "$token" == svc_acr_* ]] || die 'Ops credential provisioning returned an invalid token shape'
  write_secret "$STATE/secrets/ops-token" "$token"
  printf '%s' "$org_id" > "$STATE/org-id"
}

provision_evidence_database() {
  local db
  db="$(ops_clickhouse_database)"
  clickhouse_query "CREATE DATABASE IF NOT EXISTS ${db}" >/dev/null
  compose exec -T api sh -ec "CLICKHOUSE_URI=clickhouse://default:ch@clickhouse:8123/${db} dev-hops migrate clickhouse" >/dev/null
}

# assert_scoped_repository fails closed when a suite's evidence does not resolve to exactly
# one repository row; ClickHouseScopeResolver rejects an ambiguous scope, so two rows would
# surface as an opaque assembly error much later.
assert_scoped_repository() {
  local slug="$1" db org_id scoped_repo_count
  db="$(ops_clickhouse_database)"
  org_id="$(<"$STATE/org-id")"
  scoped_repo_count="$(clickhouse_query "SELECT count() FROM ${db}.repos FINAL WHERE org_id = '${org_id}' AND repo = '${slug}'")"
  if [[ "$scoped_repo_count" != "1" ]]; then
    note "project-scoped repository evidence count=${scoped_repo_count} for ${slug}"
    die 'project-scoped repository evidence provisioning failed'
  fi
}

seed_synthetic_evidence() {
  local db org_id
  db="$(ops_clickhouse_database)"
  org_id="$(<"$STATE/org-id")"
  compose exec -T api sh -ec "CLICKHOUSE_URI=clickhouse://default:ch@clickhouse:8123/${db} dev-hops fixtures generate --sink \"\$CLICKHOUSE_URI\" --db-type clickhouse --repo-name acme/live-e2e --provider synthetic --days 14 --commits-per-day 6 --pr-count 24 --seed 20260219 --with-metrics --with-work-graph" >/dev/null
  clickhouse_query "INSERT INTO ${db}.repos (id, repo, ref, created_at, settings, tags, last_synced, org_id, provider) SELECT generateUUIDv4(), 'acme/live-e2e', 'main', now64(3), NULL, NULL, now64(3), '${org_id}', 'synthetic'" >/dev/null
  assert_scoped_repository 'acme/live-e2e'
}

grant_clickhouse_reader() {
  local db
  db="$(ops_clickhouse_database)"
  compose exec -T clickhouse clickhouse-client --user default --password ch --multiquery <<EOF >/dev/null
CREATE USER IF NOT EXISTS acr_reader IDENTIFIED BY '$(<"$STATE/secrets/clickhouse-password")';
CREATE TABLE IF NOT EXISTS ${db}.acr_e2e_readonly_probe (probe_id UUID) ENGINE = Memory;
GRANT INSERT ON ${db}.acr_e2e_readonly_probe TO acr_reader;
ALTER USER acr_reader SETTINGS readonly = 2;
GRANT SELECT ON ${db}.* TO acr_reader;
EOF
}

bootstrap_ops() {
  provision_ops_control_plane
  provision_evidence_database
  "$ACR_E2E_SEED_HOOK"
  grant_clickhouse_reader
}

create_acr_credential() {
  local token credential_stderr
  credential_stderr="$STATE/acr-credentials.stderr"
  if ! token="$(compose run --rm --no-deps acr-credentials credentials create --org-id "$(<"$STATE/org-id")" --repository-scope "$ACR_E2E_REPOSITORY_SCOPE" --scope context:read,evidence:read --name compose-e2e --actor compose-e2e 2>"$credential_stderr")"; then
    redact_log < "$credential_stderr" >&2 || true
    die 'ACR credential provisioning failed'
  fi
  [[ "$token" == fcacr_* ]] || die 'ACR credential provisioning returned an invalid token shape'
  write_secret "$STATE/secrets/acr-token" "$token"
}

rotate_acr_credential() {
  local old_token credential_id token credential_stderr
  old_token="$(<"$STATE/secrets/acr-token")"
  credential_id="$(compose run --rm --no-deps acr-credentials credentials list --org-id "$(<"$STATE/org-id")" --json | sed -nE 's/.*"credential_id":"([^"]+)".*/\1/p' | head -1)"
  [[ -n "$credential_id" ]] || die 'ACR credential rotation did not return a credential ID'
  credential_stderr="$STATE/acr-credentials-rotate.stderr"
  if ! token="$(compose run --rm --no-deps acr-credentials credentials rotate --org-id "$(<"$STATE/org-id")" --credential-id "$credential_id" --repository-scope "$ACR_E2E_REPOSITORY_SCOPE" --scope context:read,evidence:read --name compose-e2e-rotated --actor compose-e2e --overlap 0s 2>"$credential_stderr")"; then
    redact_log < "$credential_stderr" >&2 || true
    die 'ACR credential rotation failed'
  fi
  [[ "$token" == fcacr_* ]] || die 'ACR credential rotation returned an invalid token shape'
  write_secret "$STATE/secrets/acr-rotated-token" "$token"
  if ! curl --fail --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${token}" "https://localhost:${PORT}/api/v1/agent-context/capabilities" >/dev/null; then
    die 'rotated ACR credential did not authenticate against the direct localhost endpoint'
  fi
  if curl --fail --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer ${old_token}" "https://localhost:${PORT}/api/v1/agent-context/capabilities" >/dev/null 2>&1; then
    die 'previous ACR credential remained valid after immediate rotation'
  fi
}

record_acl_probe() {
  local runtime owner_defaults
  runtime="$(compose exec -T -e "PGPASSWORD=$(<"$STATE/secrets/runtime-password")" postgres psql -h postgres -U "$ACR_RUNTIME_DB_USER" -d "$ACR_DB_NAME" -At -v ON_ERROR_STOP=1 -c "SELECT current_user, current_database(), has_schema_privilege(current_user, 'acr', 'USAGE'), has_schema_privilege(current_user, 'acr', 'CREATE'), has_table_privilege(current_user, 'acr.schema_migrations', 'SELECT'), has_table_privilege(current_user, 'acr.client_credentials', 'SELECT'), has_table_privilege(current_user, 'acr.client_credentials', 'INSERT'), has_table_privilege(current_user, 'acr.client_credentials', 'UPDATE'), has_table_privilege(current_user, 'acr.context_packet_snapshots', 'SELECT'), has_table_privilege(current_user, 'acr.context_packet_snapshots', 'INSERT'), has_table_privilege(current_user, 'acr.context_packet_snapshots', 'UPDATE'), has_table_privilege(current_user, 'acr.context_packet_snapshots', 'DELETE'), has_table_privilege(current_user, 'acr.audit_events', 'INSERT'), has_table_privilege(current_user, 'acr.device_authorizations', 'SELECT'), has_table_privilege(current_user, 'acr.device_authorizations', 'INSERT'), has_table_privilege(current_user, 'acr.device_authorizations', 'UPDATE'), (SELECT count(*) FROM acr.schema_migrations)")"
  owner_defaults="$(compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres psql -h postgres -U "$POSTGRES_USER" -d "$ACR_DB_NAME" -At -v ON_ERROR_STOP=1 -c "SELECT (SELECT bool_and(pg_get_userbyid(relowner) = 'acr_migration') FROM pg_class WHERE relnamespace = 'acr'::regnamespace AND relkind = 'r'), EXISTS (SELECT 1 FROM pg_default_acl d JOIN pg_namespace n ON n.oid = d.defaclnamespace CROSS JOIN LATERAL aclexplode(d.defaclacl) acl JOIN pg_roles grantee ON grantee.oid = acl.grantee WHERE d.defaclrole = (SELECT oid FROM pg_roles WHERE rolname = 'acr_migration') AND n.nspname = 'acr' AND grantee.rolname = 'acr_runtime' AND acl.privilege_type = 'SELECT'), (SELECT pg_get_userbyid(datdba) = 'acr_migration' FROM pg_database WHERE datname = current_database()), (SELECT pg_get_userbyid(nspowner) = 'acr_migration' FROM pg_namespace WHERE nspname = 'acr'), (SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls AND NOT rolinherit FROM pg_roles WHERE rolname = 'acr_runtime')")"
  [[ "$runtime" =~ ^acr_runtime\|acr_.*\|t\|f\|t\|t\|t\|t\|t\|t\|t\|t\|t\|t\|t\|t\|[0-9]+$ ]] || die 'runtime ACL probe did not satisfy the required contract'
  [[ "$owner_defaults" == 't|f|t|t|t' ]] || die 'migration ownership/default ACL probe did not satisfy the required contract'
  note "ACL_PROBE runtime=${runtime} owners_and_defaults=${owner_defaults}"
}

inject_existing_volume_drift() {
  compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres psql -h postgres -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 -c "ALTER ROLE ${ACR_RUNTIME_DB_USER} SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS PASSWORD 'stale-runtime-password'; ALTER ROLE ${ACR_MIGRATION_DB_USER} SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS PASSWORD 'stale-migration-password'; ALTER DATABASE ${ACR_DB_NAME} OWNER TO ${POSTGRES_USER};" >/dev/null
  compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres psql -h postgres -U "$POSTGRES_USER" -d "$ACR_DB_NAME" -v ON_ERROR_STOP=1 -c "ALTER SCHEMA acr OWNER TO ${POSTGRES_USER}; ALTER TABLE acr.client_credentials OWNER TO ${POSTGRES_USER}; GRANT ALL ON SCHEMA acr TO ${ACR_RUNTIME_DB_USER}; GRANT ALL ON ALL TABLES IN SCHEMA acr TO ${ACR_RUNTIME_DB_USER};" >/dev/null
}

mcp_shutdown() {
  local pid="$1" input_fd="$2" output_fd="$3"
  exec {input_fd}>&- || true
  exec {output_fd}<&- || true
  if kill -0 "$pid" 2>/dev/null; then
    kill -INT "$pid" 2>/dev/null || true
  fi
  wait "$pid" 2>/dev/null || true
}

run_mcp() {
  local token="$1" evidence_id initialize_response context_response evidence_response mcp_pid mcp_input mcp_output
  ACR_API_URL="https://localhost:${PORT}" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$STATE/pki/ca.crt" ACR_SIDECAR_VERSION=1.0.0 ACR_SIDECAR_CLIENT_VERSION=1.0.0 "$STATE/acr-mcp" doctor --live > "$STATE/doctor.json"
  grep -q '"status":"ok"' "$STATE/doctor.json" || die 'host MCP doctor did not report healthy TLS capability'
  coproc ACR_MCP { ACR_API_URL="https://localhost:${PORT}" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$STATE/pki/ca.crt" ACR_SIDECAR_VERSION=1.0.0 ACR_SIDECAR_CLIENT_VERSION=1.0.0 "$STATE/acr-mcp" serve; }
  mcp_pid="$ACR_MCP_PID"
  mcp_input="${ACR_MCP[1]}"
  mcp_output="${ACR_MCP[0]}"
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"compose-e2e","version":"1.0.0"}}}' >&"$mcp_input"
  if ! IFS= read -r -t 30 initialize_response <&"$mcp_output" || ! printf '%s\n' "$initialize_response" | jq -e '(.result.protocolVersion | type) == "string"' >/dev/null; then
    mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
    die 'MCP initialize did not return a negotiated protocol version'
  fi
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' "$(jq -cn --arg slug "$ACR_E2E_REPOSITORY_SCOPE" '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:"context_for_task",arguments:{goal:"inspect live evidence",repository:{slug:$slug},scope:{branch:"main"}}}}')" >&"$mcp_input"
  if ! IFS= read -r -t 30 context_response <&"$mcp_output" || ! printf '%s\n' "$context_response" | jq -e '(.result.isError // false) == false and .result.structuredContent.schema_version == "mcp_context_for_task_response.v1"' >/dev/null; then
    mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
    die 'context_for_task did not return a valid MCP response'
  fi
  evidence_id="$(printf '%s\n' "$context_response" | jq -r '[.result.structuredContent | .. | objects | .evidence_ref_ids? | arrays | .[] | select(type == "string")][0] // empty')"
  if [[ -z "$evidence_id" ]]; then
    mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
    die 'context_for_task returned no evidence reference'
  fi
  printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"source_evidence\",\"arguments\":{\"evidence_ref_id\":\"${evidence_id}\"}}}" >&"$mcp_input"
  if ! IFS= read -r -t 30 evidence_response <&"$mcp_output" || ! printf '%s\n' "$evidence_response" | jq -e '(.result.isError // false) == false and .result.structuredContent.schema_version == "mcp_source_evidence_response.v1"' >/dev/null; then
    mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
    die 'source_evidence did not return a valid MCP response'
  fi
  mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
}

# build_host_mcp produces the host-local sidecar the suite drives. The release gate sets
# ACR_MCP_BINARY to an extracted release archive instead, so the artifact customers actually
# install is the one under test rather than a source build of the same tree.
build_host_mcp() {
  if [[ -n "${ACR_MCP_BINARY:-}" ]]; then
    [[ -x "$ACR_MCP_BINARY" ]] || die 'ACR_MCP_BINARY is not an executable sidecar'
    cp "$ACR_MCP_BINARY" "$STATE/acr-mcp"
    chmod 0755 "$STATE/acr-mcp"
    note "using the released sidecar at ${ACR_MCP_BINARY##*/} instead of a source build"
    return 0
  fi
  go build -o "$STATE/acr-mcp" ./cmd/acr-mcp
}

# prepare_stack is the reusable boundary: after it returns, the isolated stack is seeded,
# TLS-ready, holds a scoped ACR credential at $STATE/secrets/acr-token, and has a host-local
# acr-mcp binary at $STATE/acr-mcp. It deliberately performs no client assertions, so suites
# can attach their own test hook without inheriting this file's smoke expectations.
prepare_stack() {
  bootstrap_ops
  compose up -d acr-db-init acr-migrate acr-api acr-tls-proxy >/dev/null
  wait_https_ready
  record_acl_probe
  create_acr_credential
  build_host_mcp
}

start_happy() {
  prepare_stack
  run_mcp "$(<"$STATE/secrets/acr-token")"
  rotate_acr_credential
  run_mcp "$(<"$STATE/secrets/acr-rotated-token")"
  SAFE_BOUNDARY='verified TLS readiness, host-local MCP, non-empty packet and evidence, immediate credential rotation revoked the prior token'
}

expect_failure() {
  local expected_status="$1" expected_pattern="$2" actual_status output redacted
  shift 2
  output="$STATE/${SCENARIO}.failure.log"
  redacted="$STATE/${SCENARIO}.failure.redacted.log"
  set +e
  "$@" >"$output" 2>&1
  actual_status="$?"
  set -e
  redact_log < "$output" > "$redacted"
  if [[ "$actual_status" -ne "$expected_status" ]]; then
    sed -n '1,40p' "$redacted" >&2
    die "expected failure status ${expected_status}, got ${actual_status}"
  fi
  if ! grep -Eq "$expected_pattern" "$redacted"; then
    sed -n '1,40p' "$redacted" >&2
    die "expected failure diagnostics did not match safe classification"
  fi
  note "expected failure diagnostics: $(sed -n '1,4p' "$redacted")"
  return 0
}

expect_clickhouse_readonly_denial() {
  expect_failure 164 'Code: 164.*readonly' compose exec -T clickhouse clickhouse-client --config-file=/run/acr-e2e/clickhouse-client-readonly.xml --secure --host clickhouse --port 9440 --user acr_reader --query "INSERT INTO acr_${PROJECT//-/}_e2e.acr_e2e_readonly_probe (probe_id) VALUES (generateUUIDv4())"
}

expect_typed_http_error() {
  local expected_http_status="$1" expected_code="$2" actual_status http_status response_body
  shift 2
  response_body="$STATE/${SCENARIO}.response.json"
  set +e
  http_status="$("$@" --output "$response_body" --write-out '%{http_code}')"
  actual_status="$?"
  set -e
  if [[ "$actual_status" -ne 0 ]]; then
    die "expected an HTTP response, curl exited ${actual_status}"
  fi
  if [[ "$http_status" != "$expected_http_status" ]]; then
    die "expected HTTP status ${expected_http_status}, got ${http_status}"
  fi
  "$REPO_ROOT/scripts/e2e/validate-error-receipt.sh" "$expected_http_status" "$expected_code" "$http_status" "$response_body" || die "expected HTTP ${expected_http_status} response with typed ${expected_code} error"
}

prepare_acr_database() {
  compose up -d acr-db-init >/dev/null
  compose wait acr-db-init >/dev/null || die 'ACR role initialization failed'
  compose up -d acr-migrate >/dev/null
  compose wait acr-migrate >/dev/null || die 'ACR migration failed'
  compose up -d acr-db-acl >/dev/null
  compose wait acr-db-acl >/dev/null || die 'ACR runtime ACL reconciliation failed'
}

run_failure() {
  local active_credential_id partial_version migration_one_checksum
  case "$SCENARIO" in
    missing-ops-token)
      bootstrap_ops; prepare_acr_database; : > "$STATE/secrets/ops-token"; expect_failure 1 'Dev Health entitlement token file is invalid' compose run --rm --no-deps acr-api; SAFE_BOUNDARY='ACR API rejected the empty Ops token file with its safe entitlement-token classification' ;;
    invalid-ca)
      start_happy; openssl req -x509 -newkey rsa:2048 -nodes -keyout "$STATE/pki/invalid-ca.key" -out "$STATE/pki/invalid-ca.crt" -subj '/CN=acr-e2e-untrusted-ca' -days 1 >/dev/null 2>&1; expect_failure 60 'SSL certificate problem' curl --fail --silent --show-error --cacert "$STATE/pki/invalid-ca.crt" --noproxy '*' "https://localhost:${PORT}/readyz"; SAFE_BOUNDARY='TLS verification rejected the valid unrelated CA with curl certificate exit 60; no insecure option was used' ;;
    revoked-acr-token)
      start_happy; active_credential_id="$(compose run --rm --no-deps acr-credentials credentials list --org-id "$(<"$STATE/org-id")" --json | jq -er '[.[] | select(.revoked_at == null)][0].credential_id')"; compose run --rm --no-deps acr-credentials credentials revoke --org-id "$(<"$STATE/org-id")" --credential-id "$active_credential_id" --actor compose-e2e >/dev/null; expect_typed_http_error 401 invalid_token curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer $(<"$STATE/secrets/acr-rotated-token")" "https://localhost:${PORT}/api/v1/agent-context/capabilities"; SAFE_BOUNDARY='revoked active ACR credential returned exact HTTP 401 typed invalid_token error before protected reads' ;;
    clickhouse-read-denied)
      start_happy; expect_clickhouse_readonly_denial; SAFE_BOUNDARY='read-only ClickHouse user rejected an E2E-only granted insert with exact code 164 before it could mutate evidence' ;;
    migration-failure)
      bootstrap_ops; compose up -d acr-db-init >/dev/null; compose wait acr-db-init >/dev/null || die 'ACR role initialization failed'; migration_one_checksum="$(openssl dgst -sha256 "$REPO_ROOT/migrations/postgres/0001_acr_core.sql")"; migration_one_checksum="${migration_one_checksum##* }"; compose exec -T -e "PGPASSWORD=$(<"$STATE/secrets/migration-password")" postgres psql -h postgres -U "$ACR_MIGRATION_DB_USER" -d "$ACR_DB_NAME" -v ON_ERROR_STOP=1 < "$REPO_ROOT/migrations/postgres/0001_acr_core.sql" >/dev/null; compose exec -T -e "PGPASSWORD=$(<"$STATE/secrets/migration-password")" postgres psql -h postgres -U "$ACR_MIGRATION_DB_USER" -d "$ACR_DB_NAME" -v ON_ERROR_STOP=1 -c "CREATE TABLE acr.schema_migrations (version BIGINT PRIMARY KEY, name TEXT NOT NULL, checksum TEXT, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()); INSERT INTO acr.schema_migrations (version, name, checksum) VALUES (1, '0001_acr_core.sql', '${migration_one_checksum}'); ALTER TABLE acr.agent_episodes ADD CONSTRAINT agent_episodes_org_repo_idempotency_key_key CHECK (false);" >/dev/null; expect_failure 1 'apply migration 2' compose run --rm --no-deps acr-migrate; partial_version="$(compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres psql -h postgres -U "$POSTGRES_USER" -d "$ACR_DB_NAME" -At -v ON_ERROR_STOP=1 -c 'SELECT version FROM acr.schema_migrations ORDER BY version')"; [[ "$partial_version" == '1' ]] || die 'migration failure did not leave the expected partial state'; [[ -z "$(compose ps -q acr-api)" ]] || die 'API started despite failed migration'; SAFE_BOUNDARY='migration 2 failed against deliberate incompatible DDL; migration 1 remains as partial state and API stayed gated' ;;
  esac
  note "expected failure verified: ${SAFE_BOUNDARY}"
  return 1
}

main() {
  parse_args "$@"
  assert_project_unused
  prepare_state
  ensure_image
  render_override
  assert_safe_render

  if [[ "$SCENARIO" == happy ]]; then
    start_happy
  elif [[ "$SCENARIO" == existing-volume ]]; then
    start_happy
    inject_existing_volume_drift
    compose down --remove-orphans >/dev/null
    compose up -d postgres clickhouse valkey pgbouncer mailpit migrate api acr-ops-tls acr-db-init acr-migrate acr-api acr-tls-proxy >/dev/null
    wait_https_ready
    record_acl_probe
    run_mcp "$(<"$STATE/secrets/acr-rotated-token")"
    SAFE_BOUNDARY='existing project-scoped volumes survived a complete application restart and reconciled role, password, ownership, and ACL drift'
  else
    run_failure
  fi
  note "PASS: ${SCENARIO}: ${SAFE_BOUNDARY}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
