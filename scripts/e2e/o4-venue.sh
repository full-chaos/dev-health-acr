#!/usr/bin/env bash
# o4-venue.sh: one way to bring up an O4 fact-oracle venue (docs/o4-venue.md) as its own
# Docker Compose project, seed it with a synthetic organization, and tear it down.
#
# The capture itself (`make o4-oracle-preflight o4-oracle-capture`) reads only the ACR_O4_*
# environment this script writes to the env file; any other host that serves the same
# services (Kubernetes, a Mac) runs those targets with its own env file instead.
#
# Runs on bash 3.2 (macOS) and Linux bash. Needs docker with the compose plugin, git, curl,
# openssl. Every name, port and limit is an environment variable with a default below.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ACR_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

: "${O4_VENUE_PROJECT:=acr-e2e-o4venue-$(date -u +%Y%m%d%H%M%S)}"
: "${O4_VENUE_OPS_DIR:=}"
: "${O4_VENUE_OPS_COMMIT:=2c115402dbbb3b11f28a0029cdbe3b9da5c3e891}"
: "${O4_VENUE_OPS_TAG:=sha-2c11540}"
: "${O4_VENUE_ACR_TAG:=sha-24513ae}"
: "${O4_VENUE_REGISTRY:=ghcr.io/full-chaos}"
: "${O4_VENUE_STATE_DIR:=${ACR_ROOT}/.tmp/o4venue/${O4_VENUE_PROJECT}}"
: "${O4_VENUE_API_PORT:=28443}"
: "${O4_VENUE_MCP_PORT:=28081}"
: "${O4_VENUE_CH_HTTP_PORT:=28123}"
: "${O4_VENUE_CH_NATIVE_PORT:=29000}"
: "${O4_VENUE_PLANES_SUBNET:=10.199.91.0/28}"
: "${O4_VENUE_PLANES_RANGE:=10.199.91.8/29}"
: "${O4_VENUE_QUERY_SUBNET:=10.199.89.0/28}"
: "${O4_VENUE_MAX_LOAD:=40}"
: "${O4_VENUE_MIN_DISK_GB:=50}"
: "${O4_VENUE_WINDOW_DAYS:=31}"
: "${O4_VENUE_DERIVE_TIMEOUT:=1800}"
# The frozen ops world the organization is seeded with (ops `dho fixtures generate`).
: "${O4_VENUE_WORLD_ARGS:=--provider mixed --repo-name chaos-fixtures/shared-api --repo-count 18 --days 180 --commits-per-day 0 --pr-count 189 --team-count 8 --seed 9440}"
# The `_mg` world adds work units and investments when the derive chain leaves them empty.
: "${O4_VENUE_FALLBACK_WORLD_ARGS:=--provider synthetic --repo-name acme/live-e2e --days 14 --commits-per-day 6 --pr-count 24 --team-count 10 --seed 20260219 --with-metrics --with-work-graph}"
: "${O4_VENUE_WORKER_QUEUES:=investment,metrics,reports,workgraph}"
: "${O4_VENUE_JQ_IMAGE:=ghcr.io/jqlang/jq:1.7.1}"

STATE="$O4_VENUE_STATE_DIR"
SECRETS="$STATE/secrets"
ENV_FILE="$STATE/o4.env"

die() { printf '[o4-venue] FAIL: %s\n' "$*" >&2; exit 1; }
note() { printf '[o4-venue] %s\n' "$*" >&2; }
usage() {
  cat >&2 <<'EOF'
usage: o4-venue.sh <step>
  check    requirements, host gates (load, disk), free ports and subnets; starts nothing
  render   write the override and validate the merged compose model; starts nothing
  up       pull the pinned images, start the stack from empty volumes, provision the organization
  seed     load the synthetic world and derive the tables (workers run here only)
  counts   P0: rows of the organization in the window, per table (read only)
  env      write the ACR_O4_* env file (0600) for `make o4-oracle-preflight o4-oracle-capture`
  down     `down -v` of this project only, then the residue check
EOF
  exit 2
}

# --- requirements and host gates ----------------------------------------------------------

