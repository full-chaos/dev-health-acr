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
POSTGRES_IMAGE="postgres:18-alpine@sha256:10a5c69a83035ab5239f5620d708520e8b28f5aa1f2b20143e6024532edadfb5"
CLICKHOUSE_IMAGE="clickhouse/clickhouse-server:latest@sha256:739e099abd1157390960a59e4532d21781d8fde57e6e4151cb920ea02c30b1ce"
PGBOUNCER_IMAGE="edoburu/pgbouncer:latest@sha256:8638aa563607256d3c7440437534195f1066a92b68015d89ae9d6b935b444a6d"
VALKEY_IMAGE="valkey/valkey:9-alpine@sha256:bf09d58e96d6b4524a15396b65b242bbe18beebbcc15a04d8ee75ddac62fae0b"
MAILPIT_IMAGE="axllent/mailpit:latest@sha256:9b3a463be15354c0f0eba91697d80e3e0d8a5623ef0e627cacc836bc17fd5231"
NGINX_IMAGE="nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10"

# allow: SIZE_OK — the isolated scenario lifecycle shares one trap-owned state directory.

usage() { printf 'usage: %s --compose <root-compose.yml> --overlay <acr.compose.yml> --project <acr-e2e-name> [--scenario happy|existing-volume|missing-ops-token|invalid-ca|revoked-acr-token|clickhouse-read-denied|migration-failure|bootstrap-rotation]\n' "$0" >&2; }
die() { printf '[compose-e2e] FAIL: %s\n' "$*" >&2; exit 1; }
note() { printf '[compose-e2e] %s\n' "$*" >&2; }
redact_log() { sed -E -e 's/(fcacr|svc_acr|fcwlb)_[[:alnum:]_-]+/REDACTED/g' -e 's#(postgresql?|clickhouse)://[^[:space:]]+#REDACTED_DSN#g'; }

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

  case "$SCENARIO" in happy|existing-volume|missing-ops-token|invalid-ca|revoked-acr-token|clickhouse-read-denied|migration-failure|bootstrap-rotation) ;; *) usage; exit 2 ;; esac
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
  if [[ -f "$STATE/graph.override.yml" ]]; then
    printf '%s\0' -f "$STATE/graph.override.yml"
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
    compose logs --no-color clickhouse migrate go-api-roles go-api auth-provision auth-owner-grants auth-migrate auth-grants auth-fence-init auth-workload-seed auth-workload-bootstrap auth-workload-subject auth-service auth-issuer acr-migrate acr-api acr-tls-proxy 2>&1 | redact_log || true
    for service in postgres pgbouncer clickhouse valkey; do
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
# Mode 644, not 600: these specific files are bind-mounted read-only into the acr-* or
# postgres/clickhouse containers, which run as a UID this script's own process shares nothing
# with -- most pointedly the distroless nonroot UID 65532 baked into the acr-api/acr-migrate
# Dockerfile's `USER 65532:65532`. A 600 file bind-mounted from the host keeps the host's
# owning UID inside the container, so a different-UID process gets EACCES reading it:
# "configuration secret file cannot be read". Docker Desktop's macOS file sharing
# (grpcfuse/virtiofs) does not enforce this the same way a native Linux bind mount does, so
# this only reproduces on a real Linux Docker host -- e.g. a GitHub Actions runner, never a
# local Mac run. fullstack-opencode.sh's own host-read-only credential files (the ACR bearer
# token driving the host-local acr-mcp process) are NOT container-mounted and explicitly
# require mode 600 -- see its own "credential file must be mode 0600" assertion -- so they
# must keep using plain write_secret(), never this one.
write_container_secret() { (umask 022; printf '%s' "$2" > "$1"); }
xml_escape() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g"; }
write_clickhouse_readonly_client_config() {
  local password escaped
  password="$(<"$STATE/secrets/clickhouse-password")"
  escaped="$(printf '%s' "$password" | xml_escape)"
  (umask 077; cat > "$STATE/clickhouse/client-readonly.xml" <<EOF
<clickhouse><password>${escaped}</password></clickhouse>
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
  export OPS_COMMIT
  OPS_COMMIT="$(git -C "$STATE/stage/ops" rev-parse HEAD 2>/dev/null || true)"
  [[ "$OPS_COMMIT" =~ ^[0-9a-f]{40}$ ]] || die 'the ops checkout has no commit: the prover and query-api must be built from one identified commit'
  ln -s "$REPO_ROOT/deploy" "$STATE/stage/deploy"
  "$REPO_ROOT/scripts/deploy/local-pki.sh" --out "$STATE/pki" --dns 'localhost,acr-api,acr-tls-proxy,127.0.0.1'
  PORT="$(free_port)"
  write_container_secret "$STATE/secrets/postgres-password" "$(random_secret)"
  write_container_secret "$STATE/secrets/runtime-password" "$(random_secret)"
  write_container_secret "$STATE/secrets/migration-password" "$(random_secret)"
  write_container_secret "$STATE/secrets/clickhouse-password" "$(random_secret)"
  write_container_secret "$STATE/secrets/evidence-kid" 'acr-e2e-kid'
  write_container_secret "$STATE/secrets/evidence-keys" "acr-e2e-kid=$(random_base64)"
  : > "$STATE/secrets/ops-token"; chmod 600 "$STATE/secrets/ops-token"
  write_clickhouse_readonly_client_config
  write_go_api_envelope_material
  cat > "$STATE/nginx-api.conf" <<'EOF'
events {}
http {
  server { listen 8080; location / { proxy_pass http://acr-api:8080; } }
  server { listen 8443 ssl; ssl_certificate /run/pki/acr.crt; ssl_certificate_key /run/pki/acr.key; location / { proxy_pass http://acr-api:8080; } }
}
EOF
}

# write_go_api_envelope_material creates the per-run Ed25519 pair the Go API edge needs: the
# ops `api` signs principal envelopes with the private key, query-api verifies them against
# the public JWKS. Same openssl derivation as fullstack-opencode.sh's
# write_web_assertion_material. The kid must equal the api's GO_API_ENVELOPE_KEY_ID
# (compose.ci.yml default), or query-api rejects every envelope.
write_go_api_envelope_material() {
  local key public kid
  kid="${GO_API_ENVELOPE_KEY_ID:-go-api-envelope-2026-08}"
  mkdir -p "$STATE/go-api"
  key="$STATE/go-api/envelope-private-key.pem"
  openssl genpkey -algorithm ED25519 -out "$key" 2>/dev/null
  chmod 600 "$key"
  public="$(openssl pkey -in "$key" -pubout -outform DER | python3 -c 'import base64,sys; print(base64.urlsafe_b64encode(sys.stdin.buffer.read()[-32:]).rstrip(b"=").decode())')"
  # 644: query-api runs as the distroless nonroot UID and the file is public key material.
  write_container_secret "$STATE/go-api/envelope-jwks.json" "$(printf '{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"%s","use":"sig","alg":"EdDSA","x":"%s"}]}' "$kid" "$public")"
  export GO_API_ENVELOPE_PRIVATE_KEY GO_API_ENVELOPE_JWKS_FILE="$STATE/go-api/envelope-jwks.json"
  GO_API_ENVELOPE_PRIVATE_KEY="$(<"$key")"
}

ensure_image() {
  if [[ -z "$IMAGE" ]]; then
    local tag="${PROJECT}-acr-api:local"
    # CHAOS-12198: the image version has no default; this test image names one.
    CONTAINER_VERSION=0.0.0-e2e CONTAINER_IMAGE="$tag" "$REPO_ROOT/scripts/container/build.sh" acr-api >/dev/null
    IMAGE="${tag}@$(docker image inspect "$tag" --format '{{.Id}}')"
  fi
  [[ "$IMAGE" == *@sha256:* ]] || die 'ACR_E2E_IMAGE must be an immutable digest reference'
}

# render_go_api_clickhouse_login writes go-api's own ClickHouse login file: ops's generated
# users.d file (docker/clickhouse-users.d/dho_api_ch.xml, exactly the api's grant manifest,
# SELECT on repos included) with its database renamed to this run's isolated one. ops says a
# stack whose CLICKHOUSE_DB is not `default` must mount its own file, or go-api is never ready;
# without the login go-api starts with clickhouse_configured:false and every principal access
# read answers 503 (CHAOS-12183). No grant is widened: only the database name changes.
render_go_api_clickhouse_login() {
  local source="$STATE/stage/ops/docker/clickhouse-users.d/dho_api_ch.xml" target="$STATE/clickhouse/dho_api_ch.xml" db
  db="$(ops_clickhouse_database)"
  [[ -f "$source" ]] || die "the ops checkout has no go-api ClickHouse login file (${source#"$STATE/stage/"})"
  grep -q ' ON default\.' "$source" || die 'the ops go-api ClickHouse login file grants nothing on the default database: its shape changed'
  mkdir -p "$STATE/clickhouse"
  (umask 022; sed "s/ ON default\./ ON ${db}./g" "$source" > "$target")
  ! grep -q ' ON default\.' "$target" || die 'the go-api ClickHouse login file still names the default database'
  grep -q " ON ${db}\.repos<" "$target" || die 'the go-api ClickHouse login file grants no SELECT on repos'
}

render_override() {
  local pg runtime migration ch jwt db postgres_port clickhouse_http_port clickhouse_native_port redis_port api_ch
  pg="$(<"$STATE/secrets/postgres-password")"; runtime="$(<"$STATE/secrets/runtime-password")"; migration="$(<"$STATE/secrets/migration-password")"; ch="$(<"$STATE/secrets/clickhouse-password")"
  jwt="$(random_secret)"
  api_ch="$(random_secret)"
  render_go_api_clickhouse_login
  db="acr_${PROJECT//-/}_e2e"
  write_container_secret "$STATE/secrets/runtime-dsn" "postgres://acr_runtime:${runtime}@postgres:5432/${db}?sslmode=disable"
  write_container_secret "$STATE/secrets/migration-dsn" "postgres://acr_migration:${migration}@postgres:5432/${db}?sslmode=disable"
  write_container_secret "$STATE/secrets/clickhouse-dsn" "clickhouse://acr_reader:${ch}@clickhouse:9000/${db}"
  postgres_port="$(free_port)"
  clickhouse_http_port="$(free_port)"
  clickhouse_native_port="$(free_port)"
  redis_port="$(free_port)"
  # The issuer auth-service signs under and go-api verifies against (ops compose.yml default). Users stay owned by
  # go-api in this slice (go-api's own default), so the auth stack runs with users not owned.
  export AUTH_TOKEN_ISSUER_URL=https://auth.dev-health.internal:8443 AUTH_USERS_OWNED=false
  # The auth one-shots read POSTGRES_DB (ops default postgres); this slice's database is devhealth.
  export POSTGRES_DB=devhealth
  # The auth services are extended from the ops checkout's compose.yml, and Compose interpolates that whole file:
  # its bugsink requires BUGSINK_SECRET_KEY (a per-run throwaway; this slice's own bugsink reads the same name).
  export BUGSINK_SECRET_KEY="${BUGSINK_SECRET_KEY:-$(random_secret)}"
  cat > "$STATE/override.yml" <<EOF
services:
  postgres:
    image: "${POSTGRES_IMAGE}"
    ports: !override []
    environment: { POSTGRES_USER: devhealth, POSTGRES_PASSWORD: "${pg}", POSTGRES_DB: devhealth }
    volumes: ["postgres_data:/var/lib/postgresql/data", "${STATE}/stage/ops/docker/init-extra-dbs.sh:/docker-entrypoint-initdb.d/init-extra-dbs.sh:ro"]
  clickhouse:
    image: "${CLICKHOUSE_IMAGE}"
    ports: !override []
    environment: { CLICKHOUSE_USER: default, CLICKHOUSE_PASSWORD: ch, CLICKHOUSE_DB: default, CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT: "1", DHO_API_CH_PASSWORD: "${api_ch}" }
    volumes: ["clickhouse_data:/var/lib/clickhouse", "${STATE}/clickhouse/client-readonly.xml:/run/acr-e2e/clickhouse-client-readonly.xml:ro", "${STATE}/clickhouse/dho_api_ch.xml:/etc/clickhouse-server/users.d/dho_api_ch.xml:ro"]
    healthcheck: { test: ["CMD-SHELL", "clickhouse-client --host clickhouse --port 9000 --user default --password ch --query 'SELECT 1'"], interval: 5s, timeout: 5s, retries: 12, start_period: 10s }
  pgbouncer: { image: "${PGBOUNCER_IMAGE}" }
  valkey:
    image: "${VALKEY_IMAGE}"
    ports: !override []
  mailpit: { image: "${MAILPIT_IMAGE}" }
  # go-api's own ClickHouse login (render_go_api_clickhouse_login): the principal access read's
  # repository inventory runs on it (CHAOS-12183).
  go-api:
    environment: { JWT_SECRET_KEY: "${jwt}", DEV_HEALTH_CH_API_HOST: clickhouse, DEV_HEALTH_CH_API_PORT: "9000", DEV_HEALTH_CH_API_USER: dho_api_ch, DEV_HEALTH_CH_API_PASSWORD: "${api_ch}", DEV_HEALTH_CH_API_DB: "${db}" }
  # query-api authenticates the Go-served REST routes with the user's edge access token, the HS256
  # JWT the Go api mints with JWT_SECRET_KEY (cmd/query-api edge_verifier_config.go). It needs
  # the same per-run secret as api, or every routed call answers 401 (CHAOS-6326).
  # query-api is built at the commit of the ops checkout (OPS_COMMIT). It serves every
  # registered catalog operation without a routing row, so the suite seeds none.
  query-api:
    build: { args: { COMMIT: "${OPS_COMMIT}" } }
    environment: { GO_API_EDGE_JWT_SECRET: "${jwt}", DEV_HEALTH_ENV: ci }
  traefik: { ports: [] }
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
      ACR_DEV_HEALTH_ENTITLEMENT_URL: http://go-api:8091
      ACR_DATA_QUERY_URL: http://query-api:8091
      ACR_DATA_QUERY_PATH: /query/run-operation
      # The ops internal acr routes accept only the acr-api workload token (ops CHAOS-12977): acr-api exchanges its
      # bootstrap credential at auth-service for it (deploy/compose/acr-auth-workload.compose.yml form).
      ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL: workload
      ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE: /run/workload/acr-api.bootstrap
      ACR_AUTH_WORKLOAD_EXCHANGE_URL: http://auth-service:8095
      ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP: "true"
      ACR_AUTH_WORKLOAD_ISSUER: "${AUTH_TOKEN_ISSUER_URL}"
      ACR_DEVICE_VERIFICATION_URL: "${ACR_E2E_DEVICE_VERIFICATION_URL:-https://device.invalid/acr/device}"
      ACR_EVIDENCE_ID_ACTIVE_KID_FILE: /run/secrets/acr_evidence_active_kid
      ACR_EVIDENCE_ID_KEYS_FILE: /run/secrets/acr_evidence_keys
      # Defaults to the service's own default (60/min), so existing consumers are unchanged.
      # A suite that legitimately issues more reads than a production client would raises it
      # explicitly rather than being throttled into an unrelated-looking failure.
      ACR_REQUESTS_PER_MINUTE: "${ACR_E2E_REQUESTS_PER_MINUTE:-60}"
    volumes: !override
      - { type: volume, source: acr_workload_bootstrap, target: /run/workload, read_only: true }
    secrets: !override [acr_runtime_dsn, acr_clickhouse_dsn, acr_evidence_active_kid, acr_evidence_keys]
    networks: [dev-health, acr_auth_workload]
    depends_on: !override
      postgres: { condition: service_healthy }
      clickhouse: { condition: service_healthy }
      go-api: { condition: service_started }
      acr-db-acl: { condition: service_completed_successfully }
  acr-credentials:
    image: "${IMAGE}"
    environment:
      ACR_ENVIRONMENT: development
      ACR_POSTGRES_DSN_FILE: /run/secrets/acr_runtime_dsn
    secrets: [acr_runtime_dsn]
    networks: [dev-health]
  acr-migrate:
    image: "${IMAGE}"
    environment: !override
      ACR_ENVIRONMENT: development
      ACR_POSTGRES_CONNECTION_KIND: direct
      ACR_POSTGRES_MIGRATION_DSN_FILE: /run/secrets/acr_migration_dsn
    secrets: !override [acr_migration_dsn]
  acr-tls-proxy:
    image: "${NGINX_IMAGE}"
    ports: ["127.0.0.1:${PORT}:8443"]
    volumes: ["${STATE}/pki:/run/pki:ro", "${STATE}/nginx-api.conf:/etc/nginx/nginx.conf:ro"]
    depends_on: { acr-api: { condition: service_started } }
    networks: [dev-health]
  # The acr-api workload credential, in the form of deploy/compose/acr-auth-workload.compose.yml (extended from it).
  acr-workload-volume:
    extends: { file: "${REPO_ROOT}/deploy/compose/acr-auth-workload.compose.yml", service: acr-workload-volume }
    profiles: !reset []
  acr-workload-bootstrap:
    extends: { file: "${REPO_ROOT}/deploy/compose/acr-auth-workload.compose.yml", service: acr-workload-bootstrap }
    profiles: !reset []
    depends_on:
      acr-workload-volume: { condition: service_completed_successfully }
      auth-workload-seed: { condition: service_completed_successfully }
  auth-service:
    networks: [acr_auth_workload]
  # Harness only: exchanges the acr-api bootstrap credential at auth-service and calls a go-api internal acr route
  # with the workload token (wait_go_api, workload_probe). Never started by \`up\` (its own profile).
  acr-workload-probe:
    image: "${NGINX_IMAGE}"
    profiles: [acr-workload-probe]
    restart: "no"
    read_only: true
    user: "65532:65532"
    tmpfs: [/tmp]
    entrypoint: ["sh", "-c"]
    volumes:
      - { type: volume, source: acr_workload_bootstrap, target: /run/workload, read_only: true }
    networks: [dev-health, acr_auth_workload]
volumes:
  acr_workload_bootstrap: {}
networks:
  acr_auth_workload: {}
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
  jq -e '.services["acr-api"].depends_on | keys == ["acr-db-acl","clickhouse","go-api","postgres"]' "$STATE/rendered.json" >/dev/null \
    || die 'ACR API inherited an unexpected local-dev dependency'
  jq -e '(.services["acr-api"].volumes // []) | all(.[]; ((.source // "") | contains("/.acr-dev/") | not))' "$STATE/rendered.json" >/dev/null \
    || die 'ACR API inherited a local-dev bind mount'
  # The one mount every acr-api render carries: its workload bootstrap credential, read-only, from its named volume.
  jq -e '[.services["acr-api"].volumes[]? | select(.target == "/run/workload")] | length == 1 and .[0].type == "volume" and .[0].source == "acr_workload_bootstrap" and .[0].read_only == true' "$STATE/rendered.json" >/dev/null \
    || die 'ACR API is missing its read-only workload credential volume'
  if [[ -f "$STATE/svs.override.yml" ]]; then
    jq -e '[.services["acr-api"].volumes[]?.target] | sort == ["/run/acr-e2e/web-assertions.jwks.json","/run/workload"]' "$STATE/rendered.json" >/dev/null \
      || die 'web agreement added an unexpected ACR API bind mount'
  else
    jq -e '[.services["acr-api"].volumes[]?.target] == ["/run/workload"]' "$STATE/rendered.json" >/dev/null \
      || die 'baseline ACR API retained a bind mount'
  fi
  jq -e '.services["acr-migrate"].environment | keys == ["ACR_ENVIRONMENT","ACR_POSTGRES_CONNECTION_KIND","ACR_POSTGRES_MIGRATION_DSN_FILE"]' "$STATE/rendered.json" >/dev/null \
    || die 'ACR migration environment inherited local-dev configuration'
  jq -e '(.services["go-api"].environment.JWT_SECRET_KEY // "") | length >= 32' "$STATE/rendered.json" >/dev/null \
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

# dho runs the Go operator CLI one-shot in the query-api service image (entrypoint dho) on the
# stack network, so no Python ops image is needed. Postgres is reached directly (not through
# pgbouncer) for the elevated admin/migration verbs; ClickHouse over the native port.
dho() {
  local pg
  pg="$(<"$STATE/secrets/postgres-password")"
  compose run --rm --no-deps -T \
    -e "MIGRATION_DATABASE_URI=postgres://devhealth:${pg}@postgres:5432/devhealth?sslmode=disable" \
    -e "POSTGRES_URI=postgres://devhealth:${pg}@postgres:5432/devhealth?sslmode=disable" \
    -e "CLICKHOUSE_URI=${DHO_CLICKHOUSE_URI:-clickhouse://default:ch@clickhouse:9000/${CLICKHOUSE_DB:-default}}" \
    query-api "$@"
}

# mint_ops_service_token creates the svc_acr_ credential the ops internal listener requires on every
# request (ops CHAOS-9993). It talks to Postgres directly through dho, so it needs no running api, but
# the schema may still be migrating when the services are first started: it retries, bounded by an
# attempt count and a deadline, and logs each retry with the first line of dho's stderr and the final
# outcome. The token is printed on stdout only; nothing else is. principal_access:read is the
# per-call access read acr-api makes for every user-class call (CHAOS-12092; ops CHAOS-12084).
mint_ops_service_token() {
  local attempt=1 max_attempts=30 deadline=$((SECONDS + 180)) token reason errfile
  errfile="$(mktemp)"
  while :; do
    if token="$(dho service-credentials create --service acr --scope entitlements:read --scope llm_config:read --scope principal_access:read 2>"$errfile")" && [[ "$token" == svc_acr_* ]]; then
      rm -f "$errfile"
      note "ops service credential minted (attempt ${attempt})"
      printf '%s' "$token"
      return 0
    fi
    reason="$(head -n 1 "$errfile" | cut -c1-200)"
    if (( attempt >= max_attempts || SECONDS >= deadline )); then
      rm -f "$errfile"
      note "ops service credential not minted after ${attempt} attempts: ${reason:-no error output}"
      return 1
    fi
    note "ops service credential not minted yet (attempt ${attempt}/${max_attempts}): ${reason:-no error output}"
    attempt=$((attempt + 1))
    sleep 2
  done
}

# workload_probe_script is the probe container's own script (acr-workload-probe, run by sh -c): it exchanges the
# acr-api bootstrap credential at auth-service for a workload token (the same form workloadtoken's client sends:
# token exchange, audience dev-health-api) and calls $ACR_PROBE_URL with it. The bootstrap is read from the
# read-only volume and posted from a tmpfs file, never placed on a command line. It prints the response headers,
# then the body after a BODY marker (probe_body), and exits with the call's status; an exchange the auth-service
# refuses prints EXCHANGE-REFUSED and exits 3.
# shellcheck disable=SC2016 # expanded by the probe container's sh, not here.
workload_probe_script='set -eu
enc() { sed -e "s/%/%25/g" -e "s/+/%2B/g" -e "s#/#%2F#g" -e "s/=/%3D/g" -e "s/&/%26/g" -e "s/:/%3A/g"; }
b="$(enc < /run/workload/acr-api.bootstrap)"
printf "grant_type=%s&subject_token=%s&subject_token_type=%s&audience=dev-health-api" \
  "urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange" "$b" \
  "urn%3Adev-health%3Aparams%3Aoauth%3Atoken-type%3Aworkload-bootstrap" > /tmp/form
if ! answer="$(wget -q -O - --header "Content-Type: application/x-www-form-urlencoded" --header "Accept: application/json" --post-file /tmp/form http://auth-service:8095/internal/v1/token/exchange 2>/dev/null)"; then
  echo EXCHANGE-REFUSED; exit 3
fi
t="$(printf "%s" "$answer" | sed -nE "s/.*\"access_token\"[[:space:]]*:[[:space:]]*\"([^\"]+)\".*/\1/p")"
[ -n "$t" ] || { echo EXCHANGE-REFUSED; exit 3; }
set +e
wget -S -O /tmp/body --header "Authorization: Bearer $t" "$ACR_PROBE_URL" 2>&1
rc=$?
printf "\nBODY "; cat /tmp/body 2>/dev/null
exit "$rc"'

# The go-api internal listener and its acr health route, kept apart (host and path) on purpose: one literal URL of
# this shape reads as a generic API key to gitleaks.
go_api_internal=http://go-api:8091
acr_health_path=/api/v1/internal/acr/health

# workload_probe runs workload_probe_script against one go-api internal acr URL; its output is the probe's.
workload_probe() {
  ACR_PROBE_URL="$1" compose run --rm --no-deps -T -e ACR_PROBE_URL acr-workload-probe "$workload_probe_script" 2>&1
}

http_status_of() { sed -nE 's#.*HTTP/[0-9.]+ ([0-9]{3}).*#\1#p' <<<"$1" | tail -n 1; }
probe_body() { sed -n '/^BODY /,$p' <<<"$1" | sed '1s/^BODY //'; }

# wait_go_api polls the Go api internal listener from the compose network with the acr-api workload token, so
# readiness also proves the auth path: the internal acr routes accept no other credential (ops CHAOS-12977). The
# go-api image is distroless (no shell, no wget), so the probe runs in the nginx image already pinned here. The
# failure names the last HTTP status seen, so a refused token (401) reads differently from a listener that is not
# accepting connections or an exchange auth-service refused.
wait_go_api() {
  local attempt output status=''
  for attempt in $(seq 1 90); do
    if output="$(workload_probe "${go_api_internal}${acr_health_path}")"; then
      note "Go ops api internal listener accepted the acr-api workload token (probe ${attempt}/90, HTTP $(http_status_of "$output"))"
      return 0
    fi
    status="$(http_status_of "$output")"
    grep -q EXCHANGE-REFUSED <<<"$output" && status="${status:-exchange refused}"
    if (( attempt % 10 == 0 )); then
      note "Go ops api internal listener not ready (probe ${attempt}/90, last HTTP status: ${status:-none})"
    fi
    sleep 2
  done
  if [[ "$status" == 401 || "$status" == 403 ]]; then
    die "Go ops api internal listener rejected the acr-api workload token (last HTTP status ${status})"
  fi
  die "Go ops api internal listener did not become ready (last HTTP status: ${status:-none, not accepting connections})"
}

# assert_service_credential_refused measures the auth path the other way: the svc_acr_ service credential, the
# harness's credential before ops CHAOS-12977, must get the uniform 401 on the same internal acr route.
assert_service_credential_refused() {
  local output status
  # shellcheck disable=SC2016 # $ACR_PROBE_TOKEN and $ACR_PROBE_URL are expanded by the probe container's sh, not here.
  output="$(ACR_PROBE_TOKEN="$(<"$STATE/ops-probe-token")" ACR_PROBE_URL="${go_api_internal}${acr_health_path}" compose run --rm --no-deps -T -e ACR_PROBE_TOKEN -e ACR_PROBE_URL --entrypoint sh acr-tls-proxy -c 'wget -S -O /dev/null --header "Authorization: Bearer $ACR_PROBE_TOKEN" "$ACR_PROBE_URL"' 2>&1)" || true
  status="$(http_status_of "$output")"
  [[ "$status" == 401 ]] || die "a svc_acr_ service credential on the go-api internal acr route answered ${status:-no status}, want 401"
  note "CONTROL svc_acr_ service credential on the go-api internal acr route: HTTP ${status} (refused)"
}

# provision_acr_workload writes the acr-api bootstrap credential into its volume once (bootstrap-create refuses a
# workload with a live credential, so a later bring-up of the same volume never runs it again).
provision_acr_workload() {
  compose run --rm acr-workload-bootstrap >/dev/null 2>"$STATE/acr-workload-bootstrap.stderr" || {
    redact_log < "$STATE/acr-workload-bootstrap.stderr" >&2 || true
    die 'the acr-api workload bootstrap credential was not created'
  }
  note 'acr-api workload bootstrap credential created'
}

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
  compose up -d postgres valkey pgbouncer mailpit migrate go-api-roles go-api query-api >/dev/null
  token="$(mint_ops_service_token)" || die 'Ops credential provisioning failed'
  write_secret "$STATE/secrets/ops-token" "$token"
  # Host-readable copy for assert_service_credential_refused: secrets/ops-token is chowned to the acr-api UID.
  write_secret "$STATE/ops-probe-token" "$token"
  provision_acr_workload
  wait_go_api
  assert_service_credential_refused
  if ! output="$(dho admin orgs create --name "${PROJECT} E2E" --slug "$PROJECT" --description 'isolated compose E2E' --tier community)"; then
    printf '%s\n' "$output" >&2
    die 'Ops organization provisioning failed'
  fi
  org_id="$(printf '%s\n' "$output" | sed -nE 's/.*id:[[:space:]]*([0-9a-fA-F-]{36}).*/\1/p')"
  [[ "$org_id" =~ ^[0-9a-fA-F-]{36}$ ]] || die 'Ops org provisioning did not return an ID'
  dho admin bundles assign-org --org-id "$org_id" --feature-key agent_context_runtime --reason 'isolated compose E2E' --expires-days 1 >/dev/null
  # This is the one secret file the entitlement client reads through readRestrictedFile /
  # isPrivateSecret (internal/entitlements/file_owner_unix.go), not the more common
  # internal/config.SecretValue path the rest of write_secret()'s files go through.
  # isPrivateSecret requires BOTH zero group/other bits (600, not this function's usual 644 --
  # see write_secret()'s own comment for why 644) AND that the file's owning UID equal
  # acr-api's own euid: the distroless nonroot UID 65532 baked into the Dockerfile's `USER
  # 65532:65532`, never whatever host UID this script itself runs as. `chown` therefore has to
  # run as root. GitHub-hosted runners carry passwordless sudo for exactly this; a local
  # developer's machine may not, so this is best-effort -- the file already had this same
  # UID-mismatch problem on any real Linux Docker host before write_secret() existed, since
  # nothing here can make a bind-mounted host file appear owned by a different container UID
  # without a privileged chown.
  chmod 600 "$STATE/secrets/ops-token"
  sudo -n chown 65532:65532 "$STATE/secrets/ops-token" 2>/dev/null \
    || note 'could not chown ops-token to the acr-api container UID (no passwordless sudo); acr-api may reject it as an unsafe secret file on a real Linux Docker host'
  printf '%s' "$org_id" > "$STATE/org-id"
}

provision_evidence_database() {
  local db
  db="$(ops_clickhouse_database)"
  clickhouse_query "CREATE DATABASE IF NOT EXISTS ${db}" >/dev/null
  DHO_CLICKHOUSE_URI="clickhouse://default:ch@clickhouse:9000/${db}" dho migrate clickhouse upgrade >/dev/null
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
  # The generator refuses a POSTGRES_URI (it writes analytics rows only, ops ff8d2ab3 auth_seeding_not_ported), so
  # it runs with the ClickHouse DSN alone, as fixture-graph.sh's dho_analytics does; dho() also sets POSTGRES_URI.
  compose run --rm --no-deps -T -e "CLICKHOUSE_URI=clickhouse://default:ch@clickhouse:9000/${db}" query-api fixtures generate --sink "clickhouse://default:ch@clickhouse:9000/${db}" --db-type clickhouse --repo-name acme/live-e2e --provider synthetic --days 14 --commits-per-day 6 --pr-count 24 --seed 20260219 --with-metrics --with-work-graph >/dev/null
  clickhouse_query "INSERT INTO ${db}.repos (id, repo, ref, created_at, settings, tags, last_synced, org_id, provider) SELECT generateUUIDv4(), 'acme/live-e2e', 'main', now64(3), NULL, NULL, now64(3), '${org_id}', 'synthetic'" >/dev/null
  assert_scoped_repository 'acme/live-e2e'
}

# probe_ops_principal_access proves go-api's own ClickHouse login serves the principal access
# read end to end (CHAOS-12183): the read for this run's organization, as the svc_acr_
# credential, answers acr_principal_access.v1 with the seeded repositories as its inventory
# (kind repos, not none), which go-api reads with SELECT on repos. Any user id works: the
# inventory is the organization's. It runs after the seed hook, whatever corpus that seeds.
probe_ops_principal_access() {
  local org_id user_id output body
  org_id="$(<"$STATE/org-id")"
  user_id="00000000-0000-4000-8000-000000000000"
  if ! output="$(workload_probe "${go_api_internal}/api/v1/internal/acr/orgs/${org_id}/principals/${user_id}/access")"; then
    printf '%s\n' "$output" | redact_log >&2
    die 'the Go ops api principal access read failed (is its ClickHouse login configured?)'
  fi
  body="$(probe_body "$output")"
  if ! jq -e '.schema_version == "acr_principal_access.v1" and .repository_scope.kind == "repos" and (.repository_scope.repos | length) > 0' <<<"$body" >/dev/null; then
    printf '%s\n' "$body" | redact_log >&2
    die 'the Go ops api principal access read did not serve the seeded repository inventory'
  fi
  note "go-api principal access read served the repository inventory (repos: $(jq -r '.repository_scope.repos | length' <<<"$body"))"
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
  probe_ops_principal_access
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

# start_acr_after_go_api starts acr-api only once the Go ops api internal listener answers. acr-api
# checks entitlement at boot and exits when the listener is down; `compose up acr-api` may recreate
# go-api first (service_started does not wait for its listener), and a recent ops change delays the
# listener by the OTLP startup flush, so acr-api lost that race and nginx then logged
# "host not found in upstream acr-api" (CHAOS-11890).
ensure_go_api_ready() {
  compose up -d go-api >/dev/null
  wait_go_api
}

# up_acr_services is the ONLY way a harness script starts acr-api (CHAOS-11890). It proves the ops
# listener answers, then starts the named services with --no-recreate so compose cannot recreate
# go-api (or migrate) as a side effect of `up acr-api`: run 40960897433 recreated go-api in the same
# `up` that started acr-api, so the readiness proof described a container that no longer existed and
# acr-api exited with "initial entitlement readiness check: Dev Health entitlement service
# unavailable". The go-api container id is compared across the start; a change is a loud failure.
up_acr_services() {
  local before after
  ensure_go_api_ready
  before="$(compose ps -q go-api)"
  [[ -n "$before" ]] || die 'go-api is not running after its readiness probe passed'
  compose up -d --no-recreate "$@" >/dev/null
  after="$(compose ps -q go-api)"
  [[ "$before" == "$after" ]] || die "go-api was recreated while starting ($*): container ${before:0:12} -> ${after:0:12}; the readiness proof no longer describes it"
  # A restart keeps the container id, so the id check alone cannot see it. acr-api exits at boot when
  # entitlement is unavailable; require it to survive a settle window and re-prove the listener.
  case " $* " in *' acr-api '*) assert_acr_api_survives_boot ;; esac
}

assert_acr_api_survives_boot() {
  local attempt state
  for attempt in 1 2 3 4 5; do
    state="$(compose ps -a --format '{{.State}}' acr-api 2>/dev/null | head -1)"
    if [[ "$state" != 'running' ]]; then
      compose logs --no-color --tail 20 acr-api >&2 || true
      die "acr-api is ${state:-absent} at settle check ${attempt}/5 after start (boot entitlement check failed?)"
    fi
    sleep 2
  done
  wait_go_api
}

start_acr_after_go_api() {
  up_acr_services acr-db-init acr-migrate acr-api acr-tls-proxy
}

# prepare_stack is the reusable boundary: after it returns, the isolated stack is seeded,
# TLS-ready, holds a scoped ACR credential at $STATE/secrets/acr-token, and has a host-local
# acr-mcp binary at $STATE/acr-mcp. It deliberately performs no client assertions, so suites
# can attach their own test hook without inheriting this file's smoke expectations.
prepare_stack() {
  bootstrap_ops
  start_acr_after_go_api
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
  expect_failure 164 'Code: 164.*readonly' compose exec -T clickhouse clickhouse-client --config-file=/run/acr-e2e/clickhouse-client-readonly.xml --host clickhouse --port 9000 --user acr_reader --query "INSERT INTO acr_${PROJECT//-/}_e2e.acr_e2e_readonly_probe (probe_id) VALUES (generateUUIDv4())"
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

run_oneshot() {
  local service="$1" failure="$2" attempts=0 row state exit_code
  compose up -d "$service" >/dev/null
  while :; do
    row="$(compose ps -a --format '{{.State}} {{.ExitCode}}' "$service" 2>/dev/null | head -1)"
    state="${row%% *}"
    exit_code="${row##* }"
    if [[ "$state" == 'exited' ]]; then
      [[ "$exit_code" == '0' ]] && return 0
      break
    fi
    attempts=$((attempts + 1))
    [[ "$attempts" -lt 90 ]] || break
    sleep 2
  done
  compose logs --no-color --tail 50 "$service" 2>&1 | redact_log || true
  die "$failure (state=${state:-none} exit=${exit_code:-none})"
}

prepare_acr_database() {
  run_oneshot acr-db-init 'ACR role initialization failed'
  run_oneshot acr-migrate 'ACR migration failed'
  run_oneshot acr-db-acl 'ACR runtime ACL reconciliation failed'
}

# expect_workload_binding_disabled_refused disables the acr-api workload in the registry (auth-migrate workloads
# disable) and requires the go-api internal acr route to refuse it: auth-service refuses the exchange, or go-api
# answers 401 once its 30 s registry cache turns over (ops acr_workload_source.go acrSubjectTTL).
expect_workload_binding_disabled_refused() {
  local attempt output status
  compose run --rm --no-deps auth-workload-seed workloads disable -workload acr-api -apply >/dev/null 2>"$STATE/workload-disable.stderr" || {
    redact_log < "$STATE/workload-disable.stderr" >&2 || true
    die 'the acr-api workload could not be disabled'
  }
  for attempt in $(seq 1 30); do
    output="$(workload_probe "${go_api_internal}${acr_health_path}")" || true
    status="$(http_status_of "$output")"
    if grep -q EXCHANGE-REFUSED <<<"$output"; then
      note "CONTROL disabled acr-api workload: auth-service refused the exchange (probe ${attempt})"
      return 0
    fi
    if [[ "$status" == 401 ]]; then
      note "CONTROL disabled acr-api workload: go-api internal acr route HTTP 401 (probe ${attempt})"
      return 0
    fi
    sleep 2
  done
  die "the disabled acr-api workload was still accepted (last HTTP status ${status:-none})"
}

run_failure() {
  local active_credential_id partial_version migration_one_checksum
  case "$SCENARIO" in
    missing-ops-token)
      bootstrap_ops; prepare_acr_database; expect_failure 1 'initial entitlement readiness check' compose run --rm --no-deps -e ACR_AUTH_WORKLOAD_BOOTSTRAP_FILE=/run/workload/absent.bootstrap acr-api; note 'CONTROL missing-ops-token: acr-api with no workload bootstrap credential refused to start'; SAFE_BOUNDARY='ACR API with no workload bootstrap credential refused to start at its entitlement readiness check' ;;
    invalid-ca)
      start_happy; openssl req -x509 -newkey rsa:2048 -nodes -keyout "$STATE/pki/invalid-ca.key" -out "$STATE/pki/invalid-ca.crt" -subj '/CN=acr-e2e-untrusted-ca' -days 1 >/dev/null 2>&1; expect_failure 60 'SSL certificate problem' curl --fail --silent --show-error --cacert "$STATE/pki/invalid-ca.crt" --noproxy '*' "https://localhost:${PORT}/readyz"; SAFE_BOUNDARY='TLS verification rejected the valid unrelated CA with curl certificate exit 60; no insecure option was used' ;;
    revoked-acr-token)
      start_happy; active_credential_id="$(compose run --rm --no-deps acr-credentials credentials list --org-id "$(<"$STATE/org-id")" --json | jq -er '[.[] | select(.revoked_at == null)][0].credential_id')"; compose run --rm --no-deps acr-credentials credentials revoke --org-id "$(<"$STATE/org-id")" --credential-id "$active_credential_id" --actor compose-e2e >/dev/null; expect_typed_http_error 401 invalid_token curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer $(<"$STATE/secrets/acr-rotated-token")" "https://localhost:${PORT}/api/v1/agent-context/capabilities"; expect_workload_binding_disabled_refused; SAFE_BOUNDARY='revoked active ACR credential returned exact HTTP 401 typed invalid_token error before protected reads; the disabled acr-api workload was refused on the go-api internal acr route' ;;
    clickhouse-read-denied)
      start_happy; expect_clickhouse_readonly_denial; SAFE_BOUNDARY='read-only ClickHouse user rejected an E2E-only granted insert with exact code 164 before it could mutate evidence' ;;
    migration-failure)
      bootstrap_ops; run_oneshot acr-db-init 'ACR role initialization failed'; migration_one_checksum="$(openssl dgst -sha256 "$REPO_ROOT/migrations/postgres/0001_acr_core.sql")"; migration_one_checksum="${migration_one_checksum##* }"; compose exec -T -e "PGPASSWORD=$(<"$STATE/secrets/migration-password")" postgres psql -h postgres -U "$ACR_MIGRATION_DB_USER" -d "$ACR_DB_NAME" -v ON_ERROR_STOP=1 < "$REPO_ROOT/migrations/postgres/0001_acr_core.sql" >/dev/null; compose exec -T -e "PGPASSWORD=$(<"$STATE/secrets/migration-password")" postgres psql -h postgres -U "$ACR_MIGRATION_DB_USER" -d "$ACR_DB_NAME" -v ON_ERROR_STOP=1 -c "CREATE TABLE acr.schema_migrations (version BIGINT PRIMARY KEY, name TEXT NOT NULL, checksum TEXT, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()); INSERT INTO acr.schema_migrations (version, name, checksum) VALUES (1, '0001_acr_core.sql', '${migration_one_checksum}'); ALTER TABLE acr.agent_episodes ADD CONSTRAINT agent_episodes_org_repo_idempotency_key_key CHECK (false);" >/dev/null; expect_failure 1 'apply migration 2' compose run --rm --no-deps acr-migrate; partial_version="$(compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres psql -h postgres -U "$POSTGRES_USER" -d "$ACR_DB_NAME" -At -v ON_ERROR_STOP=1 -c 'SELECT version FROM acr.schema_migrations ORDER BY version')"; [[ "$partial_version" == '1' ]] || die 'migration failure did not leave the expected partial state'; [[ -z "$(compose ps -q acr-api)" ]] || die 'API started despite failed migration'; SAFE_BOUNDARY='migration 2 failed against deliberate incompatible DDL; migration 1 remains as partial state and API stayed gated' ;;
  esac
  note "expected failure verified: ${SAFE_BOUNDARY}"
  return 1
}

