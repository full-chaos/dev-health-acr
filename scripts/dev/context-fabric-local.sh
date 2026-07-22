#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

# shellcheck disable=SC1091
source "$repo_root/scripts/e2e/compose.sh"

ops_dir="$repo_root/../dev-health-ops"
web_dir="$repo_root/../dev-health-web"
project="acr-e2e-local-${RANDOM}-${RANDOM}"
caller_root=""

usage() {
  cat >&2 <<USAGE
usage: $0 [--ops-dir <dev-health-ops>] [--web-dir <dev-health-web>] [--project <acr-e2e-name>]

Builds the containerized Context Fabric stack from the selected dev-health-ops
compose.yml plus ACR's canonical Compose overlay, verifies the hosted API and
host-local MCP sidecar, writes a sourceable client environment, and keeps the
stack alive until interrupted.
USAGE
}

while (($#)); do
  case "$1" in
    --ops-dir)
      ops_dir="${2:-}"
      shift 2
      ;;
    --web-dir)
      web_dir="${2:-}"
      shift 2
      ;;
    --project)
      project="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

[[ -d "$ops_dir" && -f "$ops_dir/compose.yml" && -f "$ops_dir/pyproject.toml" ]] || die "dev-health-ops checkout not found: $ops_dir"
[[ -d "$web_dir" && -f "$web_dir/package.json" ]] || die "dev-health-web checkout not found: $web_dir"
ops_dir="$(cd "$ops_dir" && pwd -P)"
web_dir="$(cd "$web_dir" && pwd -P)"
[[ "$project" =~ ^acr-e2e-[a-z0-9][a-z0-9-]{2,40}$ ]] || die 'project must be an isolated acr-e2e-* name'
[[ "$project" != dev-health && "$project" != default ]] || die 'refusing the operator default Compose project'

for tool in docker openssl curl git jq go python3 pgrep; do
  command -v "$tool" >/dev/null || die "$tool is required"
done
docker info >/dev/null 2>&1 || die 'Docker daemon is unavailable'
docker compose version >/dev/null 2>&1 || die 'Docker Compose v2 is required'
docker buildx version >/dev/null 2>&1 || die 'Docker Buildx is required'

finish_with_status() { return "$1"; }
cleanup_local() {
  local status=$?
  set +e
  if [[ -n "$caller_root" ]]; then
    rm -rf "$caller_root"
    caller_root=""
  fi
  finish_with_status "$status"
  cleanup
}
trap cleanup_local EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

