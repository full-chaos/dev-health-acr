#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$root/scripts/e2e/compose.sh"
pki="$root/scripts/deploy/local-pki.sh"

bash -n "$script" "$pki"
bash -n "$root/scripts/e2e/test-acr-db-init.sh"
for scenario in happy existing-volume missing-ops-token invalid-ca revoked-acr-token clickhouse-read-denied migration-failure; do
  grep -Fq "$scenario" "$script"
done
grep -Fq 'refusing the operator default Compose project' "$script"
grep -Fq 'down --volumes --remove-orphans' "$script"
grep -Fq 'skip_verify=false' "$script"
grep -Fq 'curl --fail --silent --show-error --cacert' "$script"
grep -Fq 'MCP must remain host-local' "$script"
grep -Fq 'zero owned containers, volumes, and networks' "$script"
grep -Fq 'POSTGRES_USER=devhealth' "$script"
grep -Fq "ACR_IMAGE=\"\$IMAGE\"" "$script"
grep -Fq 'host_ip: 127.0.0.1' "$script"
grep -Fq "REDIS_PORT=\"\$redis_port\"" "$script"
grep -Fq "REPO_ROOT/.tmp/e2e/compose-\${PROJECT}" "$script"
grep -Fq 'command: ["cp /input/acr.crt' "$script"
grep -Fq 'chown -R 70:70 /postgres-tls' "$script"
grep -Fq 'Ops organization provisioning failed' "$script"
if grep -Fq -- '--owner-email' "$script"; then exit 1; fi
grep -Fq 'clickhouse-client --user default --password ch' "$script"
db_variable=db
expected_clickhouse_database="CREATE DATABASE IF NOT EXISTS \${${db_variable}}"
grep -Fq "$expected_clickhouse_database" "$script"
expected_clickhouse_migration="CLICKHOUSE_URI=clickhouse://default:ch@clickhouse:8123/\${${db_variable}} dev-hops migrate clickhouse"
grep -Fq "$expected_clickhouse_migration" "$script"
grep -Fq "INSERT INTO \${${db_variable}}.repos (id, repo, ref, created_at, settings, tags, last_synced, org_id, provider)" "$script"
grep -Fq 'project-scoped repository evidence provisioning failed' "$script"
grep -Fq 'CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT: "1"' "$script"
grep -Fq 'CLICKHOUSE_USER=default CLICKHOUSE_PASSWORD=ch' "$script"
grep -Fq 'redact_log()' "$script"
grep -Fq 'acr-migrate acr-api' "$script"
grep -Fq 'ACR_POSTGRES_MIGRATION_DSN:' "$script"
grep -Fq 'ACR_POSTGRES_MIGRATION_DSN=' "$script"
grep -Fq 'ACR_REQUIRE_BACKING_STORES: "true"' "$script"
grep -Fq "ACR_POSTGRES_DSN: \"\$(" "$script"
grep -Fq "ACR_CLICKHOUSE_DSN: \"\$(" "$script"
grep -Fq 'acr-credentials:' "$script"
grep -Fq 'compose run --rm --no-deps acr-credentials credentials create' "$script"
if grep -Fq 'compose run --rm --no-deps acr-api credentials' "$script"; then exit 1; fi
grep -Fq 'ACR_EVIDENCE_ID_ACTIVE_KID:' "$script"
grep -Fq 'random_base64()' "$script"
grep -Fq 'record_acl_probe()' "$script"
grep -Fq 'rotate_acr_credential()' "$script"
grep -Fq 'credentials rotate' "$script"
grep -Fq -- '--overlap 0s' "$script"
grep -Fq 'ACR_CLIENT_VERSION_HEADER=' "$script"
grep -Fq 'X-ACR-Client-Version: 1.0.0' "$script"
grep -Fq "\"\$ACR_CLIENT_VERSION_HEADER\"" "$script"
grep -Fq 'rotated ACR credential did not authenticate against the direct localhost endpoint' "$script"
grep -Fq 'previous ACR credential remained valid after immediate rotation' "$script"
grep -Fq "'\"status\":\"ok\"'" "$script"
grep -Fq '"method":"notifications/initialized","params":{}' "$script"
grep -Fq 'coproc ACR_MCP' "$script"
grep -Fq '.evidence_ref_ids? | arrays | .[]' "$script"
grep -Fq 'HTTPS readiness timed out' "$script"
grep -Fq 'acr_e2e_postgres_tls' "$script"
grep -Fq 'acr_e2e_clickhouse_tls' "$script"
grep -Fq 'chown -R 101:101 /clickhouse-tls' "$script"
grep -Fq 'clickhouse-client --secure --accept-invalid-certificate' "$script"
grep -Fq 'healthcheck: { test: ["NONE"] }' "$script"
grep -Fq 'acr-api: { condition: service_started }' "$script"
state_variable=STATE
expected_cleanup_guard="[[ ! -f \"\$${state_variable}/override.yml\" ]]"
grep -Fq "$expected_cleanup_guard" "$script"
if grep -Fq 'acr-secret-entrypoint' "$root/deploy/compose/acr.compose.yml"; then exit 1; fi
grep -Fq 'entrypoint: ["/usr/local/bin/acr-migrate"]' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'secrets: [acr_migration_dsn, acr_ca]' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'acr-db-acl:' "$root/deploy/compose/acr.compose.yml"
grep -Fq '["/usr/local/bin/acr-db-init", "runtime-acl"]' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'acr-db-acl: { condition: service_completed_successfully }' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'PostgreSQL readiness timed out' "$root/deploy/compose/acr-db-init.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
"$pki" --out "$tmp" --dns 'localhost,acr-ops-tls,127.0.0.1'
openssl verify -CAfile "$tmp/ca.crt" "$tmp/acr.crt" >/dev/null
openssl x509 -in "$tmp/acr.crt" -noout -ext subjectAltName | grep -q 'DNS:acr-ops-tls'
printf 'compose e2e static checks passed\n'