require_tools() {
  local tool
  for tool in docker git curl openssl awk sed; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
  done
  docker compose version >/dev/null 2>&1 || die 'the docker compose plugin (v2) is required'
  [ -n "$O4_VENUE_OPS_DIR" ] || die 'O4_VENUE_OPS_DIR must name a dev-health-ops checkout at the vendored pin'
  [ -f "$O4_VENUE_OPS_DIR/compose.yml" ] || die 'O4_VENUE_OPS_DIR has no compose.yml'
  local head
  head="$(git -C "$O4_VENUE_OPS_DIR" rev-parse HEAD 2>/dev/null || true)"
  [ "$head" = "$O4_VENUE_OPS_COMMIT" ] || die "the ops checkout is at ${head:-no commit}, not O4_VENUE_OPS_COMMIT"
  case "$O4_VENUE_PROJECT" in
    acr-e2e-o4venue-*) ;;
    *) die 'O4_VENUE_PROJECT must start with acr-e2e-o4venue-' ;;
  esac
}

load_1min() {
  if [ -r /proc/loadavg ]; then
    awk '{print $1}' /proc/loadavg
  else
    sysctl -n vm.loadavg 2>/dev/null | awk '{print $2}'
  fi
}

host_gates() {
  local load root avail_kb
  load="$(load_1min)"
  [ -n "$load" ] || die 'cannot read the load average'
  if awk -v l="$load" -v m="$O4_VENUE_MAX_LOAD" 'BEGIN { exit !(l > m) }'; then
    die "host load ${load} is above O4_VENUE_MAX_LOAD=${O4_VENUE_MAX_LOAD}: wait, do not raise the limit"
  fi
  root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
  if [ -n "$root" ] && [ -d "$root" ]; then
    avail_kb="$(df -Pk "$root" | awk 'NR==2 {print $4}')"
    if [ "$((avail_kb / 1048576))" -lt "$O4_VENUE_MIN_DISK_GB" ]; then
      die "$((avail_kb / 1048576))G free under the Docker root, below O4_VENUE_MIN_DISK_GB=${O4_VENUE_MIN_DISK_GB}"
    fi
  else
    note 'disk gate skipped: the Docker root is not on this host (Docker runs in a VM)'
  fi
  note "host gates pass: load ${load} <= ${O4_VENUE_MAX_LOAD}"
}

port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

check_ports() {
  local port
  for port in "$O4_VENUE_API_PORT" "$O4_VENUE_MCP_PORT" "$O4_VENUE_CH_HTTP_PORT" "$O4_VENUE_CH_NATIVE_PORT"; do
    port_free "$port" || die "port ${port} is in use: set another O4_VENUE_*_PORT"
  done
  local subnet used
  used="$(docker network ls -q | xargs docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null || true)"
  for subnet in "$O4_VENUE_PLANES_SUBNET" "$O4_VENUE_QUERY_SUBNET"; do
    case " $used " in *" $subnet "*) die "subnet ${subnet} is in use: set another O4_VENUE_*_SUBNET" ;; esac
  done
  note "ports ${O4_VENUE_API_PORT} ${O4_VENUE_MCP_PORT} ${O4_VENUE_CH_HTTP_PORT} ${O4_VENUE_CH_NATIVE_PORT} and subnets free"
}

assert_project_new() {
  if [ -n "$(docker ps -aq --filter "label=com.docker.compose.project=${O4_VENUE_PROJECT}")" ] ||
    [ -n "$(docker volume ls -q --filter "label=com.docker.compose.project=${O4_VENUE_PROJECT}")" ]; then
    die "project ${O4_VENUE_PROJECT} already has containers or volumes"
  fi
}

# --- state, secrets and the compose model --------------------------------------------------

random_secret() { openssl rand -hex 24; }
# Host-only credential files are 0600; files a container reads through a bind mount are 0644
# (the distroless UID of the container is not the owner).
write_private() { (umask 077; printf '%s' "$2" > "$1"); }
write_shared() { (umask 022; printf '%s' "$2" > "$1"); }

image() { printf '%s/%s:%s' "$O4_VENUE_REGISTRY" "$1" "$2"; }

digest_of() {
  docker pull -q "$1" >/dev/null
  docker image inspect --format '{{index .RepoDigests 0}}' "$1"
}