render_ops_compose_root() {
  local rendered="$caller_root/ops.compose.json"
  local target="$caller_root/compose.yml"

  # Render the selected Ops Compose source first so build contexts and bind mounts
  # resolve against the real checkout. Interpolation stays disabled at this stage:
  # the disposable fixture must not copy optional credentials from the developer's
  # shell or Ops .env file, and its generated database values are supplied only at
  # the final Compose render. The generated caller root is not a checked-in copy.
  docker compose \
    --project-directory "$ops_dir" \
    -f "$ops_dir/compose.yml" \
    config --no-interpolate --format json >"$rendered"

  python3 - "$rendered" "$target" <<'PY'
from __future__ import annotations

import copy
import json
import sys
from pathlib import Path

source_path = Path(sys.argv[1])
target_path = Path(sys.argv[2])
source = json.loads(source_path.read_text(encoding="utf-8"))
required = ("postgres", "pgbouncer", "clickhouse", "valkey", "migrate", "api")
source_services = source.get("services") or {}
missing = [name for name in required if name not in source_services]
if missing:
    raise SystemExit(
        "dev-health-ops compose.yml is missing required Context Fabric services: "
        + ", ".join(missing)
    )

services: dict[str, dict[str, object]] = {}
for name in required:
    service = copy.deepcopy(source_services[name])
    # Fixed names and published ports would collide with an already-running
    # developer stack. Compose supplies project-scoped names, while the ACR
    # acceptance override publishes only its generated loopback TLS endpoint.
    service.pop("container_name", None)
    service["ports"] = []
    service["restart"] = "no"
    service["networks"] = {"dev-health": None}
    services[name] = service

# The verified ACR fixture generates a fresh PostgreSQL administrator password
# after this root is rendered. Preserve Ops' actual service/build definitions,
# but defer every PostgreSQL consumer to the fixture-owned variables that are
# exported before Compose performs the final render.
postgres_health = copy.deepcopy(services["postgres"].get("healthcheck") or {})
postgres_health["test"] = [
    "CMD-SHELL",
    "pg_isready -U ${POSTGRES_USER} -d devhealth",
]
services["postgres"]["healthcheck"] = postgres_health

services["pgbouncer"]["environment"] = {
    "DB_HOST": "postgres",
    "DB_PORT": "5432",
    "DB_USER": "${POSTGRES_USER}",
    "DB_PASSWORD": "${POSTGRES_PASSWORD}",
    "DB_NAME": "devhealth",
    "LISTEN_PORT": "6432",
    "POOL_MODE": "transaction",
    "AUTH_TYPE": "scram-sha-256",
    "MAX_CLIENT_CONN": "1000",
    "DEFAULT_POOL_SIZE": "25",
    "MIN_POOL_SIZE": "5",
    "RESERVE_POOL_SIZE": "5",
    "ADMIN_USERS": "${POSTGRES_USER}",
}
services["migrate"]["entrypoint"] = [
    "sh",
    "-c",
    (
        'if [ -n "$$POSTGRES_URI" ]; then dev-hops migrate postgres; fi '
        "&& dev-hops migrate clickhouse"
    ),
]
services["migrate"]["environment"] = {
    "POSTGRES_URI": (
        "postgresql+asyncpg://${POSTGRES_USER}:${POSTGRES_PASSWORD}"
        "@postgres:5432/devhealth"
    ),
    "CLICKHOUSE_URI": "clickhouse://default:ch@clickhouse:8123/default",
    "LOG_LEVEL": "INFO",
}

# Do not carry optional Stripe, email, telemetry, or error-reporting secrets from
# a developer's Ops environment into this disposable fixture. The allowlist is
# sufficient for the real Ops API, entitlement administration, and evidence
# seeding exercised by Context Fabric.
ops_runtime_dsn = (
    "postgresql+asyncpg://${POSTGRES_USER}:${POSTGRES_PASSWORD}"
    "@pgbouncer:6432/devhealth"
)
services["api"]["environment"] = {
    "SETUPTOOLS_SCM_PRETEND_VERSION_FOR_DEV_HEALTH_OPS": "0.0.0",
    "PYTHONPATH": "/app/src",
    "GRAPHQL_QUERY_TIMEOUT": "30",
    "DATABASE_URI": ops_runtime_dsn,
    "POSTGRES_URI": ops_runtime_dsn,
    "PGBOUNCER_TRANSACTION_MODE": "true",
    "CLICKHOUSE_URI": "clickhouse://default:ch@clickhouse:8123/default",
    "REDIS_URL": "redis://valkey:6379/1",
    "PROVIDER_SYNC_QUEUES_ENABLED": "true",
    "AUTO_RUN_MIGRATIONS": "false",
    "EMAIL_PROVIDER": "console",
    "EMAIL_FROM_ADDRESS": "noreply@example.com",
    "OTEL_ENABLED": "false",
    "SENTRY_DSN": "",
    "SENTRY_ENVIRONMENT": "development",
    "SENTRY_TRACES_RATE": "0",
    "SENTRY_SEND_PII": "false",
}

# The acceptance lifecycle historically starts Mailpit and renders a root-router
# key. They are support placeholders only; ACR is reached through its generated
# localhost TLS proxy and neither service carries product configuration.
services["mailpit"] = {
    "image": "axllent/mailpit:latest@sha256:b868afa176bfd6cce2323ea316cd99ccad77915e51e595748f6d786700ecf109",
    "restart": "no",
    "networks": {"dev-health": None},
}
services["traefik"] = {
    "image": "nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10",
    "restart": "no",
    "profiles": ["root-router-placeholder"],
    "networks": {"dev-health": None},
}

result = {
    "services": services,
    # Keep project-scoped storage names even though the canonical Ops render has
    # already expanded its own top-level project name.
    "volumes": {
        "postgres_data": {"driver": "local"},
        "clickhouse_data": {"driver": "local"},
    },
    "networks": {"dev-health": {}},
}
target_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
PY

  rm -f "$rendered"
}

