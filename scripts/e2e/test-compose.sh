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

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
"$pki" --out "$tmp" --dns 'localhost,acr-ops-tls,127.0.0.1'
openssl verify -CAfile "$tmp/ca.crt" "$tmp/acr.crt" >/dev/null
openssl x509 -in "$tmp/acr.crt" -noout -ext subjectAltName | grep -q 'DNS:acr-ops-tls'
printf 'compose e2e static checks passed\n'
