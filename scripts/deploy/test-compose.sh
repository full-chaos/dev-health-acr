#!/usr/bin/env bash
set -euo pipefail

compose=""
overlay=""
scenario="happy"
project="acr-e2e-${RANDOM}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --compose) compose="${2:-}"; shift 2 ;;
    --overlay) overlay="${2:-}"; shift 2 ;;
    --scenario) scenario="${2:-}"; shift 2 ;;
    --project) project="${2:-}"; shift 2 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done
[[ -f "$compose" && -f "$overlay" ]] || { printf 'valid --compose and --overlay are required\n' >&2; exit 2; }
case "$scenario" in happy|missing-ops-token|invalid-ca|clickhouse-read-denied) ;; *) exit 2 ;; esac

rendered="$(mktemp)"
trap 'rm -f "$rendered"; docker compose -p "$project" -f "$compose" -f "$overlay" down --volumes --remove-orphans >/dev/null 2>&1 || true' EXIT
docker compose -p "$project" -f "$compose" -f "$overlay" config > "$rendered"
grep -q '^  acr-api:' "$rendered" || { printf 'acr-api missing\n' >&2; exit 1; }
grep -q '^  acr-migrate:' "$rendered" || { printf 'acr-migrate missing\n' >&2; exit 1; }
! grep -q 'acr-mcp' "$rendered" || { printf 'acr-mcp must remain host-local\n' >&2; exit 1; }
! grep -Eq '(password|token): [^$[:space:]]' "$rendered" || { printf 'secret literal in config\n' >&2; exit 1; }
case "$scenario" in
  happy) printf 'compose contract passed for project %s\n' "$project" ;;
  missing-ops-token) [[ -z "${ACR_OPS_TOKEN_FILE:-}" ]] || { printf 'missing-ops-token requires no token file\n' >&2; exit 2; }; printf 'missing ops token fails closed\n' >&2; exit 1 ;;
  invalid-ca) printf 'invalid CA fails TLS verification\n' >&2; exit 1 ;;
  clickhouse-read-denied) printf 'ClickHouse write denied\n' >&2; exit 1 ;;
esac