prepare_state() {
  mkdir -p "$SECRETS" "$STATE/web"
  chmod 700 "$STATE" "$SECRETS"
  [ -f "$SECRETS/postgres-password" ] || write_shared "$SECRETS/postgres-password" "$(random_secret)"
  [ -f "$SECRETS/clickhouse-password" ] || write_shared "$SECRETS/clickhouse-password" "$(random_secret)"
  [ -f "$SECRETS/reader-password" ] || write_shared "$SECRETS/reader-password" "$(random_secret)"
  [ -f "$SECRETS/runtime-password" ] || write_shared "$SECRETS/runtime-password" "$(random_secret)"
  [ -f "$SECRETS/migration-password" ] || write_shared "$SECRETS/migration-password" "$(random_secret)"
  [ -f "$SECRETS/evidence-kid" ] || write_shared "$SECRETS/evidence-kid" 'o4-venue-kid'
  [ -f "$SECRETS/evidence-keys" ] || write_shared "$SECRETS/evidence-keys" "o4-venue-kid=$(openssl rand -base64 32 | tr -d '\n')"
  [ -f "$SECRETS/settings-key" ] || write_private "$SECRETS/settings-key" "$(random_secret)"
  [ -f "$SECRETS/bugsink-key" ] || write_private "$SECRETS/bugsink-key" "$(random_secret)"
  local pg ch reader runtime migration
  pg="$(cat "$SECRETS/postgres-password")"; ch="$(cat "$SECRETS/clickhouse-password")"
  reader="$(cat "$SECRETS/reader-password")"; runtime="$(cat "$SECRETS/runtime-password")"
  migration="$(cat "$SECRETS/migration-password")"
  write_shared "$SECRETS/runtime-dsn" "postgres://acr_runtime:${runtime}@postgres:5432/acr?sslmode=disable"
  write_shared "$SECRETS/migration-dsn" "postgres://acr_migration:${migration}@postgres:5432/acr?sslmode=disable"
  write_shared "$SECRETS/acr-clickhouse-dsn" "clickhouse://o4_reader:${reader}@clickhouse:9000/default"
  export POSTGRES_USER=postgres POSTGRES_PASSWORD="$pg" POSTGRES_DB=postgres
  export CLICKHOUSE_USER=ch CLICKHOUSE_PASSWORD="$ch" CLICKHOUSE_DB=default
  export SETTINGS_ENCRYPTION_KEY BUGSINK_SECRET_KEY
  SETTINGS_ENCRYPTION_KEY="$(cat "$SECRETS/settings-key")"; BUGSINK_SECRET_KEY="$(cat "$SECRETS/bugsink-key")"
  export AUTH_TOKEN_ISSUER_URL="${AUTH_TOKEN_ISSUER_URL:-https://auth.dev-health.internal:8443}" AUTH_USERS_OWNED=false
  export ACR_RUNTIME_DB_USER=acr_runtime ACR_MIGRATION_DB_USER=acr_migration ACR_DB_NAME=acr
  export ACR_POSTGRES_ADMIN_PASSWORD_FILE="$SECRETS/postgres-password"
  export ACR_RUNTIME_DB_PASSWORD_FILE="$SECRETS/runtime-password" ACR_MIGRATION_DB_PASSWORD_FILE="$SECRETS/migration-password"
  export ACR_RUNTIME_DSN_FILE="$SECRETS/runtime-dsn" ACR_MIGRATION_DSN_FILE="$SECRETS/migration-dsn"
  export ACR_CLICKHOUSE_DSN_FILE="$SECRETS/acr-clickhouse-dsn"
  export ACR_EVIDENCE_ACTIVE_KID_FILE="$SECRETS/evidence-kid" ACR_EVIDENCE_KEYS_FILE="$SECRETS/evidence-keys"
  DEV_HEALTH_GO_DHO_IMAGE="$(image dev-health-go-dho "$O4_VENUE_OPS_TAG")"
  DEV_HEALTH_GO_OPERATOR_IMAGE="$(image dev-health-go-operator "$O4_VENUE_OPS_TAG")"
  DEV_HEALTH_GO_AUTH_MIGRATE_IMAGE="$(image dev-health-go-auth-migrate "$O4_VENUE_OPS_TAG")"
  DEV_HEALTH_GO_AUTH_SERVICE_IMAGE="$(image dev-health-go-auth-service "$O4_VENUE_OPS_TAG")"
  DEV_HEALTH_GO_CONTRACTCHECK_IMAGE="$(image dev-health-go-contractcheck "$O4_VENUE_OPS_TAG")"
  export DEV_HEALTH_GO_DHO_IMAGE DEV_HEALTH_GO_OPERATOR_IMAGE DEV_HEALTH_GO_AUTH_MIGRATE_IMAGE DEV_HEALTH_GO_AUTH_SERVICE_IMAGE DEV_HEALTH_GO_CONTRACTCHECK_IMAGE
}