# --- bootstrap-rotation ---------------------------------------------------------------------------------------------------
# The acr-api workload bootstrap credential is rotated with a bounded overlap, delivered, then the old one is revoked, all
# under load. It runs about 40 minutes with real waits (the overlap is real), so it is a manual or dispatch scenario and is
# not in the PR smoke set. The order is the operations runbook (docs/operations.md, acr-api workload credential on
# Compose): rotate into a .next file, deliver it over the live file (mv -f as uid 65532) inside the overlap, let the old
# credential expire, revoke it.
#   S0 start_happy; C1 is the live credential | S1 baseline | S2 rotate (dry run first) -overlap 10m: C2 in .next (T0)
#   S3 T0+60 direct cells and bound cells | S4 T0+180 deliver (TS) | S5 T0+630 C1 refused expired
#   S6 T0+660 revoke C1 (dry run, apply, repeat) | S7 post window to T0+1020 | S8 NEG: C1 put back, then C2 put back
# Probes: P-ent (an fcacr_ capabilities call every 5 s: each one needs an entitlement lookup, which acr-api sends with its
# workload token and which fails closed with 503 when it has no token: the token_unavailable outcome), A-rows
# (auth.security_audit_events auth.workload.token_issued: client_ip, credential_id; acr-api rows by its container
# addresses), direct cells (acr-workload-probe exchanges one named credential file; the refusal reason is read from the
# auth-service log). Credential values never leave the volume (cp and mv run as uid 65532 in the volume one-shot image);
# the records hold credential ids, counts and times only. T0 is the end of the rotate apply (host clock).
ROTATION_OVERLAP=10m
ROTATION_OVERLAP_S=600
ROTATION_BASE_S=300
ROTATION_DELIVER_AT_S=180
ROTATION_EXPIRED_AT_S=630
ROTATION_REVOKE_AT_S=660
ROTATION_POST_END_S=1020
ROTATION_CUT_BOUND_S=200
ROTATION_NEG_REFUSE_BOUND_S=330
ROTATION_NEG_FAIL_BOUND_S=360
ROTATION_NEG_RECOVER_BOUND_S=90
ROTATION_NEG_STEADY_S=60
ROTATION_LOAD_EVERY_S=5
ROTATION_LIVE=/run/workload/acr-api.bootstrap
ROTATION_NEXT=/run/workload/acr-api.bootstrap.next
ROTATION_HOLD_C1=/run/workload/acr-api.bootstrap.drill-c1
ROTATION_HOLD_C2=/run/workload/acr-api.bootstrap.drill-c2
ROTATION_SPARE=/run/workload/acr-api.bootstrap.drill-spare
ROTATION_RECORDS=""
ROTATION_ACR_IPS=""
ROTATION_LOAD_PID=""

