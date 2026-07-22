#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ops_root="${DEV_HEALTH_OPS_DIR:-$repo_root/../dev-health-ops}"
web_root="${DEV_HEALTH_WEB_DIR:-$repo_root/../dev-health-web}"
state_dir="${CONTEXT_FABRIC_LOCAL_STATE_DIR:-$repo_root/.local/context-fabric}"
reset=false

usage() {
  cat >&2 <<USAGE
usage: $0 [--state-dir <path>] [--reset]

Creates owner-readable local TLS material, ACR database credentials, Web
assertion keys, and the Compose environment consumed by the root compose.yml.
It does not start or stop containers.
USAGE
}

while (($#)); do
  case "$1" in
    --state-dir) state_dir="${2:-}"; shift 2 ;;
    --reset) reset=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

for command in openssl python3; do
  command -v "$command" >/dev/null 2>&1 || {
    printf '%s is required\n' "$command" >&2
    exit 1
  }
done

[[ -f "$ops_root/compose.yml" ]] || {
  printf 'dev-health-ops checkout not found: %s\n' "$ops_root" >&2
  exit 1
}
[[ -f "$web_root/Dockerfile" && -f "$web_root/deploy/compose/web.compose.yml" ]] || {
  printf 'dev-health-web checkout or Compose model not found: %s\n' "$web_root" >&2
  exit 1
}
ops_root="$(cd "$ops_root" && pwd -P)"
web_root="$(cd "$web_root" && pwd -P)"