write_client_environment() {
  local env_file="$STATE/client.env"
  {
    printf '# Generated by scripts/dev/context-fabric-local.sh. Contains paths, not token contents.\n'
    printf 'export PATH=%q:"$PATH"\n' "$STATE"
    printf 'export ACR_API_URL=%q\n' "https://localhost:${PORT}"
    printf 'export ACR_API_TOKEN_FILE=%q\n' "$STATE/secrets/acr-rotated-token"
    printf 'export ACR_API_CA_BUNDLE=%q\n' "$STATE/pki/ca.crt"
    printf 'export ACR_SIDECAR_VERSION=%q\n' '1.0.0'
    printf 'export ACR_SIDECAR_CLIENT_VERSION=%q\n' '1.0.0'
    printf 'export ACR_LOCAL_INDEX_PROVIDER=%q\n' 'disabled'
    printf 'export ACR_ENABLE_WRITEBACK=%q\n' 'false'
    printf 'export ACR_LOCAL_TEST_REPOSITORY=%q\n' 'acme/live-e2e'
    printf 'export ACR_LOCAL_TEST_BRANCH=%q\n' 'main'
    printf 'export DEV_HEALTH_ACR_DIR=%q\n' "$repo_root"
    printf 'export DEV_HEALTH_OPS_DIR=%q\n' "$ops_dir"
    printf 'export DEV_HEALTH_WEB_DIR=%q\n' "$web_dir"
  } >"$env_file"
  chmod 600 "$env_file"
  printf '%s\n' "$env_file"
}

cd "$repo_root"
export CONTAINER_ALLOW_DIRTY="${CONTAINER_ALLOW_DIRTY:-1}"
COMPOSE_FILE=""
OVERLAY_FILE="$repo_root/deploy/compose/acr.compose.yml"
PROJECT="$project"
SCENARIO=happy

mkdir -p "$repo_root/.tmp"
caller_root="$(mktemp -d "$repo_root/.tmp/context-fabric-caller.XXXXXX")"
ln -s "$ops_dir" "$caller_root/ops"
ln -s "$web_dir" "$caller_root/web"
render_ops_compose_root
COMPOSE_FILE="$caller_root/compose.yml"

assert_project_unused
prepare_state

# prepare_state preserves the historical meta-repository layout. Replace those
# staging links with the explicit sibling checkouts before Docker reads them.
rm -f "$STATE/stage/ops" "$STATE/stage/web"
ln -s "$ops_dir" "$STATE/stage/ops"
ln -s "$web_dir" "$STATE/stage/web"
rm -rf "$caller_root"
caller_root=""

ensure_image
render_override
assert_safe_render
start_happy

env_file="$(write_client_environment)"
note "PASS: ${SCENARIO}: ${SAFE_BOUNDARY}"
note "Context Fabric is ready for interactive client testing."
note "In a second shell run: source ${env_file}"
note "Then verify: acr-mcp doctor --live"
note "Seeded repository: acme/live-e2e (branch main)"
note "Press Ctrl-C here to remove the isolated containers, volumes, network, credentials, and generated files."

while sleep 5; do
  wait_https /readyz || die 'interactive Context Fabric stack lost readiness'
done
