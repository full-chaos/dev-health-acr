#!/usr/bin/env sh
set -eu

: "${POSTGRES_HOST:=postgres}"
: "${POSTGRES_PORT:=5432}"
: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${ACR_DB_NAME:=acr}"
: "${ACR_RUNTIME_DB_USER:?ACR_RUNTIME_DB_USER is required}"
: "${ACR_MIGRATION_DB_USER:?ACR_MIGRATION_DB_USER is required}"
: "${ACR_ENABLE_EPISODE_WRITEBACK:=false}"
: "${ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE:=}"
: "${ACR_WORKLOAD_TRUST_DOMAIN:=}"

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

# Workload token exchange is configured when both of its settings are set to
# something other than whitespace, the same rule the hosted runtime applies
# (workloadTokenExchangeConfigured trims both).
trim() { printf '%s' "$1" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'; }
workload_token_exchange=false
if [ -n "$(trim "$ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE")" ] && [ -n "$(trim "$ACR_WORKLOAD_TRUST_DOMAIN")" ]; then
  workload_token_exchange=true
fi

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
    -v episode_writeback="$ACR_ENABLE_EPISODE_WRITEBACK" -v workload_token_exchange="$workload_token_exchange" <<'SQL'
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

-- BEGIN GENERATED RUNTIME GRANTS (internal/dbacl; regenerate with DBACL_WRITE=1 go test ./internal/dbacl)
-- readiness check and migration verification read it: internal/runtime/hosted/postgres_readiness.go:20
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.schema_migrations FROM :"runtime_user";
GRANT SELECT ON TABLE acr.schema_migrations TO :"runtime_user";

-- SELECT internal/storage/postgres/credentials.go:61, INSERT credentials.go:167, UPDATE credentials.go:115 and credentials_audit.go:72
-- DELETE of expired workload credentials: internal/storage/postgres/workload_bindings.go:65, started only when workload token exchange is configured (internal/runtime/hosted/postgres.go:185)
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.client_credentials FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.client_credentials TO :"runtime_user";
\if :workload_token_exchange
GRANT DELETE ON TABLE acr.client_credentials TO :"runtime_user";
\endif

-- read only, no write path; the resolver is built only when workload token exchange is configured: internal/storage/postgres/workload_bindings.go:36, internal/runtime/hosted/workload_token_exchange.go:57
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.workload_bindings FROM :"runtime_user";
\if :workload_token_exchange
GRANT SELECT ON TABLE acr.workload_bindings TO :"runtime_user";
\endif

-- SELECT internal/storage/postgres/packet.go:69, INSERT packet.go:49, DELETE packet_retention.go:28; UPDATE because the purge locks rows with FOR UPDATE SKIP LOCKED (packet_retention.go:20), which Postgres checks as UPDATE; the readiness check asserts it
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_packet_snapshots FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.context_packet_snapshots TO :"runtime_user";

-- INSERT internal/storage/postgres/audit.go:69; SELECT credentials_audit.go:50 and :61 (credential rotation conflict checks)
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.audit_events FROM :"runtime_user";
GRANT INSERT ON TABLE acr.audit_events TO :"runtime_user";

-- SELECT internal/storage/postgres/episodes.go:92, INSERT episodes.go:63, UPDATE episodes.go:126
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.agent_episodes FROM :"runtime_user";
\if :episode_writeback
GRANT SELECT, INSERT, UPDATE ON TABLE acr.agent_episodes TO :"runtime_user";
\endif

-- SELECT internal/storage/postgres/device_authorization.go:88, INSERT device_authorization.go:61, UPDATE device_authorization.go:132, DELETE oauth_purge.go:167
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.device_authorizations FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.device_authorizations TO :"runtime_user";

-- SELECT internal/storage/postgres/oauth.go:66, INSERT oauth.go:104, DELETE oauth_purge.go:109; UPDATE because the idle-client purge locks rows with FOR UPDATE OF c SKIP LOCKED (oauth_purge.go:103), which Postgres checks as UPDATE
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.oauth_clients FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.oauth_clients TO :"runtime_user";

-- SELECT internal/storage/postgres/oauth.go:165, INSERT oauth.go:68, UPDATE oauth.go:185, DELETE oauth_purge.go:59
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.oauth_authorization_requests FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.oauth_authorization_requests TO :"runtime_user";

-- SELECT internal/storage/postgres/oauth.go:308, INSERT oauth.go:79; removed with its device_authorizations parent by cascade
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.oauth_device_grants FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.oauth_device_grants TO :"runtime_user";

-- INSERT internal/storage/postgres/web_assertion_replay.go:24, SELECT and DELETE web_assertion_replay.go:33
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.web_assertion_replays FROM :"runtime_user";
GRANT SELECT, INSERT, DELETE ON TABLE acr.web_assertion_replays TO :"runtime_user";

-- insert-only capture sink: internal/contextfabric/pgclarification/sink.go:387
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_clarification_selections FROM :"runtime_user";
GRANT INSERT ON TABLE acr.context_fabric_clarification_selections TO :"runtime_user";

-- SELECT internal/contextfabric/pgprojection/checkpoints.go:38, INSERT checkpoints.go:99, UPDATE checkpoints.go:80, DELETE checkpoints.go:258
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_projection_checkpoints FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.context_fabric_projection_checkpoints TO :"runtime_user";

-- SELECT internal/contextfabric/pgprojection/rebuild_marker.go:63, INSERT rebuild_marker.go:43, DELETE rebuild_marker.go:79
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_projection_rebuild_markers FROM :"runtime_user";
GRANT SELECT, INSERT, DELETE ON TABLE acr.context_fabric_projection_rebuild_markers TO :"runtime_user";

-- SELECT internal/contextfabric/pgprojection/org_purge_marker.go:46 (the tick's check), DELETE org_purge_marker.go:86 (discovery's clear); only the purge writes it
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_org_purge_markers FROM :"runtime_user";
GRANT SELECT, DELETE ON TABLE acr.context_fabric_org_purge_markers TO :"runtime_user";

-- SELECT internal/contextfabric/pginvestigation/store.go:395, INSERT store.go:284
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_investigation_results FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_investigation_results TO :"runtime_user";

-- SELECT internal/contextfabric/pginvestigation/store.go:491, INSERT store.go:345
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_supersession_claims FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_structure_supersession_claims TO :"runtime_user";

-- SELECT internal/contextfabric/pginvestigation/store.go:748, INSERT and upsert store.go:1290
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_reuse_invalidations FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_reuse_invalidations TO :"runtime_user";

-- SELECT internal/contextfabric/pgmodelconfig/store.go:138, INSERT and upsert store.go:95, DELETE store.go:195; generation draws from an explicit sequence
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_org_model_config FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE acr.context_fabric_org_model_config TO :"runtime_user";
GRANT USAGE ON SEQUENCE acr.context_fabric_org_model_config_generation_seq TO :"runtime_user";

-- insert-only: internal/contextfabric/pgmodelreceipts/store.go:80
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_model_execution_receipts FROM :"runtime_user";
GRANT INSERT ON TABLE acr.context_fabric_model_execution_receipts TO :"runtime_user";

-- SELECT internal/contextfabric/pglifecycle/store.go:83, INSERT store.go:114, UPDATE store.go:240
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_graph_lifecycle FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_graph_lifecycle TO :"runtime_user";

-- SELECT internal/contextfabric/pglifecycle/store.go:469, INSERT store.go:328, UPDATE store.go:496
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_graph_epoch_retirements FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_graph_epoch_retirements TO :"runtime_user";

-- SELECT internal/contextfabric/pglifecycle/store.go:172, INSERT and upsert store.go:153
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_graph_build_source_progress FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_graph_build_source_progress TO :"runtime_user";

-- INSERT internal/contextfabric/pgstructureselection/sink.go:423, SELECT structurepriorcuration/curate.go:104
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_selections FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_structure_selections TO :"runtime_user";

-- SELECT internal/contextfabric/pgstructurepriors/store.go:130, INSERT store.go:221
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_priors FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_structure_priors TO :"runtime_user";

-- SELECT internal/contextfabric/pgstructurepriors/store.go:113, INSERT store.go:463, UPDATE store.go:366
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_prior_pointer FROM :"runtime_user";
GRANT SELECT, INSERT, UPDATE ON TABLE acr.context_fabric_structure_prior_pointer TO :"runtime_user";

-- insert-only history: internal/contextfabric/pgstructurepriors/store.go:374; id is BIGSERIAL
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_prior_pointer_history FROM :"runtime_user";
GRANT INSERT ON TABLE acr.context_fabric_structure_prior_pointer_history TO :"runtime_user";
GRANT USAGE ON SEQUENCE acr.context_fabric_structure_prior_pointer_history_id_seq TO :"runtime_user";

-- SELECT internal/contextfabric/pgstructurepriors/store.go:169, INSERT store.go:499
REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE acr.context_fabric_structure_prior_revocations FROM :"runtime_user";
GRANT SELECT, INSERT ON TABLE acr.context_fabric_structure_prior_revocations TO :"runtime_user";
-- END GENERATED RUNTIME GRANTS

ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON TABLES FROM :"runtime_user";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA acr REVOKE ALL ON SEQUENCES FROM :"runtime_user";
SQL
fi