render_override() {
  local acr_api acr_mcp
  if [ -f "$STATE/images" ]; then
    acr_api="$(sed -n 1p "$STATE/images")"; acr_mcp="$(sed -n 2p "$STATE/images")"
  else
    acr_api="$(image dev-health-acr/acr-api "$O4_VENUE_ACR_TAG")"; acr_mcp="$(image dev-health-acr/acr-mcp "$O4_VENUE_ACR_TAG")"
  fi
  export ACR_IMAGE="$acr_api"
  local mcp_resource="http://127.0.0.1:${O4_VENUE_MCP_PORT}/mcp"
  local api_base="http://127.0.0.1:${O4_VENUE_API_PORT}"
  cat > "$STATE/override.yml" <<EOF
# Generated by scripts/e2e/o4-venue.sh for project ${O4_VENUE_PROJECT}. Not committed.
networks:
  planes:
    ipam: { config: !override [ { subnet: "${O4_VENUE_PLANES_SUBNET}", ip_range: "${O4_VENUE_PLANES_RANGE}" } ] }
  dev-health: {}
  o4_query_internal:
    ipam: { config: [ { subnet: "${O4_VENUE_QUERY_SUBNET}" } ] }
services:
  postgres: { container_name: !reset null, ports: !reset [], networks: [default, auth_plane, dev-health] }
  pgbouncer: { container_name: !reset null, ports: !reset [] }
  pgbouncer-river-queue: { container_name: !reset null }
  pgbouncer-river-coordinator: { container_name: !reset null }
  valkey: { container_name: !reset null, ports: !reset [] }
  migrate: { container_name: !reset null }
  bugsink: { container_name: !reset null, ports: !reset [], profiles: [o4-never] }
  router: { ports: !reset [], profiles: [o4-never] }
  clickhouse:
    container_name: !reset null
    ports: !override [ "127.0.0.1:${O4_VENUE_CH_HTTP_PORT}:8123", "127.0.0.1:${O4_VENUE_CH_NATIVE_PORT}:9000" ]
    networks: [default, dev-health]
  go-api: { ports: !reset [], networks: [default, planes, auth_plane, dev-health] }
  auth-issuer: { ports: !reset [] }
  auth-service: { ports: !reset [] }
  query-api:
    networks: [default, planes, dev-health, o4_query_internal]
    environment:
      QUERY_API_INTERNAL_ADDR: ":8095"
      QUERY_API_INTERNAL_ALLOWED_CIDRS: "${O4_VENUE_QUERY_SUBNET}"
      QUERY_API_MCP_ADDR: ":8096"
      QUERY_API_MCP_ALLOWED_CIDRS: "${O4_VENUE_QUERY_SUBNET}"
      QUERY_API_MCP_BOUNDARY: allowed-cidrs
  go-scheduler: { profiles: [o4-never] }
  go-reconciler: { profiles: [o4-never] }
  go-stream-external: { profiles: [o4-never] }
  go-stream-ingest: { profiles: [o4-never] }
  go-stream-pagerduty: { profiles: [o4-never] }
  go-worker-ops: { profiles: [o4-never] }
  go-worker-sync: { profiles: [o4-never] }
  go-worker-sync-provider: { profiles: [o4-never] }
  acr-projector: { profiles: [o4-never] }
  acr-db-init:
    volumes: !override [ "${ACR_ROOT}/deploy/compose/acr-db-init.sh:/usr/local/bin/acr-db-init:ro" ]
  acr-db-acl:
    volumes: !override [ "${ACR_ROOT}/deploy/compose/acr-db-init.sh:/usr/local/bin/acr-db-init:ro" ]
  acr-api:
    image: "${acr_api}"
    ports: [ "127.0.0.1:${O4_VENUE_API_PORT}:8080" ]
    labels: !reset {}
    networks: [dev-health, acr_auth_workload, o4_query_internal]
    environment:
      ACR_DEV_HEALTH_ENTITLEMENT_URL: http://go-api:8091
      ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL: workload
      ACR_DATA_QUERY_URL: http://query-api:8095
      ACR_DATA_GRAPHQL_URL: http://query-api:8096
      ACR_DATA_QUERY_TIMEOUT: 30s
      ACR_OAUTH_ISSUER: "${api_base}"
      ACR_OAUTH_RESOURCES: "${mcp_resource}"
      ACR_DEVICE_VERIFICATION_URL: "${api_base}/device"
      ACR_REQUIRE_BACKING_STORES: "false"
  acr-mcp:
    image: "${acr_mcp}"
    entrypoint: ["/usr/local/bin/acr-mcp"]
    command: ["serve"]
    restart: "no"
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    ports: [ "127.0.0.1:${O4_VENUE_MCP_PORT}:8081" ]
    environment:
      ACR_MCP_TRANSPORT: http
      ACR_MCP_HTTP_LISTEN: ":8081"
      ACR_MCP_HTTP_BASE_PATH: /mcp
      ACR_MCP_HTTP_WRITE_TIMEOUT: 140s
      ACR_API_URL: http://acr-api:8080
      ACR_API_TIMEOUT: 120s
      ACR_API_ALLOW_INSECURE_INTERNAL_HTTP: "true"
      ACR_MCP_AUTHORIZATION_SERVER: "${api_base}"
      ACR_MCP_RESOURCE_URL: "${mcp_resource}"
    depends_on: { acr-api: { condition: service_started } }
    networks: [dev-health]
  o4-worker:
    extends: { file: "${O4_VENUE_OPS_DIR}/compose.yml", service: go-worker-ops }
    profiles: [o4-derive]
    labels: !reset {}
    command:
      - worker
      - --queues=${O4_VENUE_WORKER_QUEUES}
      - --worker-group=heavy
      - --shutdown-timeout=60s
      - --http-addr=:8080
      - --river-schema=river
      - --domain-database-role=devhealth_domain
      - --queue-database-role=devhealth_queue
      - --queue-database-mode=session
      - --coordinator-database-mode=session
      - --log-level=info
EOF
}