case "$state_dir" in
  /*) ;;
  *) state_dir="$repo_root/$state_dir" ;;
esac

if [[ -L "$state_dir" ]]; then
  printf 'refusing symlink state directory: %s\n' "$state_dir" >&2
  exit 1
fi
if $reset && [[ -e "$state_dir" ]]; then
  rm -rf -- "$state_dir"
fi
if [[ -e "$state_dir/compose.env" ]]; then
  printf 'local Context Fabric state already exists: %s\n' "$state_dir" >&2
  printf 'use --reset only after docker compose down --volumes\n' >&2
  exit 1
fi

umask 077
mkdir -p "$state_dir/pki" "$state_dir/secrets" "$state_dir/runtime"
chmod 700 "$state_dir" "$state_dir/pki" "$state_dir/secrets" "$state_dir/runtime"

random_hex() { openssl rand -hex 24; }
random_b64() { openssl rand -base64 32 | tr -d '\n'; }
write_secret() { printf '%s' "$2" >"$1"; chmod 600 "$1"; }

pki="$state_dir/pki"
openssl req -x509 -newkey rsa:2048 -nodes -days 14 -sha256 \
  -subj '/CN=Dev Health Context Fabric Local CA' \
  -keyout "$pki/ca.key" -out "$pki/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=acr.localhost' \
  -keyout "$pki/server.key" -out "$pki/server.csr" >/dev/null 2>&1
cat >"$pki/server.ext" <<'EXT'
subjectAltName=DNS:acr.localhost,DNS:acr-tls,DNS:acr-ops-tls,DNS:acr-api,DNS:postgres,DNS:clickhouse,DNS:localhost,IP:127.0.0.1
extendedKeyUsage=serverAuth
EXT
openssl x509 -req -days 14 -sha256 \
  -in "$pki/server.csr" -CA "$pki/ca.crt" -CAkey "$pki/ca.key" \
  -CAcreateserial -extfile "$pki/server.ext" -out "$pki/server.crt" >/dev/null 2>&1
rm -f "$pki/server.csr" "$pki/server.ext" "$pki/ca.srl"
chmod 600 "$pki/ca.key" "$pki/server.key"
chmod 644 "$pki/ca.crt" "$pki/server.crt"

secrets="$state_dir/secrets"
runtime_password="$(random_hex)"
migration_password="$(random_hex)"
reader_password="$(random_hex)"
evidence_kid="acr-local-$(openssl rand -hex 6)"

write_secret "$secrets/postgres-admin-password" 'postgres'
write_secret "$secrets/acr-runtime-password" "$runtime_password"
write_secret "$secrets/acr-migration-password" "$migration_password"
write_secret "$secrets/clickhouse-reader-password" "$reader_password"
write_secret "$secrets/acr-evidence-kid" "$evidence_kid"
write_secret "$secrets/acr-evidence-keys" "$evidence_kid=$(random_b64)"
write_secret "$secrets/acr-ops-token" ''
write_secret "$secrets/acr-client-token" ''

acr_db='acr_local'
evidence_db='acr_local_evidence'
write_secret "$secrets/acr-runtime-dsn" \
  "postgres://acr_runtime:${runtime_password}@postgres:5432/${acr_db}?sslmode=verify-full&sslrootcert=/run/secrets/acr_ca"
write_secret "$secrets/acr-migration-dsn" \
  "postgres://acr_migration:${migration_password}@postgres:5432/${acr_db}?sslmode=verify-full&sslrootcert=/run/secrets/acr_ca"
write_secret "$secrets/acr-clickhouse-dsn" \
  "clickhouse://acr_reader:${reader_password}@clickhouse:9440/${evidence_db}?secure=true&skip_verify=false"

web_key="$secrets/acr-web-assertion.key"
web_jwks="$secrets/acr-web-assertion.jwks.json"
web_kid="web-local-$(openssl rand -hex 6)"
openssl genpkey -algorithm Ed25519 -out "$web_key" >/dev/null 2>&1
public_x="$({ openssl pkey -in "$web_key" -pubout -outform DER 2>/dev/null || exit 1; } | tail -c 32 | openssl base64 -A | tr '+/' '-_' | tr -d '=')"
python3 - "$web_jwks" "$web_kid" "$public_x" <<'PY'
from __future__ import annotations

import json
import sys
from pathlib import Path

Path(sys.argv[1]).write_text(
    json.dumps(
        {
            "keys": [
                {
                    "kty": "OKP",
                    "crv": "Ed25519",
                    "x": sys.argv[3],
                    "kid": sys.argv[2],
                    "use": "sig",
                    "alg": "EdDSA",
                }
            ]
        },
        separators=(",", ":"),
    )
    + "\n",
    encoding="utf-8",
)
PY
chmod 600 "$web_key"
chmod 644 "$web_jwks"

org_slug="context-fabric-local-$(openssl rand -hex 5)"
repo_name="acme/live-e2e"
cat >"$state_dir/compose.env" <<EOF_ENV
COMPOSE_PROJECT_NAME=dev-health-context-fabric
DEV_HEALTH_OPS_DIR=$ops_root
DEV_HEALTH_OPS_COMPOSE=$ops_root/compose.yml
DEV_HEALTH_WEB_DIR=$web_root
DEV_HEALTH_WEB_COMPOSE=$web_root/deploy/compose/web.compose.yml
DEV_HEALTH_NETWORK_NAME=dev-health-context-fabric
ACR_IMAGE=dev-health-acr:local
DEV_HEALTH_WEB_IMAGE=dev-health-web:local
BUGSINK_SECRET_KEY=$(random_hex)
POSTGRES_USER=postgres
ACR_RUNTIME_DB_USER=acr_runtime
ACR_MIGRATION_DB_USER=acr_migration
ACR_ENABLE_EPISODE_WRITEBACK=false
ACR_DEV_HEALTH_ENTITLEMENT_URL=https://acr-ops-tls:8443
ACR_LOCAL_STATE_DIR=$state_dir
ACR_LOCAL_PKI_DIR=$pki
ACR_POSTGRES_ADMIN_PASSWORD_FILE=$secrets/postgres-admin-password
ACR_RUNTIME_DB_PASSWORD_FILE=$secrets/acr-runtime-password
ACR_MIGRATION_DB_PASSWORD_FILE=$secrets/acr-migration-password
ACR_RUNTIME_DSN_FILE=$secrets/acr-runtime-dsn
ACR_MIGRATION_DSN_FILE=$secrets/acr-migration-dsn
ACR_CLICKHOUSE_DSN_FILE=$secrets/acr-clickhouse-dsn
ACR_OPS_TOKEN_FILE=$secrets/acr-ops-token
ACR_CA_FILE=$pki/ca.crt
ACR_EVIDENCE_ACTIVE_KID_FILE=$secrets/acr-evidence-kid
ACR_EVIDENCE_KEYS_FILE=$secrets/acr-evidence-keys
ACR_WEB_ASSERTION_KEY_FILE=$web_key
ACR_WEB_ASSERTION_JWKS_FILE=$web_jwks
ACR_WEB_ASSERTION_KID=$web_kid
ACR_WEB_ASSERTION_ISSUER=dev-health-web
ACR_WEB_ASSERTION_AUDIENCE=dev-health-acr
ACR_REQUEST_TIMEOUT_MS=5000
ACR_DB_NAME=$acr_db
ACR_EVIDENCE_DB=$evidence_db
ACR_LOCAL_ORG_SLUG=$org_slug
ACR_LOCAL_REPOSITORY=$repo_name
ACR_LOCAL_BRANCH=main
ACR_LOCAL_PORT=8444
DEV_HEALTH_WEB_PORT=3000
EOF_ENV
chmod 600 "$state_dir/compose.env"

{
  printf 'export PATH=%q:"$PATH"\n' "$state_dir"
  printf 'export ACR_API_URL=%q\n' 'https://localhost:8444'
  printf 'export ACR_API_CA_BUNDLE=%q\n' "$pki/ca.crt"
  printf 'export ACR_API_TOKEN_FILE=%q\n' "$secrets/acr-client-token"
  printf 'export ACR_LOCAL_INDEX_PROVIDER=%q\n' 'disabled'
  printf 'export ACR_ENABLE_WRITEBACK=%q\n' 'false'
  printf 'export ACR_SIDECAR_VERSION=%q\n' '1.0.0'
  printf 'export ACR_SIDECAR_CLIENT_VERSION=%q\n' '1.0.0'
  printf 'export ACR_LOCAL_TEST_REPOSITORY=%q\n' "$repo_name"
  printf 'export ACR_LOCAL_TEST_BRANCH=%q\n' 'main'
} >"$state_dir/client.env"
chmod 600 "$state_dir/client.env"

printf 'Context Fabric local state created at %s\n' "$state_dir"
printf 'Start the platform graph with:\n'
printf '  docker compose --env-file %q up --build --wait\n' "$state_dir/compose.env"
printf 'After readiness, create a client credential with the documented Compose job.\n'
