#!/usr/bin/env sh
set -eu

: "${POSTGRES_HOST:=postgres}"
: "${POSTGRES_PORT:=5432}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${ACR_DB_NAME:=acr}"
: "${ACR_RUNTIME_DB_USER:?ACR_RUNTIME_DB_USER is required}"
: "${ACR_MIGRATION_DB_USER:?ACR_MIGRATION_DB_USER is required}"
: "${ACR_ENABLE_EPISODE_WRITEBACK:=false}"

secret_value() {
  name="$1"
  file_name="${name}_FILE"
  eval "direct_set=\${${name}+x}"
  eval "direct_value=\${${name}-}"
  eval "file_set=\${${file_name}+x}"
  eval "file_path=\${${file_name}-}"
  if [ -n "$direct_set" ] && [ -n "$file_set" ]; then
    printf '%s and %s are mutually exclusive\n' "$name" "$file_name" >&2
    exit 2
  fi
  if [ -n "$file_set" ]; then
    [ -n "$file_path" ] && [ -f "$file_path" ] && [ ! -L "$file_path" ] || {
      printf '%s is invalid\n' "$file_name" >&2
      exit 2
    }
    permissions="$(stat -c '%a' "$file_path" 2>/dev/null)" || {
      printf '%s is unreadable\n' "$file_name" >&2
      exit 2
    }
    group_permissions="${permissions#?}"
    group_permissions="${group_permissions%?}"
    other_permissions="${permissions#??}"
    case "$group_permissions$other_permissions" in
      *[2367]*) printf '%s has unsafe permissions\n' "$file_name" >&2; exit 2 ;;
    esac
    size="$(wc -c < "$file_path" 2>/dev/null)" || {
      printf '%s is unreadable\n' "$file_name" >&2
      exit 2
    }
    size="$(printf '%s' "$size" | tr -d '[:space:]')"
    case "$size" in
      ''|*[!0-9]*) printf '%s is unreadable\n' "$file_name" >&2; exit 2 ;;
    esac
    [ "$size" -le 65536 ] || {
      printf '%s is too large\n' "$file_name" >&2
      exit 2
    }
    value="$(cat "$file_path" 2>/dev/null)" || {
      printf '%s is unreadable\n' "$file_name" >&2
      exit 2
    }
    value="$(printf '%s' "$value" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    [ -n "$value" ] || {
      printf '%s is empty\n' "$file_name" >&2
      exit 2
    }
    printf '%s' "$value"
    return
  fi
  [ -n "$direct_set" ] && [ -n "$direct_value" ] || {
    printf '%s or %s is required\n' "$name" "$file_name" >&2
    exit 2
  }
  printf '%s' "$direct_value"
}

POSTGRES_PASSWORD="$(secret_value POSTGRES_PASSWORD)"
ACR_RUNTIME_DB_PASSWORD="$(secret_value ACR_RUNTIME_DB_PASSWORD)"
ACR_MIGRATION_DB_PASSWORD="$(secret_value ACR_MIGRATION_DB_PASSWORD)"

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
SELECT format('ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD %L', :'runtime_user', :'runtime_password') \gexec
SELECT format('ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD %L', :'migration_user', :'migration_password') \gexec
SELECT format('CREATE DATABASE %I OWNER %I', :'acr_db_name', :'migration_user')
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'acr_db_name') \gexec
SELECT format('ALTER DATABASE %I OWNER TO %I', :'acr_db_name', :'migration_user') \gexec
SELECT format('REVOKE ALL PRIVILEGES ON DATABASE %I FROM PUBLIC', :'acr_db_name') \gexec
SELECT format('REVOKE ALL PRIVILEGES ON DATABASE %I FROM %I', :'acr_db_name', :'runtime_user') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'acr_db_name', :'runtime_user') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'acr_db_name', :'migration_user') \gexec
SQL

psql -v ON_ERROR_STOP=1 -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$ACR_DB_NAME" \
  -v migration_user="$ACR_MIGRATION_DB_USER" <<'SQL'
SELECT format('ALTER SCHEMA acr OWNER TO %I', :'migration_user')
WHERE EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'acr') \gexec
SELECT format('ALTER TABLE acr.%I OWNER TO %I', c.relname, :'migration_user')
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = 'acr' AND c.relkind IN ('r', 'p') \gexec
SELECT format('ALTER SEQUENCE acr.%I OWNER TO %I', c.relname, :'migration_user')
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = 'acr' AND c.relkind = 'S' \gexec
SQL

if [ "$mode" = runtime-acl ]; then
  psql -v ON_ERROR_STOP=1 -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$ACR_DB_NAME" \
    -v runtime_user="$ACR_RUNTIME_DB_USER" -v migration_user="$ACR_MIGRATION_DB_USER" \
    -v episode_writeback="$ACR_ENABLE_EPISODE_WRITEBACK" <<'SQL'
REVOKE USAGE, CREATE ON SCHEMA acr FROM PUBLIC;
REVOKE USAGE, CREATE ON SCHEMA acr FROM :"runtime_user";
ALTER SCHEMA acr OWNER TO :"migration_user";
ALTER TABLE acr.schema_migrations OWNER TO :"migration_user";
ALTER TABLE acr.client_credentials OWNER TO :"migration_user";
ALTER TABLE acr.context_packet_snapshots OWNER TO :"migration_user";
ALTER TABLE acr.audit_events OWNER TO :"migration_user";
ALTER TABLE acr.agent_episodes OWNER TO :"migration_user";
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA acr FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA acr FROM PUBLIC;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA acr FROM :"runtime_user";
REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA acr FROM :"runtime_user";
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

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.device_authorizations FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.device_authorizations TO :"runtime_user";

ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON TABLES FROM :"runtime_user";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON SEQUENCES FROM :"runtime_user";
SQL
fi
