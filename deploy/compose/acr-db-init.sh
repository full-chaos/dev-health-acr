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

-- CHAOS-3859 (sol review F1): the hosted runtime writes clarification-
-- selection capture events through pgclarification.Sink -- INSERT only,
-- mirroring audit_events immediately above exactly: this table has no
-- read path yet (capture-only phase), so no SELECT grant either. The
-- table needs no sequence grant at all: its primary key is an
-- application-generated UUID string (migrations/postgres/0016, matching
-- 0010's context_fabric_model_execution_receipts idiom), not a
-- database-generated BIGSERIAL/IDENTITY.
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_clarification_selections FROM :"runtime_user";
GRANT INSERT ON TABLE acr.context_fabric_clarification_selections TO :"runtime_user";

-- CHAOS-3876: auditing every OTHER context_fabric_* table found the exact
-- same gap the clarification-selections fix immediately above closed for
-- one table. The hosted API and acr-projector (including its "priors"
-- operator subcommands, cmd/acr-projector/priors.go's openPriorsDB) each
-- open exactly ONE Postgres connection, as :"runtime_user" -- so a table
-- absent from this list was not merely under-privileged, it was completely
-- unwritable in production: every INSERT/UPDATE/SELECT against it fails
-- permission-denied, silently swallowed by whichever caller's fail-open
-- error handling sits above the store (the "fails-toward-fine" class this
-- ticket named). acr_db_init_integration_test.go extends the CHAOS-3859
-- proof seam with one real INSERT per table below, under this exact ACL.
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_projection_checkpoints FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.context_fabric_projection_checkpoints TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_projection_rebuild_markers FROM :"runtime_user";
GRANT SELECT, INSERT, DELETE ON TABLE acr.context_fabric_projection_rebuild_markers TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_investigation_results FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_investigation_results TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_supersession_claims FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_structure_supersession_claims TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_reuse_invalidations FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_reuse_invalidations TO :"runtime_user";

-- context_fabric_org_model_config.generation DEFAULTs to nextval() on its
-- own explicit sequence (migration 0010, NOT a GENERATED ... AS IDENTITY
-- column) -- an INSERT/UPSERT needs USAGE on that sequence in addition to
-- INSERT on the table itself; Postgres does not imply one grant from the
-- other for a plain sequence-backed column DEFAULT.
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_org_model_config FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.context_fabric_org_model_config TO :"runtime_user";
GRANT USAGE ON SEQUENCE acr.context_fabric_org_model_config_generation_seq TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_model_execution_receipts FROM :"runtime_user";
GRANT INSERT ON TABLE acr.context_fabric_model_execution_receipts TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_graph_lifecycle FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_graph_lifecycle TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_graph_epoch_retirements FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_graph_epoch_retirements TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_graph_build_source_progress FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_graph_build_source_progress TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_selections FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_structure_selections TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_priors FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_structure_priors TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_prior_pointer FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_structure_prior_pointer TO :"runtime_user";

-- context_fabric_structure_prior_pointer_history.id is BIGSERIAL (migration
-- 0028) -- the one exception among these tables to the app-generated-key
-- convention every other table here follows -- so its owned sequence needs
-- the same explicit USAGE grant as the org_model_config sequence above.
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_prior_pointer_history FROM :"runtime_user";
GRANT INSERT ON TABLE acr.context_fabric_structure_prior_pointer_history TO :"runtime_user";
GRANT USAGE ON SEQUENCE acr.context_fabric_structure_prior_pointer_history_id_seq TO :"runtime_user";

REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_prior_revocations FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_structure_prior_revocations TO :"runtime_user";

ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON TABLES FROM :"runtime_user";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON SEQUENCES FROM :"runtime_user";
SQL
fi