# The direct cell's script: workload_probe_script with the credential file named by $ACR_PROBE_BOOTSTRAP (a file in the
# read-only volume), so a cell can present C1 or C2 whichever one is live.
# shellcheck disable=SC2016 # expanded by the probe container's sh, not here.
rotation_probe_path='"$ACR_PROBE_BOOTSTRAP"'
rotation_probe_script="${workload_probe_script//\/run\/workload\/acr-api.bootstrap/$rotation_probe_path}"

rotation_now() { date -u +%s; }
rotation_at() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }
rotation_sleep_until() {
  local wait
  wait=$(( $1 - $(rotation_now) ))
  if (( wait > 0 )); then sleep "$wait"; fi
}
# rotation_after <label> <time> <base>: "<label>+<time - base> s", or "none" when the time was never seen.
rotation_after() {
  if [[ -n "$2" ]]; then printf '%s+%s s' "$1" "$(( $2 - $3 ))"; else printf 'none'; fi
}
rotation_note() {
  note "rotation: $*"
  printf '%s %s\n' "$(rotation_at "$(rotation_now)")" "$*" >> "$ROTATION_RECORDS/steps.log"
}
# rotation_rule <n> <verdict line>: one PASS, FAIL or VOID line per rule, on stderr and in rules.md.
rotation_rule() { printf 'RULE %s: %s\n' "$1" "$2" | tee -a "$ROTATION_RECORDS/rules.md" >&2; }

