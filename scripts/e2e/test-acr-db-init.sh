#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
init_script="$root/deploy/compose/acr-db-init.sh"
container="acr-e2e-acl-bootstrap-${RANDOM}${RANDOM}"
database="acr_acl_${RANDOM}${RANDOM}"
admin_password="admin-${RANDOM}${RANDOM}"
# CI has no Docker Hub credentials: it sets ACR_E2E_POSTGRES_IMAGE to the digest-identical ghcr mirror copy.
postgres_image="${ACR_E2E_POSTGRES_IMAGE:-postgres:18-alpine@sha256:10a5c69a83035ab5239f5620d708520e8b28f5aa1f2b20143e6024532edadfb5}"

cleanup() {
  local status=$?
  docker rm -f "$container" >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

POSTGRES_PASSWORD="$admin_password" docker run -d --rm --name "$container" --label devhealth.acr.e2e=acl-bootstrap \
  -e POSTGRES_USER=bootstrap -e POSTGRES_PASSWORD -e POSTGRES_DB=postgres \
  "$postgres_image" >/dev/null

# The entrypoint's temporary init server listens on the unix socket only and is then restarted, so the wait is on TCP.
ready=false
for _ in {1..90}; do
  if docker exec "$container" pg_isready -h 127.0.0.1 -U bootstrap -d postgres >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  printf 'postgres container %s did not accept TCP connections in 90 s\n' "$container" >&2
  docker logs "$container" >&2 || true
  exit 1
fi

run_init() {
  local mode="$1"
  POSTGRES_PASSWORD="$admin_password" ACR_RUNTIME_DB_PASSWORD=runtime-password ACR_MIGRATION_DB_PASSWORD=migration-password \
    docker run --rm --network "container:${container}" \
    -v "$init_script:/usr/local/bin/acr-db-init:ro" \
    -e POSTGRES_HOST=127.0.0.1 -e POSTGRES_PORT=5432 \
    -e POSTGRES_USER=bootstrap -e POSTGRES_PASSWORD -e ACR_DB_NAME="$database" \
    -e ACR_RUNTIME_DB_USER=acr_runtime -e ACR_RUNTIME_DB_PASSWORD \
    -e ACR_MIGRATION_DB_USER=acr_migration -e ACR_MIGRATION_DB_PASSWORD \
    -e ACR_ENABLE_EPISODE_WRITEBACK="${ACR_ENABLE_EPISODE_WRITEBACK:-false}" \
    "$postgres_image" /usr/local/bin/acr-db-init "$mode" >/dev/null
}

admin_query() {
  (export PGPASSWORD="$admin_password"; docker exec -e PGPASSWORD "$container" \
    psql -h 127.0.0.1 -U bootstrap -d "$database" -At -v ON_ERROR_STOP=1 -c "$1")
}

expect() {
  local expected="$1"
  local actual
  actual="$(admin_query "$2")"
  [[ "$actual" == "$expected" ]] || {
    printf 'ACL contract failed: expected %s, got %s\n' "$expected" "$actual" >&2
    exit 1
  }
}

run_init roles

# The fixture is the real migration set, applied in order as the migration role, so the ACL contract is
# checked against the tables acr-db-init.sh really grants on (CHAOS-14215).
migrations=("$root"/migrations/postgres/[0-9][0-9][0-9][0-9]_*.sql)
[[ -f "${migrations[0]}" ]] || { printf 'no migrations found under %s\n' "$root/migrations/postgres" >&2; exit 1; }
{
  printf 'CREATE SCHEMA IF NOT EXISTS acr; CREATE TABLE IF NOT EXISTS acr.schema_migrations (version BIGINT PRIMARY KEY, name TEXT NOT NULL, checksum TEXT, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW());\n'
  for migration in "${migrations[@]}"; do
    printf 'BEGIN;\n'
    cat "$migration"
    printf '\nCOMMIT;\n'
  done
} | PGPASSWORD=migration-password docker exec -i -e PGPASSWORD "$container" \
  psql -h 127.0.0.1 -U acr_migration -d "$database" -v ON_ERROR_STOP=1 -q >/dev/null 2>&1

run_init runtime-acl

expect 't|f' "SELECT has_schema_privilege('acr_runtime', 'acr', 'USAGE'), has_schema_privilege('acr_runtime', 'acr', 'CREATE')"
expect 't' "SELECT bool_and(pg_get_userbyid(relowner) = 'acr_migration') FROM pg_class WHERE relnamespace = 'acr'::regnamespace AND relkind = 'r'"
expect 't|f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.schema_migrations', 'SELECT'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'INSERT'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'DELETE')"
expect 't|t|t|f' "SELECT has_table_privilege('acr_runtime', 'acr.client_credentials', 'SELECT'), has_table_privilege('acr_runtime', 'acr.client_credentials', 'INSERT'), has_table_privilege('acr_runtime', 'acr.client_credentials', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.client_credentials', 'DELETE')"
expect 't|t|t|t' "SELECT has_table_privilege('acr_runtime', 'acr.context_packet_snapshots', 'SELECT'), has_table_privilege('acr_runtime', 'acr.context_packet_snapshots', 'INSERT'), has_table_privilege('acr_runtime', 'acr.context_packet_snapshots', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.context_packet_snapshots', 'DELETE')"
expect 't|t|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.audit_events', 'SELECT'), has_table_privilege('acr_runtime', 'acr.audit_events', 'INSERT'), has_table_privilege('acr_runtime', 'acr.audit_events', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.audit_events', 'DELETE')"
expect 'f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.agent_episodes', 'SELECT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'INSERT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'UPDATE')"
expect 't|t|t|t' "SELECT has_table_privilege('acr_runtime', 'acr.device_authorizations', 'SELECT'), has_table_privilege('acr_runtime', 'acr.device_authorizations', 'INSERT'), has_table_privilege('acr_runtime', 'acr.device_authorizations', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.device_authorizations', 'DELETE')"
expect 't|t|t|t' "SELECT has_table_privilege('acr_runtime', 'acr.oauth_clients', 'SELECT'), has_table_privilege('acr_runtime', 'acr.oauth_clients', 'INSERT'), has_table_privilege('acr_runtime', 'acr.oauth_clients', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.oauth_clients', 'DELETE')"
expect 't|t|t|t' "SELECT has_table_privilege('acr_runtime', 'acr.oauth_authorization_requests', 'SELECT'), has_table_privilege('acr_runtime', 'acr.oauth_authorization_requests', 'INSERT'), has_table_privilege('acr_runtime', 'acr.oauth_authorization_requests', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.oauth_authorization_requests', 'DELETE')"