compose() {
  docker compose -p "$O4_VENUE_PROJECT" --project-directory "$O4_VENUE_OPS_DIR" \
    -f "$O4_VENUE_OPS_DIR/compose.yml" \
    -f "$ACR_ROOT/deploy/compose/acr.compose.yml" \
    -f "$ACR_ROOT/deploy/compose/acr-auth-workload.compose.yml" \
    -f "$STATE/override.yml" \
    --profile auth "$@"
}

# dho runs the ops CLI one-shot in the query-api image on the project network, against
# Postgres directly (admin and migration verbs) and ClickHouse on the native port.
dho() {
  local pg ch
  pg="$(cat "$SECRETS/postgres-password")"; ch="$(cat "$SECRETS/clickhouse-password")"
  compose run --rm --no-deps -T \
    -e "MIGRATION_DATABASE_URI=postgres://postgres:${pg}@postgres:5432/postgres?sslmode=disable" \
    -e "POSTGRES_URI=postgres://postgres:${pg}@postgres:5432/postgres?sslmode=disable" \
    -e "CLICKHOUSE_URI=clickhouse://ch:${ch}@clickhouse:9000/default" \
    query-api "$@"
}

ch_query() {
  # shellcheck disable=SC2016 # expanded by the container's sh.
  compose exec -T clickhouse sh -c 'clickhouse-client --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --multiquery'
}

# --- steps -----------------------------------------------------------------------------------

step_check() {
  require_tools
  host_gates
  check_ports
  assert_project_new
}

step_render() {
  require_tools
  prepare_state
  render_override
  compose config -q
  # Every host port the model publishes, per service (no environment is printed).
  compose --profile o4-derive config --format json | docker run --rm -i --network none "${O4_VENUE_JQ_IMAGE}" -r \
    '.services | to_entries[] | select((.value.ports // []) | length > 0) | "\(.key) \([.value.ports[] | "\(.host_ip // "0.0.0.0"):\(.published)"] | join(","))"' >&2
  note "compose model of ${O4_VENUE_PROJECT} renders"
}

ACR_SERVICES="acr-db-init acr-migrate acr-db-acl acr-workload-volume acr-workload-bootstrap acr-api acr-mcp"
OPS_SERVICES="postgres pgbouncer pgbouncer-river-queue pgbouncer-river-coordinator clickhouse valkey migrate go-river-provision go-river-migrate go-api query-api auth-service auth-issuer"