# rotation_verb <record name> <auth-migrate args...>: one auth-migrate workloads verb in the acr-workload-bootstrap
# one-shot (the credential volume read-write, uid 65532). Its output names credential ids, never a value; it is the
# record verb-<name>.log. Prints the exit status.
rotation_verb() {
  local name="$1" status
  shift
  set +e
  compose run --rm --no-deps -T acr-workload-bootstrap "$@" > "$ROTATION_RECORDS/verb-${name}.log" 2>&1
  status=$?
  set -e
  printf '%s' "$status"
}

# rotation_volume <sh script>: one file step inside the credential volume as its owner (uid 65532), in the volume
# one-shot image (no network). No value is read out of the volume.
rotation_volume() {
  compose run --rm --no-deps -T --user 65532:65532 --entrypoint sh acr-workload-volume -c "$1" 2>"$STATE/rotation-volume.stderr"
}
# rotation_deliver is the runbook's delivery: the rotated file replaces the live one in one rename, as its owner.
rotation_deliver() { rotation_volume "mv -f ${ROTATION_NEXT} ${ROTATION_LIVE}"; }
# rotation_put_back <held file>: the NEG cells put a held credential back over the live file, also in one rename.
rotation_put_back() { rotation_volume "cp -p $1 ${ROTATION_SPARE} && mv -f ${ROTATION_SPARE} ${ROTATION_LIVE}"; }

