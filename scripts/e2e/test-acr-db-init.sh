#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
init_script="$root/deploy/compose/acr-db-init.sh"
container="acr-e2e-acl-bootstrap-${RANDOM}${RANDOM}"
database="acr_acl_${RANDOM}${RANDOM}"
admin_password="admin-${RANDOM}${RANDOM}"

cleanup() {
  local status=$?
  docker rm -f "$container" >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

docker run -d --rm --name "$container" --label devhealth.acr.e2e=acl-bootstrap \
  -e POSTGRES_USER=bootstrap -e POSTGRES_PASSWORD="$admin_password" -e POSTGRES_DB=postgres \
  postgres:18-alpine >/dev/null

for _ in {1..60}; do
  if docker exec "$container" pg_isready -U bootstrap -d postgres >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$container" pg_isready -U bootstrap -d postgres >/dev/null

run_init() {
  local mode="$1"
  docker run --rm --network "container:${container}" \
    -v "$init_script:/usr/local/bin/acr-db-init:ro" \
    -e POSTGRES_HOST=127.0.0.1 -e POSTGRES_PORT=5432 \
    -e POSTGRES_USER=bootstrap -e POSTGRES_PASSWORD="$admin_password" -e ACR_DB_NAME="$database" \
    -e ACR_RUNTIME_DB_USER=acr_runtime -e ACR_RUNTIME_DB_PASSWORD=runtime-password \
    -e ACR_MIGRATION_DB_USER=acr_migration -e ACR_MIGRATION_DB_PASSWORD=migration-password \
    -e ACR_ENABLE_EPISODE_WRITEBACK="${ACR_ENABLE_EPISODE_WRITEBACK:-false}" \
    postgres:18-alpine /usr/local/bin/acr-db-init "$mode" >/dev/null
}

admin_query() {
  docker exec -e PGPASSWORD="$admin_password" "$container" \
    psql -h 127.0.0.1 -U bootstrap -d "$database" -At -v ON_ERROR_STOP=1 -c "$1"
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

docker exec -i -e PGPASSWORD=migration-password "$container" \
  psql -h 127.0.0.1 -U acr_migration -d "$database" -v ON_ERROR_STOP=1 <<'SQL' >/dev/null
CREATE SCHEMA acr AUTHORIZATION acr_migration;
CREATE TABLE acr.schema_migrations (version BIGINT PRIMARY KEY);
CREATE TABLE acr.client_credentials (credential_id TEXT PRIMARY KEY);
CREATE TABLE acr.context_packet_snapshots (context_packet_id TEXT PRIMARY KEY);
CREATE TABLE acr.audit_events (audit_event_id UUID PRIMARY KEY);
CREATE TABLE acr.agent_episodes (episode_id TEXT PRIMARY KEY);
SQL

run_init runtime-acl

expect 't|f' "SELECT has_schema_privilege('acr_runtime', 'acr', 'USAGE'), has_schema_privilege('acr_runtime', 'acr', 'CREATE')"
expect 't' "SELECT bool_and(pg_get_userbyid(relowner) = 'acr_migration') FROM pg_class WHERE relnamespace = 'acr'::regnamespace AND relkind = 'r'"
expect 't|f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.schema_migrations', 'SELECT'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'INSERT'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.schema_migrations', 'DELETE')"
expect 't|t|t|f' "SELECT has_table_privilege('acr_runtime', 'acr.client_credentials', 'SELECT'), has_table_privilege('acr_runtime', 'acr.client_credentials', 'INSERT'), has_table_privilege('acr_runtime', 'acr.client_credentials', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.client_credentials', 'DELETE')"
expect 't|t|t|t' "SELECT has_table_privilege('acr_runtime', 'acr.context_packet_snapshots', 'SELECT'), has_table_privilege('acr_runtime', 'acr.context_packet_snapshots', 'INSERT'), has_table_privilege('acr_runtime', 'acr.context_packet_snapshots', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.context_packet_snapshots', 'DELETE')"
expect 'f|t|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.audit_events', 'SELECT'), has_table_privilege('acr_runtime', 'acr.audit_events', 'INSERT'), has_table_privilege('acr_runtime', 'acr.audit_events', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.audit_events', 'DELETE')"
expect 'f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.agent_episodes', 'SELECT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'INSERT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'UPDATE')"

docker exec -e PGPASSWORD=migration-password "$container" \
  psql -h 127.0.0.1 -U acr_migration -d "$database" -v ON_ERROR_STOP=1 \
  -c "CREATE TABLE acr.future_runtime_table (id TEXT PRIMARY KEY)" >/dev/null
expect 'f|f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.future_runtime_table', 'SELECT'), has_table_privilege('acr_runtime', 'acr.future_runtime_table', 'INSERT'), has_table_privilege('acr_runtime', 'acr.future_runtime_table', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.future_runtime_table', 'DELETE')"

docker exec -i -e PGPASSWORD="$admin_password" "$container" \
  psql -h 127.0.0.1 -U bootstrap -d postgres -v ON_ERROR_STOP=1 <<SQL >/dev/null
ALTER ROLE acr_runtime SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS PASSWORD 'stale-runtime-password';
ALTER ROLE acr_migration SUPERUSER CREATEDB CREATEROLE REPLICATION BYPASSRLS PASSWORD 'stale-migration-password';
ALTER DATABASE ${database} OWNER TO bootstrap;
SQL
docker exec -i -e PGPASSWORD="$admin_password" "$container" \
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
docker exec -e PGPASSWORD=runtime-password "$container" \
  psql -h 127.0.0.1 -U acr_runtime -d "$database" -At -v ON_ERROR_STOP=1 -c 'SELECT current_user' | grep -qx 'acr_runtime'

ACR_ENABLE_EPISODE_WRITEBACK=true run_init runtime-acl
expect 't|t|t|f' "SELECT has_table_privilege('acr_runtime', 'acr.agent_episodes', 'SELECT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'INSERT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'UPDATE'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'DELETE')"

run_init runtime-acl
expect 'f|f|f' "SELECT has_table_privilege('acr_runtime', 'acr.agent_episodes', 'SELECT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'INSERT'), has_table_privilege('acr_runtime', 'acr.agent_episodes', 'UPDATE')"

printf 'acr database ACL integration contract passed\n'
