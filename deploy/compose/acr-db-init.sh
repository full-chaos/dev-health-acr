#!/usr/bin/env sh
set -eu

: "${POSTGRES_HOST:=postgres}"
: "${POSTGRES_PORT:=5432}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
: "${ACR_DB_NAME:=acr}"
: "${ACR_RUNTIME_DB_USER:?ACR_RUNTIME_DB_USER is required}"
: "${ACR_RUNTIME_DB_PASSWORD:?ACR_RUNTIME_DB_PASSWORD is required}"
: "${ACR_MIGRATION_DB_USER:?ACR_MIGRATION_DB_USER is required}"
: "${ACR_MIGRATION_DB_PASSWORD:?ACR_MIGRATION_DB_PASSWORD is required}"
: "${ACR_ENABLE_EPISODE_WRITEBACK:=false}"

mode="${1:-roles}"
case "$mode" in
  roles|runtime-acl) ;;
  *) printf 'usage: %s [roles|runtime-acl]\n' "$0" >&2; exit 2 ;;
esac
case "$ACR_ENABLE_EPISODE_WRITEBACK" in
  true|false) ;;
  *) printf 'ACR_ENABLE_EPISODE_WRITEBACK must be true or false\n' >&2; exit 2 ;;
esac

export PGPASSWORD="$POSTGRES_PASSWORD"
attempts=0
until pg_isready -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d postgres >/dev/null 2>&1; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 60 ]; then
    printf 'PostgreSQL readiness timed out\n' >&2
    exit 1
  fi
  sleep 1
done

psql -v ON_ERROR_STOP=1 -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d postgres \
  -v acr_db_name="$ACR_DB_NAME" \
  -v runtime_user="$ACR_RUNTIME_DB_USER" -v runtime_password="$ACR_RUNTIME_DB_PASSWORD" \
  -v migration_user="$ACR_MIGRATION_DB_USER" -v migration_password="$ACR_MIGRATION_DB_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', :'runtime_user', :'runtime_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'runtime_user') \gexec
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', :'migration_user', :'migration_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'migration_user') \gexec
SELECT format('CREATE DATABASE %I OWNER %I', :'acr_db_name', :'migration_user')
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'acr_db_name') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'acr_db_name', :'runtime_user') \gexec
SQL

if [ "$mode" = runtime-acl ]; then
  psql -v ON_ERROR_STOP=1 -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$ACR_DB_NAME" \
    -v runtime_user="$ACR_RUNTIME_DB_USER" -v migration_user="$ACR_MIGRATION_DB_USER" \
    -v episode_writeback="$ACR_ENABLE_EPISODE_WRITEBACK" <<'SQL'
REVOKE USAGE, CREATE ON SCHEMA acr FROM PUBLIC;
REVOKE USAGE, CREATE ON SCHEMA acr FROM :"runtime_user";
GRANT USAGE ON SCHEMA acr TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.schema_migrations FROM :"runtime_user";
GRANT SELECT ON TABLE acr.schema_migrations TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.client_credentials FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.client_credentials TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_packet_snapshots FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.context_packet_snapshots TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.audit_events FROM :"runtime_user";
GRANT INSERT ON TABLE acr.audit_events TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.agent_episodes FROM :"runtime_user";
\if :episode_writeback
GRANT SELECT, INSERT, UPDATE ON TABLE acr.agent_episodes TO :"runtime_user";
\endif

ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM :"runtime_user";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"runtime_user";
SQL
fi
