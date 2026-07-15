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

export PGPASSWORD="$POSTGRES_PASSWORD"
until pg_isready -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d postgres >/dev/null 2>&1; do sleep 1; done

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

psql -v ON_ERROR_STOP=1 -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" -d "$ACR_DB_NAME" \
  -v runtime_user="$ACR_RUNTIME_DB_USER" -v migration_user="$ACR_MIGRATION_DB_USER" <<'SQL'
GRANT USAGE ON SCHEMA public TO :"runtime_user";
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO :"runtime_user";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migration_user" IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"runtime_user";
SQL