PGPASSWORD=migration-password docker exec -e PGPASSWORD "$container" \
  psql -h 127.0.0.1 -U acr_migration -d "$database" -v ON_ERROR_STOP=1 \
  -c "CREATE TABLE acr.future_runtime_table (id TEXT PRIMARY KEY)" >/dev/null
expect 'f|f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.future_runtime_table', 'SELECT'), has_table_privilege('acr_runtime', 'acr.future_runtime_table', 'INSERT'), has_table_privilege('acr_runtime', 'acr.future_runtime_table', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.future_runtime_table', 'DELETE')"

PGPASSWORD="$admin_password" docker exec -i -e PGPASSWORD "$container" \
  psql -h 127.0.0.1 -U bootstrap -d postgres -v ON_ERROR_STOP=1 <<SQL >/dev/null
ALTER ROLE acr_runtime SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS PASSWORD 'stale-runtime-password';
ALTER ROLE acr_migration SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS PASSWORD 'stale-migration-password';
ALTER DATABASE ${database} OWNER TO bootstrap;
SQL
PGPASSWORD="$admin_password" docker exec -i -e PGPASSWORD "$container" \
  psql -h 127.0.0.1 -U bootstrap -d "$database" -v ON_ERROR_STOP=1 <<'SQL' >/dev/null
ALTER SCHEMA acr OWNER TO bootstrap;
ALTER TABLE acr.client_credentials OWNER TO bootstrap;
GRANT ALL ON SCHEMA acr TO acr_runtime;
GRANT ALL ON ALL TABLES IN SCHEMA acr TO acr_runtime;
SQL

run_init roles
run_init runtime-acl

expect 'f|f|f|f|f|f' "SELECT rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls, rolinherit FROM pg_roles WHERE rolname = 'acr_runtime'"
expect 'f|f|f|f|f|f' "SELECT rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls, rolinherit FROM pg_roles WHERE rolname = 'acr_migration'"
expect 'acr_migration' "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = '${database}'"
expect 'acr_migration' "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = 'acr'"
expect 'acr_migration' "SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid = 'acr.client_credentials'::regclass"
expect 't|f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.schema_migrations', 'SELECT'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'INSERT'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'DELETE')"
PGPASSWORD=runtime-password docker exec -e PGPASSWORD "$container" \
  psql -h 127.0.0.1 -U acr_runtime -d "$database" -At -v ON_ERROR_STOP=1 -c 'SELECT current_user' | grep -qx 'acr_runtime'

ACR_ENABLE_EPISODE_WRITEBACK=true run_init runtime-acl
expect 't|t|t|f' "SELECT has_table_privilege('acr_runtime', 'acr.agent_episodes', 'SELECT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'INSERT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'DELETE')"

run_init runtime-acl
expect 'f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.agent_episodes', 'SELECT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'INSERT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'UPDATE')"

printf 'acr database ACL integration contract passed\n'