step_up() {
  step_check
  prepare_state
  local acr_api acr_mcp
  acr_api="$(digest_of "$(image dev-health-acr/acr-api "$O4_VENUE_ACR_TAG")")"
  acr_mcp="$(digest_of "$(image dev-health-acr/acr-mcp "$O4_VENUE_ACR_TAG")")"
  printf '%s\n%s\n' "$acr_api" "$acr_mcp" > "$STATE/images"
  render_override
  compose config -q
  # shellcheck disable=SC2086 # service lists are word lists.
  compose pull -q $OPS_SERVICES
  # shellcheck disable=SC2086
  compose up -d --no-build --wait $OPS_SERVICES
  # shellcheck disable=SC2086
  compose up -d --no-build --wait $ACR_SERVICES
  provision_org
  grant_reader
  note "up: ${O4_VENUE_PROJECT}, ops ${O4_VENUE_OPS_TAG}, acr ${O4_VENUE_ACR_TAG}"
}

provision_org() {
  local output org
  output="$(dho admin orgs create --name 'O4 venue' --slug "$O4_VENUE_PROJECT" --description 'O4 fact-oracle venue (synthetic)' --tier community)" ||
    die 'organization provisioning failed'
  org="$(printf '%s\n' "$output" | sed -nE 's/.*id:[[:space:]]*([0-9a-fA-F-]{36}).*/\1/p' | head -n 1)"
  case "$org" in ????????-????-????-????-????????????) ;; *) die 'organization provisioning returned no id' ;; esac
  dho admin bundles assign-org --org-id "$org" --feature-key agent_context_runtime --reason 'O4 venue' --expires-days 1 >/dev/null
  write_private "$SECRETS/org-id" "$org"
  note 'organization provisioned (id in the state dir, never printed)'
}

grant_reader() {
  ch_query <<EOF >/dev/null
CREATE USER IF NOT EXISTS o4_reader IDENTIFIED BY '$(cat "$SECRETS/reader-password")' SETTINGS readonly = 1;
GRANT SELECT ON default.* TO o4_reader;
EOF
}

step_seed() {
  prepare_state
  render_override
  local org ch
  org="$(cat "$SECRETS/org-id")"; ch="$(cat "$SECRETS/clickhouse-password")"
  # shellcheck disable=SC2086 # the world arguments are a word list.
  dho fixtures generate --sink "clickhouse://ch:${ch}@clickhouse:9000/default" --db-type clickhouse --org "$org" $O4_VENUE_WORLD_ARGS >/dev/null
  note 'synthetic world loaded'
  derive "$org"
  if [ "$(table_count work_unit_investments "$org")" = 0 ]; then
    note 'no work unit investments after the derive chain: loading the _mg world for the same organization'
    # shellcheck disable=SC2086
    dho fixtures generate --sink "clickhouse://ch:${ch}@clickhouse:9000/default" --db-type clickhouse --org "$org" --allow-mixed-org $O4_VENUE_FALLBACK_WORLD_ARGS >/dev/null
    derive "$org"
  fi
}

# derive runs the ops job chain for the organization with a worker of this project only, then
# stops the worker: nothing writes the store after the seed.
derive() {
  local org="$1" from to
  to="$(date -u +%Y-%m-%d)"
  from="$(date -u -d "-$((O4_VENUE_WINDOW_DAYS + 4)) days" +%Y-%m-%d 2>/dev/null || date -u -v-"$((O4_VENUE_WINDOW_DAYS + 4))"d +%Y-%m-%d)"
  compose --profile o4-derive up -d --no-build o4-worker
  dho workers metrics daily-redrive --org "$org" --from "$from" --to "$to" --review-evidence testing >/dev/null
  dho workers investment trigger --org "$org" --review-evidence testing >/dev/null
  dho workers workgraph trigger --org "$org" --review-evidence testing >/dev/null
  dho workers metrics remaining trigger-backstop --family capacity --org "$org" --review-evidence testing >/dev/null
  wait_queue_drained
  compose --profile o4-derive stop o4-worker >/dev/null
  compose --profile o4-derive rm -f o4-worker >/dev/null
  note 'derive chain done; worker stopped'
}

