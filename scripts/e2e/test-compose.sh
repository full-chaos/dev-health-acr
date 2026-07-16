#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$root/scripts/e2e/compose.sh"
pki="$root/scripts/deploy/local-pki.sh"

bash -n "$script" "$pki"
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
grep -Fq 'chown -R 70:70 /tls' "$script"
grep -Fq 'Ops organization provisioning failed' "$script"
if grep -Fq -- '--owner-email' "$script"; then exit 1; fi
grep -Fq 'clickhouse-client --user default --password ch' "$script"
grep -Fq 'CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT: "1"' "$script"
grep -Fq 'CLICKHOUSE_USER=default CLICKHOUSE_PASSWORD=ch' "$script"
grep -Fq 'redact_log()' "$script"
grep -Fq 'acr-migrate acr-api' "$script"
grep -Fq 'ACR_POSTGRES_MIGRATION_DSN:' "$script"
grep -Fq 'ACR_POSTGRES_MIGRATION_DSN=' "$script"
grep -Fq 'ACR_REQUIRE_BACKING_STORES: "true"' "$script"
grep -Fq 'ACR_EVIDENCE_ID_ACTIVE_KID:' "$script"
grep -Fq 'healthcheck: { test: ["NONE"] }' "$script"
grep -Fq 'acr-api: { condition: service_started }' "$script"
expected_cleanup_guard=$(printf '%s' '[[ ! -f "$STATE/override.yml" ]]')
grep -Fq "$expected_cleanup_guard" "$script"
if grep -Fq 'acr-secret-entrypoint' "$root/deploy/compose/acr.compose.yml"; then exit 1; fi
grep -Fq 'entrypoint: ["/usr/local/bin/acr-migrate"]' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'secrets: [acr_migration_dsn, acr_ca]' "$root/deploy/compose/acr.compose.yml"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
"$pki" --out "$tmp" --dns 'localhost,acr-ops-tls,127.0.0.1'
openssl verify -CAfile "$tmp/ca.crt" "$tmp/acr.crt" >/dev/null
openssl x509 -in "$tmp/acr.crt" -noout -ext subjectAltName | grep -q 'DNS:acr-ops-tls'
printf 'compose e2e static checks passed\n'