rotation_sql() {
  compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres psql -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -v ON_ERROR_STOP=1 -c "$1"
}
# rotation_rows <file>: every auth.workload.token_issued row as epoch|client_ip|credential_id.
rotation_rows() {
  rotation_sql "SELECT extract(epoch FROM occurred_at)::bigint || '|' || coalesce(host(client_ip), 'null') || '|' || coalesce(attributes->>'credential_id', 'null') FROM auth.security_audit_events WHERE event_type = 'auth.workload.token_issued' ORDER BY id" > "$1"
}
# rotation_audit_counts: bootstrap_created bootstrap_rotated bootstrap_revoked all-bootstrap rows, then the outbox
# workload.bootstrap_changed rows, on one line.
rotation_audit_counts() {
  local audit outbox
  audit="$(rotation_sql "SELECT count(*) FILTER (WHERE event_type = 'auth.workload.bootstrap_created') || ' ' || count(*) FILTER (WHERE event_type = 'auth.workload.bootstrap_rotated') || ' ' || count(*) FILTER (WHERE event_type = 'auth.workload.bootstrap_revoked') || ' ' || count(*) FILTER (WHERE event_type LIKE 'auth.workload.bootstrap_%') FROM auth.security_audit_events")"
  outbox="$(rotation_sql "SELECT count(*) FROM auth.auth_outbox_events WHERE event_type = 'workload.bootstrap_changed'")"
  printf '%s %s' "$audit" "$outbox"
}