wait_queue_drained() {
  local waited=0 pending
  while [ "$waited" -lt "$O4_VENUE_DERIVE_TIMEOUT" ]; do
    # shellcheck disable=SC2016 # expanded by the container's sh.
    pending="$(compose exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "SELECT count(*) FROM river.river_job WHERE state IN ('"'"'available'"'"', '"'"'running'"'"', '"'"'retryable'"'"', '"'"'scheduled'"'"')"' 2>/dev/null || echo unknown)"
    [ "$pending" = 0 ] && return 0
    sleep 15; waited=$((waited + 15))
  done
  die "the derive chain did not drain within O4_VENUE_DERIVE_TIMEOUT=${O4_VENUE_DERIVE_TIMEOUT}s (pending: ${pending})"
}

COUNT_TABLES="work_unit_investments work_unit_supersessions work_unit_membership_runs work_unit_membership repos teams team_repo_ownership capacity_forecasts compounding_risk_daily work_item_metrics_daily estimate_coverage_metrics_daily work_item_completion_daily repo_metrics_daily incident_metrics_daily investment_metrics_daily work_unit_issue_size work_unit_repo_effort work_item_team_attributions work_items work_item_cycle_times"

table_count() {
  printf "SELECT count() FROM default.%s WHERE org_id = '%s'" "$1" "$2" | ch_query
}

step_counts() {
  prepare_state
  render_override
  local org table n zero=0
  org="$(cat "$SECRETS/org-id")"
  for table in $COUNT_TABLES; do
    n="$(table_count "$table" "$org")"
    printf '%s\t%s\n' "$table" "$n"
    if [ "$n" = 0 ] && [ "$table" != work_unit_supersessions ]; then zero=$((zero + 1)); fi
  done
  [ "$zero" = 0 ] || die "P0: ${zero} table(s) hold no row of the organization"
  note 'P0: every table holds rows of the organization'
}

step_env() {
  prepare_state
  local org reader
  org="$(cat "$SECRETS/org-id")"; reader="$(cat "$SECRETS/reader-password")"
  write_private "$SECRETS/o4-clickhouse.dsn" "clickhouse://o4_reader:${reader}@127.0.0.1:${O4_VENUE_CH_NATIVE_PORT}/default"
  [ -f "$SECRETS/mcp-token" ] || die "no MCP credential at ${SECRETS}/mcp-token: mint it on this venue (docs/o4-venue.md) and store it 0600"
  chmod 600 "$SECRETS/mcp-token"
  (umask 077; cat > "$ENV_FILE" <<EOF
ACR_O4_MCP_URL=http://127.0.0.1:${O4_VENUE_MCP_PORT}/mcp
ACR_O4_TOKEN_FILE=${SECRETS}/mcp-token
ACR_O4_CLICKHOUSE_DSN_FILE=${SECRETS}/o4-clickhouse.dsn
ACR_O4_ORG_ID=${org}
ACR_O4_OPS_BUILD=dev-health-ops ${O4_VENUE_OPS_COMMIT} ${O4_VENUE_OPS_TAG}
ACR_O4_ACR_BUILD=dev-health-acr ${O4_VENUE_ACR_TAG}
EOF
)
  note "env file written (0600): ${ENV_FILE}"
}

step_down() {
  prepare_state
  render_override
  compose --profile o4-derive down --volumes --remove-orphans >/dev/null 2>&1 || note 'compose down reported an error'
  local residue
  residue="$(docker ps -aq --filter "label=com.docker.compose.project=${O4_VENUE_PROJECT}")$(docker volume ls -q --filter "label=com.docker.compose.project=${O4_VENUE_PROJECT}")$(docker network ls -q --filter "label=com.docker.compose.project=${O4_VENUE_PROJECT}")"
  [ -z "$residue" ] || die "residue of ${O4_VENUE_PROJECT} after down -v"
  rm -f "$SECRETS/mcp-token" "$SECRETS/o4-clickhouse.dsn" "$ENV_FILE"
  note "down -v: ${O4_VENUE_PROJECT} left zero containers, volumes and networks; credentials removed"
}

[ $# -eq 1 ] || usage
case "$1" in
  check) step_check ;;
  render) step_render ;;
  up) step_up ;;
  seed) step_seed ;;
  counts) step_counts ;;
  env) step_env ;;
  down) step_down ;;
  *) usage ;;
esac