# rotation_refusals_since <epoch> <reason>: auth-service "workload exchange refused" lines for a bootstrap subject with
# that reason, logged at or after epoch.
rotation_refusals_since() {
  { compose logs --no-color --no-log-prefix --since "$(rotation_at "$1")" auth-service 2>/dev/null || true; } \
    | grep -F 'workload exchange refused' | grep -E 'subject"?[:=]"?bootstrap' | grep -Ec "reason\"?[:=]\"?${2}([\"[:space:],}]|$)" || true
}
# rotation_lookup_failures <from epoch> <to epoch>: acr-api "entitlement lookup failed" lines in the window.
rotation_lookup_failures() {
  { compose logs --no-color --no-log-prefix --since "$(rotation_at "$1")" --until "$(rotation_at "$2")" acr-api 2>/dev/null || true; } \
    | grep -Fc 'entitlement lookup failed' || true
}

# rotation_cell <label> <credential file> <want: 200|refused> <reason|->: three direct exchanges with one credential
# file. 200: each exchange accepted and the go-api internal acr route answers 200 with the token. refused: each exchange
# refused, and the auth-service log names the reason on at least three refusals since the cell started. Prints PASS or
# FAIL; the line is in cells.md.
rotation_cell() {
  local label="$1" file="$2" want="$3" reason="$4" mark output status codes="" verdict=PASS lines=-
  mark="$(rotation_now)"
  for _ in 1 2 3; do
    output="$(ACR_PROBE_URL="${go_api_internal}${acr_health_path}" ACR_PROBE_BOOTSTRAP="$file" compose run --rm --no-deps -T -e ACR_PROBE_URL -e ACR_PROBE_BOOTSTRAP acr-workload-probe "$rotation_probe_script" 2>&1)" || true
    if grep -q EXCHANGE-REFUSED <<<"$output"; then
      status=refused
    else
      status="$(http_status_of "$output")"
      status="${status:-none}"
    fi
    codes="${codes} ${status}"
    [[ "$status" == "$want" ]] || verdict=FAIL
    sleep 1
  done
  if [[ "$reason" != - ]]; then
    sleep 2
    lines="$(rotation_refusals_since "$mark" "$reason")"
    (( lines >= 3 )) || verdict=FAIL
  fi
  printf '%s %s %s: results%s; auth-service refusals with reason %s: %s -> %s\n' "$(rotation_at "$mark")" "$label" "${file##*/}" "$codes" "$reason" "$lines" "$verdict" >> "$ROTATION_RECORDS/cells.md"
  printf '%s' "$verdict"
}

# rotation_load <log> <flag file> <parent pid>: P-ent. One capabilities call every ROTATION_LOAD_EVERY_S s with the
# scoped fcacr_ credential (sent from a 0600 header file, never an argument), one "epoch status" line each, while the
# flag file exists and the parent runs.
rotation_load() {
  local log="$1" flag="$2" parent="$3" status
  while [[ -f "$flag" ]] && kill -0 "$parent" 2>/dev/null; do
    status="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 10 --cacert "$STATE/pki/ca.crt" --noproxy '*' -H "$ACR_CLIENT_VERSION_HEADER" -H @"$STATE/rotation-auth.header" "https://localhost:${PORT}/api/v1/agent-context/capabilities" 2>/dev/null)" || true
    printf '%s %s\n' "$(rotation_now)" "${status:-000}" >> "$log"
    sleep "$ROTATION_LOAD_EVERY_S"
  done
}

# --- the rule judges: pure functions of the records, so test-compose.sh can feed them planted inputs ---------------------
# rotation_acr_rows <rows file> <addresses>: the token_issued rows whose client_ip is one of acr-api's addresses.
rotation_acr_rows() { awk -F'|' -v ips=" $2 " 'index(ips, " " $2 " ")' "$1"; }

# Rule 1 <P-ent log> <from> <to> <lookup failures>: every capabilities call in the window answered 200, at least one call
# per (interval + 1) s, and acr-api logged no failed entitlement lookup (token_unavailable fails a lookup closed).
rotation_judge_load() {
  local log="$1" from="$2" to="$3" failures="$4" minimum n bad hist verdict=FAIL
  minimum=$(( (to - from) / (ROTATION_LOAD_EVERY_S + 1) ))
  read -r n bad hist < <(awk -v a="$from" -v b="$to" 'NF == 2 && $1 >= a && $1 <= b { n++; if ($2 != "200") f++; h[$2]++ }
    END { s = ""; for (k in h) s = s k "=" h[k] ","; print n + 0, f + 0, (s == "" ? "none" : s) }' "$log")
  if (( n >= minimum && bad == 0 && failures == 0 )); then verdict=PASS; fi
  printf '%s: capabilities calls %s..%s: %s (want >= %s), non-200 %s (%s); acr-api entitlement lookup failures %s (want 0)' \
    "$verdict" "$(rotation_at "$from")" "$(rotation_at "$to")" "$n" "$minimum" "$bad" "$hist" "$failures"
}

# Rule 2 <rows> <addresses> <C1> <T0> <overlap s>: acr-api exchanged C1 inside the overlap (T0, T0+overlap).
rotation_judge_overlap_use() {
  local rows="$1" ips="$2" c1="$3" t0="$4" overlap="$5" n nulls verdict=FAIL
  n="$(rotation_acr_rows "$rows" "$ips" | awk -F'|' -v c="$c1" -v a="$t0" -v b=$((t0 + overlap)) '$3 == c && $1 > a && $1 < b' | wc -l | tr -d ' ')"
  nulls="$(awk -F'|' '$2 == "null"' "$rows" | wc -l | tr -d ' ')"
  if (( n >= 1 )); then verdict=PASS; fi
  if (( nulls > 0 )); then verdict="VOID(client_ip null x${nulls})"; fi
  printf '%s: acr-api exchanges with C1 inside the overlap (T0..T0+%s): %s (want >= 1)' "$verdict" "$overlap" "$n"
}

# Rule 3 <rows> <addresses> <C1> <C2> <TS> <T0> <overlap s> <bound s> <until>: the first acr-api exchange with C2 at or
# after the delivery TS is within bound s and before the overlap ends; acr-api made no C1 exchange from the end of the
# overlap to until (the NEG start).
rotation_judge_cutover() {
  local rows="$1" ips="$2" c1="$3" c2="$4" ts="$5" t0="$6" overlap="$7" bound="$8" until="$9" first late verdict=FAIL after=none
  first="$(rotation_acr_rows "$rows" "$ips" | awk -F'|' -v c="$c2" -v a="$ts" '$3 == c && $1 >= a { print $1; exit }')"
  late="$(rotation_acr_rows "$rows" "$ips" | awk -F'|' -v c="$c1" -v a=$((t0 + overlap)) -v b="$until" '$3 == c && $1 >= a && $1 < b' | wc -l | tr -d ' ')"
  if [[ -n "$first" ]]; then
    after="TS+$((first - ts)) s"
    if (( first - ts <= bound && first < t0 + overlap && late == 0 )); then verdict=PASS; fi
  fi
  printf '%s: first acr-api exchange with C2 at %s (bound %s s, before T0+%s); acr-api C1 exchanges after the overlap: %s' \
    "$verdict" "$after" "$bound" "$overlap" "$late"
}

# Rule 4 <x1..x5>: the direct cells (C1, C2 inside the overlap; C1 expired; C1 revoked; C2 after the revoke).
rotation_judge_cells() {
  local verdict=PASS cell
  for cell in "$@"; do [[ "$cell" == PASS ]] || verdict=FAIL; done
  printf '%s: C1/C2 inside the overlap 200 (%s/%s); C1 after the overlap refused expired (%s); after the revoke C1 refused revoked (%s), C2 200 (%s)' \
    "$verdict" "$1" "$2" "$3" "$4" "$5"
}

# Rule 5 <name=verdict...>: the bounds of the verbs and the file.
rotation_judge_bounds() {
  local verdict=PASS item detail=""
  for item in "$@"; do
    [[ "${item#*=}" == PASS ]] || verdict=FAIL
    detail="${detail} ${item}"
  done
  printf '%s:%s' "$verdict" "$detail"
}

# Rule 6 <created> <rotated> <revoked> <outbox>: the deltas from before the rotate to the end of S7.
rotation_judge_counts() {
  local verdict=FAIL
  if [[ "$1" == 0 && "$2" == 1 && "$3" == 1 && "$4" == 2 ]]; then verdict=PASS; fi
  printf '%s: audit bootstrap_created +%s, bootstrap_rotated +%s, bootstrap_revoked +%s (want +0, +1, +1); outbox workload.bootstrap_changed +%s (want +2)' \
    "$verdict" "$1" "$2" "$3" "$4"
}

# Rule 7 <TN> <TR> <TF> <TC> <TOK> <non-200 after recovery> <acr-api C1 tokens after TN>: C1 put back is refused
# (revoked) within the refusal bound and acr-api fails closed within the failure bound; C2 put back recovers within the
# recovery bound and stays up.
rotation_judge_neg() {
  local tn="$1" tr="$2" tf="$3" tc="$4" tok="$5" after="$6" c1neg="$7" verdict=FAIL
  if [[ -n "$tr" && -n "$tf" && -n "$tok" && "$after" == 0 && "$c1neg" == 0 ]] \
    && (( tr - tn <= ROTATION_NEG_REFUSE_BOUND_S && tf - tn <= ROTATION_NEG_FAIL_BOUND_S && tok - tc <= ROTATION_NEG_RECOVER_BOUND_S )); then
    verdict=PASS
  fi
  printf '%s: C1 put back: refused revoked at %s (bound %s s), capabilities non-200 from %s (bound %s s), acr-api C1 tokens after TN %s; C2 put back: 200 at %s (bound %s s), non-200 in the next %s s: %s' \
    "$verdict" "$(rotation_after TN "$tr" "$tn")" "$ROTATION_NEG_REFUSE_BOUND_S" "$(rotation_after TN "$tf" "$tn")" "$ROTATION_NEG_FAIL_BOUND_S" "$c1neg" \
    "$(rotation_after TC "$tok" "$tc")" "$ROTATION_NEG_RECOVER_BOUND_S" "$ROTATION_NEG_STEADY_S" "$after"
}

# Rule 8 <files...>: no bootstrap credential value (the workload bootstrap prefix and a value body) in any log or record.
rotation_judge_leak() {
  local hits prefix=fcwlb
  hits="$(cat "$@" 2>/dev/null | grep -Ec "${prefix}_[A-Za-z0-9_-]{8,}" || true)"
  if [[ "$hits" == 0 ]]; then
    printf 'PASS: leak scan of %s files (service logs and records): 0 bootstrap values' "$#"
  else
    printf 'FAIL: leak scan of %s files found %s lines with a bootstrap value' "$#" "$hits"
  fi
}

run_bootstrap_rotation() {
  local tb t0 ts tn tr="" tf="" tc tok="" trv c1 c2 c1_exp c1_exp_s rc rc61 rc2 left first after c1neg failures
  local x1 x2 x3 x4 x5 b61 b2 ba r5a r5b rv rr dl ex counts0 counts1 counts2 load_flag mode
  ROTATION_RECORDS="${ACR_E2E_RECORDS:-$STATE/rotation}"
  mkdir -p "$ROTATION_RECORDS"
  [[ -z "$(ls -A "$ROTATION_RECORDS")" ]] || die 'the rotation records directory must be new or empty'
  start_happy
  ROTATION_ACR_IPS="$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$(compose ps -q acr-api)")"
  ROTATION_ACR_IPS="${ROTATION_ACR_IPS% }"
  [[ -n "$ROTATION_ACR_IPS" ]] || die 'acr-api has no container address'
  rotation_note "S0: acr-api addresses ${ROTATION_ACR_IPS}"

  # S0: C1 is the one live credential; P-ent starts.
  rc="$(rotation_verb dry0 workloads bootstrap-rotate -workload acr-api -out "$ROTATION_NEXT" -overlap "$ROTATION_OVERLAP")"
  if [[ "$rc" != 0 ]] || ! grep -q 'dry run: workload=acr-api live=1 ' "$ROTATION_RECORDS/verb-dry0.log"; then die 'rotation S0: the rotate dry run does not list one live credential'; fi
  c1="$(sed -nE 's/^live credential=([0-9a-f-]{36}) .*/\1/p' "$ROTATION_RECORDS/verb-dry0.log")"
  [[ "$c1" =~ ^[0-9a-f-]{36}$ ]] || die 'rotation S0: no C1 credential id'
  (umask 077; printf 'Authorization: Bearer %s\n' "$(<"$STATE/secrets/acr-rotated-token")" > "$STATE/rotation-auth.header")
  load_flag="$STATE/rotation-load.on"
  : > "$load_flag"
  rotation_load "$ROTATION_RECORDS/pent.log" "$load_flag" "$$" &
  ROTATION_LOAD_PID=$!
  rotation_note "S0: C1 credential=${c1}; P-ent pid ${ROTATION_LOAD_PID}"
  sleep 60
  rotation_rows "$STATE/rotation-rows.txt"
  [[ "$(awk '$2 == "200"' "$ROTATION_RECORDS/pent.log" | wc -l)" -ge 3 ]] || die 'rotation pre-check: fewer than 3 capabilities calls answered 200'
  [[ "$(rotation_acr_rows "$STATE/rotation-rows.txt" "$ROTATION_ACR_IPS" | awk -F'|' -v c="$c1" '$3 == c' | wc -l)" -ge 1 ]] || die 'rotation pre-check: no acr-api exchange with C1'
  if rotation_acr_rows "$STATE/rotation-rows.txt" "$ROTATION_ACR_IPS" | awk -F'|' -v c="$c1" '$3 != c' | grep -q .; then die 'rotation pre-check: an acr-api exchange names a credential other than C1'; fi

  # S1: baseline.
  tb="$(rotation_now)"
  rotation_note "S1: baseline ${ROTATION_BASE_S} s"
  rotation_sleep_until $((tb + ROTATION_BASE_S))

  # S2: rotate into .next (dry run first); C1 and C2 are held in the volume for the direct and NEG cells.
  rotation_volume "cp -p ${ROTATION_LIVE} ${ROTATION_HOLD_C1}" || die 'rotation S2: C1 could not be held'
  counts0="$(rotation_audit_counts)"
  rc="$(rotation_verb dry1 workloads bootstrap-rotate -workload acr-api -out "$ROTATION_NEXT" -overlap "$ROTATION_OVERLAP")"
  if [[ "$rc" != 0 ]] || ! grep -q "^live credential=${c1} " "$ROTATION_RECORDS/verb-dry1.log"; then die 'rotation S2: the rotate dry run failed'; fi
  rc="$(rotation_verb rotate workloads bootstrap-rotate -workload acr-api -out "$ROTATION_NEXT" -overlap "$ROTATION_OVERLAP" -apply)"
  t0="$(rotation_now)"
  [[ "$rc" == 0 ]] || die 'rotation S2: bootstrap-rotate failed (verb-rotate.log)'
  c2="$(sed -nE 's/^workloads bootstrap-rotate applied: workload=acr-api credential=([0-9a-f-]{36}) .*/\1/p' "$ROTATION_RECORDS/verb-rotate.log")"
  c1_exp="$(sed -nE "s/^previous credential=${c1} now expires_at=(.*)$/\1/p" "$ROTATION_RECORDS/verb-rotate.log")"
  [[ "$c2" =~ ^[0-9a-f-]{36}$ && -n "$c1_exp" ]] || die 'rotation S2: the rotate output lacks C2 or the C1 expiry'
  c1_exp_s="$(date -u -d "$c1_exp" +%s)"
  if (( c1_exp_s >= t0 + ROTATION_OVERLAP_S - 60 && c1_exp_s <= t0 + ROTATION_OVERLAP_S + 5 )); then ex=PASS; else ex=FAIL; fi
  rotation_volume "cp -p ${ROTATION_NEXT} ${ROTATION_HOLD_C2}" || die 'rotation S2: C2 could not be held'
  rotation_note "S2: rotate -overlap ${ROTATION_OVERLAP} applied at T0=$(rotation_at "$t0"): C2 credential=${c2}; C1 now expires_at=${c1_exp} (T0+$((c1_exp_s - t0)) s) ${ex}"
  rotation_sleep_until $((t0 + 30))
  rc="$(rotation_verb dry2 workloads bootstrap-rotate -workload acr-api -out "${ROTATION_LIVE}.dry" -overlap "$ROTATION_OVERLAP")"
  if [[ "$rc" == 0 ]] && grep -q 'dry run: workload=acr-api live=2 ' "$ROTATION_RECORDS/verb-dry2.log"; then r5a=PASS; else r5a=FAIL; fi
  rotation_note "S2: dry run at T0+30 lists live=2: ${r5a}"

  # S3: both credentials exchange inside the overlap; the bounds hold; the audit grows by the rotate only.
  rotation_sleep_until $((t0 + 60))
  x1="$(rotation_cell 'S3 inside the overlap' "$ROTATION_HOLD_C1" 200 -)"
  x2="$(rotation_cell 'S3 inside the overlap' "$ROTATION_NEXT" 200 -)"
  rc61="$(rotation_verb b61 workloads bootstrap-rotate -workload acr-api -out "${ROTATION_LIVE}.b61" -overlap 61m -apply)"
  rc2="$(rotation_verb b2 workloads bootstrap-rotate -workload acr-api -out "${ROTATION_LIVE}.second" -overlap 5m -apply)"
  left="$(rotation_volume "ls -A /run/workload" | grep -Ec '\.(b61|second)$' || true)"
  counts1="$(rotation_audit_counts)"
  if [[ "$rc61" != 0 ]] && grep -q -- '-overlap must be from 0 to 1h' "$ROTATION_RECORDS/verb-b61.log"; then b61=PASS; else b61=FAIL; fi
  if [[ "$rc2" == 1 ]] && grep -q 'bootstrap-rotate failed (a rotation is still in its overlap)' "$ROTATION_RECORDS/verb-b2.log" && [[ "$left" == 0 ]]; then b2=PASS; else b2=FAIL; fi
  if [[ "$(cut -d' ' -f4 <<<"$counts1")" == $(( $(cut -d' ' -f4 <<<"$counts0") + 1 )) ]]; then ba=PASS; else ba=FAIL; fi
  rotation_note "S3: cells C1 ${x1} C2 ${x2}; -overlap 61m rc=${rc61} ${b61}; 2nd rotate rc=${rc2} -out files left=${left} ${b2}; bootstrap audit rows $(cut -d' ' -f4 <<<"$counts0") -> $(cut -d' ' -f4 <<<"$counts1") ${ba}"

  # S4: deliver C2 over the live file (the runbook step).
  rotation_sleep_until $((t0 + ROTATION_DELIVER_AT_S))
  rotation_deliver || die 'rotation S4: the delivery failed'
  ts="$(rotation_now)"
  mode="$(rotation_volume "stat -c '%u:%a' ${ROTATION_LIVE}")" || true
  if [[ "$mode" == 65532:600 ]]; then dl=PASS; else dl=FAIL; fi
  rotation_note "S4: .next moved over the live file at TS=$(rotation_at "$ts") (T0+$((ts - t0)) s); live file ${mode:-unknown} ${dl}"
  first=""
  while (( $(rotation_now) <= ts + ROTATION_CUT_BOUND_S )); do
    rotation_rows "$STATE/rotation-rows.txt"
    first="$(rotation_acr_rows "$STATE/rotation-rows.txt" "$ROTATION_ACR_IPS" | awk -F'|' -v c="$c2" -v a="$ts" '$3 == c && $1 >= a { print $1; exit }')"
    [[ -n "$first" ]] && break
    sleep 10
  done
  rotation_note "S4: first acr-api exchange with C2 at $(rotation_after TS "$first" "$ts") (looked until TS+${ROTATION_CUT_BOUND_S} s)"

  # S5: the overlap is over: C2 alone is live and C1 is refused as expired.
  rotation_sleep_until $((t0 + ROTATION_EXPIRED_AT_S))
  rc="$(rotation_verb dry3 workloads bootstrap-rotate -workload acr-api -out "${ROTATION_LIVE}.dry" -overlap "$ROTATION_OVERLAP")"
  if [[ "$rc" == 0 ]] && grep -q 'dry run: workload=acr-api live=1 ' "$ROTATION_RECORDS/verb-dry3.log" && grep -q "^live credential=${c2} " "$ROTATION_RECORDS/verb-dry3.log"; then r5b=PASS; else r5b=FAIL; fi
  x3="$(rotation_cell 'S5 after the overlap' "$ROTATION_HOLD_C1" refused expired)"
  rotation_note "S5: dry run lists C2 alone: ${r5b}; cell C1 expired ${x3}"

  # S6: revoke C1 (dry run, apply, dry run, repeat apply).
  rotation_sleep_until $((t0 + ROTATION_REVOKE_AT_S))
  [[ "$(rotation_verb rv0 workloads bootstrap-revoke -credential "$c1" -reason owner_request)" == 0 ]] || die 'rotation S6: the revoke dry run failed'
  [[ "$(rotation_verb rv1 workloads bootstrap-revoke -credential "$c1" -reason owner_request -apply)" == 0 ]] || die 'rotation S6: bootstrap-revoke failed (verb-rv1.log)'
  trv="$(rotation_now)"
  [[ "$(rotation_verb rv2 workloads bootstrap-revoke -credential "$c1" -reason owner_request)" == 0 ]] || die 'rotation S6: the second revoke dry run failed'
  rc="$(rotation_verb rv3 workloads bootstrap-revoke -credential "$c1" -reason owner_request -apply)"
  if grep -q "credential=${c1} status=expired" "$ROTATION_RECORDS/verb-rv0.log" && grep -q "credential=${c1} status=revoked" "$ROTATION_RECORDS/verb-rv2.log"; then rv=PASS; else rv=FAIL; fi
  if [[ "$rc" == 0 ]]; then rr=PASS; else rr=FAIL; fi
  x4="$(rotation_cell 'S6 after the revoke' "$ROTATION_HOLD_C1" refused revoked)"
  x5="$(rotation_cell 'S6 after the revoke' "$ROTATION_LIVE" 200 -)"
  rotation_note "S6: revoke C1 applied at T0+$((trv - t0)) s; status expired -> revoked ${rv}; repeat apply rc=${rc} ${rr}; cells C1 revoked ${x4}, C2 ${x5}"

  # S7: post window.
  rotation_sleep_until $((t0 + ROTATION_POST_END_S))
  rotation_note "S7: post window over (T0+${ROTATION_POST_END_S})"

  # Rules 1..6.
  rotation_rows "$STATE/rotation-rows.txt"
  cp "$STATE/rotation-rows.txt" "$ROTATION_RECORDS/token-issued-s1-s7.txt"
  counts2="$(rotation_audit_counts)"
  failures="$(rotation_lookup_failures "$tb" $((t0 + ROTATION_POST_END_S)))"
  rotation_rule 1 "$(rotation_judge_load "$ROTATION_RECORDS/pent.log" "$tb" $((t0 + ROTATION_POST_END_S)) "$failures")"
  rotation_rule 2 "$(rotation_judge_overlap_use "$STATE/rotation-rows.txt" "$ROTATION_ACR_IPS" "$c1" "$t0" "$ROTATION_OVERLAP_S")"
  tn="$(rotation_now)"
  rotation_rule 3 "$(rotation_judge_cutover "$STATE/rotation-rows.txt" "$ROTATION_ACR_IPS" "$c1" "$c2" "$ts" "$t0" "$ROTATION_OVERLAP_S" "$ROTATION_CUT_BOUND_S" "$tn")"
  first="$(rotation_acr_rows "$STATE/rotation-rows.txt" "$ROTATION_ACR_IPS" | awk -F'|' -v c="$c2" -v a="$ts" '$3 == c && $1 >= a { print $1; exit }')"
  rotation_rule 4 "$(rotation_judge_cells "$x1" "$x2" "$x3" "$x4" "$x5")"
  rotation_rule 5 "$(rotation_judge_bounds "c1-expiry=${ex}" "61m-refused=${b61}" "2nd-rotate-refused-no-out=${b2}" "audit-plus-1=${ba}" "live2-in-overlap=${r5a}" "c2-alone-after=${r5b}" "expired-to-revoked=${rv}" "repeat-revoke=${rr}" "delivered-65532-600=${dl}")"
  rotation_rule 6 "$(rotation_judge_counts $(( $(cut -d' ' -f1 <<<"$counts2") - $(cut -d' ' -f1 <<<"$counts0") )) $(( $(cut -d' ' -f2 <<<"$counts2") - $(cut -d' ' -f2 <<<"$counts0") )) $(( $(cut -d' ' -f3 <<<"$counts2") - $(cut -d' ' -f3 <<<"$counts0") )) $(( $(cut -d' ' -f5 <<<"$counts2") - $(cut -d' ' -f5 <<<"$counts0") )))"

  # S8 NEG: the revoked C1 put back is refused and acr-api fails closed; C2 put back recovers it. No direct cell runs
  # here, so every refusal in the auth-service log is acr-api's.
  rotation_put_back "$ROTATION_HOLD_C1" || die 'rotation S8: C1 could not be put back'
  tn="$(rotation_now)"
  rotation_note "S8 NEG: C1 (revoked) put back over the live file at TN=$(rotation_at "$tn")"
  while (( $(rotation_now) <= tn + ROTATION_NEG_REFUSE_BOUND_S )); do
    sleep 15
    if (( $(rotation_refusals_since "$tn" revoked) >= 1 )); then tr="$(rotation_now)"; break; fi
  done
  while (( $(rotation_now) <= tn + ROTATION_NEG_FAIL_BOUND_S )); do
    tf="$(awk -v a="$tn" 'NF == 2 && $1 >= a && $2 != "200" { print $1; exit }' "$ROTATION_RECORDS/pent.log")"
    [[ -n "$tf" ]] && break
    sleep 10
  done
  rotation_put_back "$ROTATION_HOLD_C2" || die 'rotation S8: C2 could not be put back'
  tc="$(rotation_now)"
  rotation_note "S8 NEG: refused revoked seen at $(rotation_after TN "$tr" "$tn"); first capabilities non-200 at $(rotation_after TN "$tf" "$tn"); C2 put back at TC=$(rotation_at "$tc")"
  while (( $(rotation_now) <= tc + ROTATION_NEG_RECOVER_BOUND_S )); do
    sleep 5
    tok="$(awk -v a="$tc" 'NF == 2 && $1 >= a && $2 == "200" { print $1; exit }' "$ROTATION_RECORDS/pent.log")"
    [[ -n "$tok" ]] && break
  done
  if [[ -n "$tok" ]]; then rotation_sleep_until $((tok + ROTATION_NEG_STEADY_S + ROTATION_LOAD_EVERY_S)); fi
  rm -f "$load_flag"
  wait "$ROTATION_LOAD_PID" 2>/dev/null || true
  after=x
  if [[ -n "$tok" ]]; then after="$(awk -v a="$tok" -v b=$((tok + ROTATION_NEG_STEADY_S)) 'NF == 2 && $1 > a && $1 <= b && $2 != "200"' "$ROTATION_RECORDS/pent.log" | wc -l | tr -d ' ')"; fi
  rotation_rows "$STATE/rotation-rows.txt"
  cp "$STATE/rotation-rows.txt" "$ROTATION_RECORDS/token-issued-all.txt"
  c1neg="$(rotation_acr_rows "$STATE/rotation-rows.txt" "$ROTATION_ACR_IPS" | awk -F'|' -v c="$c1" -v a="$tn" '$3 == c && $1 >= a' | wc -l | tr -d ' ')"
  rotation_rule 7 "$(rotation_judge_neg "$tn" "$tr" "$tf" "$tc" "$tok" "$after" "$c1neg")"

  # Rule 8: every service log of the project and every record.
  compose logs --no-color > "$STATE/rotation-services.log" 2>&1 || true
  rotation_rule 8 "$(rotation_judge_leak "$STATE/rotation-services.log" "$STATE/rendered.yml" "$ROTATION_RECORDS"/*)"

  {
    printf 'bootstrap-rotation drill, project %s\n' "$PROJECT"
    printf 'C1 %s, C2 %s, T0 %s, overlap %s, C1 expires_at %s (T0+%s s), delivered at T0+%s s, first acr-api C2 exchange %s, revoke at T0+%s s\n' \
      "$c1" "$c2" "$(rotation_at "$t0")" "$ROTATION_OVERLAP" "$c1_exp" "$((c1_exp_s - t0))" "$((ts - t0))" "$(rotation_after TS "$first" "$ts")" "$((trv - t0))"
    printf 'NEG: refused revoked at %s, capabilities non-200 from %s, recovered at %s\n' "$(rotation_after TN "$tr" "$tn")" "$(rotation_after TN "$tf" "$tn")" "$(rotation_after TC "$tok" "$tc")"
    cat "$ROTATION_RECORDS/rules.md"
  } > "$ROTATION_RECORDS/summary.md"
  if grep -Eq '^RULE [0-9]+: (FAIL|VOID)' "$ROTATION_RECORDS/rules.md"; then
    die "bootstrap rotation drill: $(grep -Ec '^RULE [0-9]+: (FAIL|VOID)' "$ROTATION_RECORDS/rules.md") rule(s) failed or void (rules.md)"
  fi
  SAFE_BOUNDARY='the acr-api bootstrap credential rotated with a 10m overlap, delivered by mv -f inside the overlap, the old one expired then revoked: rules 1..8 PASS, 0 failed capabilities calls across the switch'
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
    compose up -d postgres clickhouse valkey pgbouncer mailpit migrate go-api-roles go-api >/dev/null
    start_acr_after_go_api
    wait_https_ready
    record_acl_probe
    run_mcp "$(<"$STATE/secrets/acr-rotated-token")"
    SAFE_BOUNDARY='existing project-scoped volumes survived a complete application restart and reconciled role, password, ownership, and ACL drift'
  elif [[ "$SCENARIO" == bootstrap-rotation ]]; then
    run_bootstrap_rotation
  else
    run_failure
  fi
  note "PASS: ${SCENARIO}: ${SAFE_BOUNDARY}"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
