#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$root/scripts/e2e/compose.sh"
pki="$root/scripts/deploy/local-pki.sh"
receipt_validator="$root/scripts/e2e/validate-error-receipt.sh"

bash -n "$script" "$pki" "$receipt_validator"
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
grep -Fq "trap '' INT TERM HUP" "$script"
grep -Fq "trap 'exit 129' HUP" "$script"
grep -Fq 'refusing pre-existing Compose project resources' "$script"
grep -Fq 'POSTGRES_USER=devhealth' "$script"
grep -Fq "ACR_IMAGE=\"\$IMAGE\"" "$script"
grep -Fq 'host_ip: 127.0.0.1' "$script"
grep -Fq "REDIS_PORT=\"\$redis_port\"" "$script"
grep -Fq "REPO_ROOT/.tmp/e2e/compose-\${PROJECT}" "$script"
grep -Fq 'command: ["cp /input/acr.crt' "$script"
grep -Fq 'chown -R 70:70 /postgres-tls' "$script"
grep -Fq 'Ops organization provisioning failed' "$script"
grep -Fq 'actual_status="$?"' "$script"
grep -Fq 'expected failure diagnostics' "$script"
grep -Fq 'for tool in docker openssl curl git jq' "$script"
grep -Fq "expect_failure 60 'SSL certificate problem'" "$script"
grep -Fq 'openssl req -x509 -newkey rsa:2048' "$script"
if grep -Fq 'not a certificate' "$script"; then exit 1; fi
revoked_acr_token_case="$(sed -n '/revoked-acr-token)/,/;;/p' "$script")"
printf '%s\n' "$revoked_acr_token_case" | grep -Fq 'expect_typed_http_error 401 invalid_token'
printf '%s\n' "$revoked_acr_token_case" | grep -Fq 'acr-rotated-token'
grep -Fq "http_status=\"\$(\"\$@\" --output \"\$response_body\" --write-out '%{http_code}')\"" "$script"
grep -Fq "if [[ \"\$http_status\" != \"\$expected_http_status\" ]]; then" "$script"
grep -Fq 'validate-error-receipt.sh' "$script"
test "$(grep -Fc 'response_body' "$script")" -eq 4
if grep -Fq "cat \"\$response_body\"" "$script"; then exit 1; fi
grep -Fq "expect_failure 164 'Code: 164.*readonly'" "$script"
grep -Fq "expect_failure 1 'apply migration 2'" "$script"
grep -Fq 'migrations/postgres/0001_acr_core.sql' "$script"
grep -Fq 'agent_episodes_org_repo_idempotency_key_key CHECK (false)' "$script"
grep -Fq 'partial state and API stayed gated' "$script"
if grep -Fq 'system_metrics' "$script"; then exit 1; fi
grep -Fq 'select(.revoked_at == null)' "$script"
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
grep -Fq 'ALTER USER acr_reader SETTINGS readonly = 2;' "$script"
grep -Fq "CREATE TABLE IF NOT EXISTS \${db}.acr_e2e_readonly_probe" "$script"
grep -Fq "GRANT INSERT ON \${db}.acr_e2e_readonly_probe TO acr_reader;" "$script"
grep -Fq "INSERT INTO acr_\${PROJECT//-/}_e2e.acr_e2e_readonly_probe" "$script"
grep -Fq 'redact_log()' "$script"
grep -Fq 'acr-migrate acr-api' "$script"
grep -Fq 'ACR_POSTGRES_MIGRATION_DSN_FILE: /run/secrets/acr_migration_dsn' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'ACR_REQUIRE_BACKING_STORES: "true"' "$script"
if grep -Eq 'ACR_(POSTGRES_DSN|CLICKHOUSE_DSN|EVIDENCE_ID_ACTIVE_KID|EVIDENCE_ID_KEYS):' "$script"; then exit 1; fi
grep -Fq 'acr-credentials:' "$script"
grep -Fq 'compose run --rm --no-deps acr-credentials credentials create' "$script"
if grep -Fq 'compose run --rm --no-deps acr-api credentials' "$script"; then exit 1; fi
grep -Fq 'ACR_EVIDENCE_ID_ACTIVE_KID_FILE: /run/secrets/acr_evidence_active_kid' "$root/deploy/compose/acr.compose.yml"
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
grep -F -A 3 'compose up -d postgres clickhouse valkey pgbouncer mailpit migrate api acr-ops-tls acr-db-init acr-migrate acr-api acr-tls-proxy >/dev/null' "$script" | grep -Fq "run_mcp \"\$(<\"\$STATE/secrets/acr-rotated-token\")\""
grep -Fq 'inject_existing_volume_drift' "$script"
grep -Fq 'reconciled role, password, ownership, and ACL drift' "$script"
grep -Fq "'\"status\":\"ok\"'" "$script"
grep -Fq '"method":"notifications/initialized","params":{}' "$script"
grep -Fq 'coproc ACR_MCP' "$script"
grep -Fq '.evidence_ref_ids? | arrays | .[]' "$script"
grep -Fq 'HTTPS readiness timed out' "$script"
grep -Fq 'acr_e2e_postgres_tls' "$script"
grep -Fq 'acr_e2e_clickhouse_tls' "$script"
grep -Fq 'chown -R 101:101 /clickhouse-tls' "$script"
if grep -Fq -- '--accept-invalid-certificate' "$script"; then exit 1; fi
if grep -Fq -- '--insecure' "$script"; then exit 1; fi
grep -Fq '<verificationMode>strict</verificationMode>' "$script"
grep -Fq '<name>RejectCertificateHandler</name>' "$script"
grep -Fq 'clickhouse-client --config-file=/run/acr-e2e/clickhouse-client-readonly.xml --secure --host clickhouse --port 9440 --user acr_reader' "$script"
dollar='$'
password_argument="--password=\"${dollar}(<\"${dollar}STATE/secrets/clickhouse-password\")\""
if grep -Fq -- "$password_argument" "$script"; then exit 1; fi
grep -Fq 'write_clickhouse_readonly_client_config()' "$script"
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
grep -Fq 'POSTGRES_PASSWORD_FILE' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'ACR_RUNTIME_DB_PASSWORD_FILE' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'ACR_MIGRATION_DB_PASSWORD_FILE' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'ACR_POSTGRES_ADMIN_PASSWORD_FILE' "$root/docs/service-shell.md"
grep -Fq 'ACR_RUNTIME_DB_PASSWORD_FILE' "$root/docs/service-shell.md"
grep -Fq 'ACR_MIGRATION_DB_PASSWORD_FILE' "$root/docs/service-shell.md"
grep -Fq "mode \`0600\`" "$root/docs/service-shell.md"
grep -Fq 'mutually exclusive; there is no fallback precedence' "$root/docs/service-shell.md"
grep -Fq 'ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD %L' "$root/deploy/compose/acr-db-init.sh"
grep -Fq 'ALTER DATABASE %I OWNER TO %I' "$root/deploy/compose/acr-db-init.sh"
grep -Fq 'REVOKE ALL ON TABLES FROM :"runtime_user"' "$root/deploy/compose/acr-db-init.sh"
if grep -Fq 'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"runtime_user"' "$root/deploy/compose/acr-db-init.sh"; then exit 1; fi
for image in \
  'postgres:18-alpine@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15' \
  'clickhouse/clickhouse-server:latest@sha256:fdc22372465a336fa47e9deab61fad8277b9e2f2473234a1294b33b53f01d377' \
  'edoburu/pgbouncer:latest@sha256:4c1ca296ef525f108f5d3552cc337c0c09587cf8dae7f0067fd93349e47dc1cd' \
  'valkey/valkey:9-alpine@sha256:ee91f7a174ac4d6a6b0685b3a60e321f0a9dbbb691f9b0e285be2ba1d1be8328' \
  'axllent/mailpit:latest@sha256:b868afa176bfd6cce2323ea316cd99ccad77915e51e595748f6d786700ecf109' \
  'nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10'; do
  grep -Fq "$image" "$root/deploy/compose/acr.compose.yml" "$script" "$root/scripts/e2e/test-acr-db-init.sh"
  pin="${image/@sha256:/|sha256:}"
  grep -Fq "$pin" "$root/scripts/container/verify-pins.sh"
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  ps|volume|network) exit 0 ;;
  compose)
    case "${2:-}" in
      ls) printf '[{"Name":"acr-e2e-collision"}]\n' ;;
      *) printf 'unexpected docker compose invocation: %s\n' "$*" >&2; exit 99 ;;
    esac
    ;;
  *) printf 'unexpected docker invocation: %s\n' "$*" >&2; exit 99 ;;
esac
EOF
chmod +x "$tmp/bin/docker"
touch "$tmp/compose.yml" "$tmp/overlay.yml"
mkdir -p "$tmp/no-jq-bin"
for tool in docker openssl curl git dirname; do
  ln -s "$(command -v "$tool")" "$tmp/no-jq-bin/$tool"
done
set +e
PATH="$tmp/no-jq-bin" /bin/bash "$script" --compose "$tmp/compose.yml" --overlay "$tmp/overlay.yml" --project acr-e2e-collision --scenario happy >"$tmp/no-jq.log" 2>&1
missing_jq_status=$?
set -e
test "$missing_jq_status" -eq 1
grep -Fq 'jq is required' "$tmp/no-jq.log"
set +e
PATH="$tmp/bin:$PATH" bash "$script" --compose "$tmp/compose.yml" --overlay "$tmp/overlay.yml" --project acr-e2e-collision --scenario happy >"$tmp/collision.log" 2>&1
collision_status=$?
set -e
test "$collision_status" -eq 1
grep -Fq 'refusing pre-existing Compose project name' "$tmp/collision.log"
valid_receipt="$tmp/valid-receipt.json"
cat > "$valid_receipt" <<'EOF'
{"schema_version":"error.v1","request_id":"req_test","error":{"code":"invalid_token","message":"token is invalid","http_status":401,"retryable":false,"details":{}}}
EOF
"$receipt_validator" 401 invalid_token 401 "$valid_receipt"
for response in \
  '{"code":"invalid_token"}' \
  '{"schema_version":"error.v1","request_id":"req_test","error":{"code":"invalid_token","message":"token is invalid","http_status":401,"retryable":false,"unexpected":true}}' \
  '{"schema_version":"error.v1","request_id":"req_test","error":{"code":"invalid_token","message":"token is invalid","http_status":403,"retryable":false}}' \
  '{"schema_version":"error.v1","request_id":"req_test","error":{"code":"repo_forbidden","message":"token is invalid","http_status":401,"retryable":false}}' \
  'not-json'; do
  response_file="$tmp/receipt.json"
  printf '%s\n' "$response" > "$response_file"
  if "$receipt_validator" 401 invalid_token 401 "$response_file"; then
    exit 1
  fi
done
if "$receipt_validator" 401 invalid_token 500 "$valid_receipt"; then
  exit 1
fi
"$pki" --out "$tmp" --dns 'localhost,acr-ops-tls,127.0.0.1'
openssl verify -CAfile "$tmp/ca.crt" "$tmp/acr.crt" >/dev/null
openssl x509 -in "$tmp/acr.crt" -noout -ext subjectAltName | grep -q 'DNS:acr-ops-tls'

compose_library="$tmp/compose-library.sh"
sed '/^assert_project_unused$/,$d' "$script" > "$compose_library"
set -- --compose "$tmp/compose.yml" --overlay "$tmp/overlay.yml" --project acr-e2e-secret-safe --scenario clickhouse-read-denied
test_cleanup() { rm -rf "$tmp"; }
trap test_cleanup EXIT
# shellcheck source=/dev/null
source "$compose_library"
trap -p EXIT > "$tmp/exit-trap.txt"
grep -Fq "trap -- 'test_cleanup' EXIT" "$tmp/exit-trap.txt"
trap -p INT > "$tmp/int-trap.txt"
test ! -s "$tmp/int-trap.txt"
trap -p TERM > "$tmp/term-trap.txt"
test ! -s "$tmp/term-trap.txt"
STATE="$tmp/state"
export PROJECT='acr-e2e-secret-safe'
export OVERLAY_FILE="$tmp/overlay.yml"
export SCENARIO='clickhouse-read-denied'
mkdir -p "$STATE/secrets" "$STATE/clickhouse" "$STATE/stage"
touch "$STATE/override.yml"
password="$(cat <<'EOF'
- starts & < > " ' $() ; | `
EOF
)"
write_secret "$STATE/secrets/clickhouse-password" "$password"
write_clickhouse_readonly_client_config
config_mode="$(stat -c '%a' "$STATE/clickhouse/client-readonly.xml" 2>/dev/null || stat -f '%Lp' "$STATE/clickhouse/client-readonly.xml")"
test "$config_mode" = 600
escaped_password="<password>- starts &amp; &lt; &gt; &quot; &apos; ${dollar}() ; | \`</password>"
grep -Fq "$escaped_password" "$STATE/clickhouse/client-readonly.xml"
fake_docker_args="$tmp/fake-docker-args.log"
cat > "$tmp/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "$FAKE_DOCKER_ARGS"
if [[ "$*" == *'clickhouse-client'* ]]; then
  printf '%s\n' "${FAKE_CLICKHOUSE_OUTPUT:-Code: 164. DB::Exception: Cannot execute query in readonly mode}"
  exit "${FAKE_CLICKHOUSE_EXIT:-164}"
fi
EOF
chmod +x "$tmp/bin/docker"
PATH="$tmp/bin:$PATH" FAKE_DOCKER_ARGS="$fake_docker_args" FAKE_CLICKHOUSE_EXIT=164 expect_clickhouse_readonly_denial
grep -Fq -- '--config-file=/run/acr-e2e/clickhouse-client-readonly.xml' "$fake_docker_args"
if grep -Fq -- "$password" "$fake_docker_args"; then exit 1; fi
for fake_exit in 1 165; do
  set +e
  ( PATH="$tmp/bin:$PATH" FAKE_DOCKER_ARGS="$fake_docker_args" FAKE_CLICKHOUSE_EXIT="$fake_exit" expect_clickhouse_readonly_denial ) >"$tmp/clickhouse-failure.log" 2>&1
  fake_status=$?
  set -e
  test "$fake_status" -eq 1
done
set +e
( PATH="$tmp/bin:$PATH" FAKE_DOCKER_ARGS="$fake_docker_args" FAKE_CLICKHOUSE_EXIT=164 FAKE_CLICKHOUSE_OUTPUT='Code: 164. unrelated failure' expect_clickhouse_readonly_denial ) >"$tmp/clickhouse-failure.log" 2>&1
fake_status=$?
set -e
test "$fake_status" -eq 1
set +e
( PATH="$tmp/bin:$PATH" FAKE_DOCKER_ARGS="$fake_docker_args" cleanup )
cleanup_status=$?
set -e
test "$cleanup_status" -eq 0
test ! -e "$STATE/clickhouse/client-readonly.xml"
failure_state="$tmp/failure-state"
failure_cleanup() {
  local STATE="$1"
  mkdir -p "$STATE/secrets" "$STATE/clickhouse"
  touch "$STATE/override.yml" "$STATE/secrets/password" "$STATE/clickhouse/client.xml"
  false
  PATH="$tmp/bin:$PATH" FAKE_DOCKER_ARGS="$fake_docker_args" cleanup
}
set +e
( failure_cleanup "$failure_state" )
failure_cleanup_status=$?
set -e
test "$failure_cleanup_status" -eq 1
test ! -e "$failure_state"

signal_bin="$tmp/signal-bin"
mkdir -p "$signal_bin"
cat > "$signal_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
exit 0
EOF
chmod +x "$signal_bin/docker"
cleanup_runner="$tmp/cleanup-runner.sh"
cat > "$cleanup_runner" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
set -- --compose "$CASE_STATE/compose.yml" --overlay "$CASE_STATE/overlay.yml" --project acr-e2e-signal --scenario happy
source "$COMPOSE_LIBRARY"
STATE="$CASE_STATE"
PROJECT=acr-e2e-signal
OVERLAY_FILE="$CASE_STATE/overlay.yml"
mkdir -p "$STATE/stage" "$STATE/secrets" "$STATE/clickhouse"
touch "$STATE/override.yml" "$STATE/secrets/password" "$STATE/clickhouse/client.xml"
compose() {
  if [[ "${1:-}" == down ]]; then
    if [[ -n "${CLEANUP_SIGNAL:-}" ]]; then
      kill "-$CLEANUP_SIGNAL" "$$"
    fi
    sleep 0.1
  fi
}
owned_receipt() { printf 'containers= volumes= networks='; }
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
trap cleanup EXIT
case "$CASE_MODE" in
  failure) false ;;
  success) : ;;
  signal-int) CLEANUP_SIGNAL=INT; kill -INT "$$" ;;
  signal-term) CLEANUP_SIGNAL=TERM; kill -TERM "$$" ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$cleanup_runner"

run_cleanup_case() {
  local mode="$1" expected_status="$2"
  local case_state="$tmp/case-$mode" status
  mkdir -p "$case_state"
  : > "$case_state/compose.yml"
  : > "$case_state/overlay.yml"
  set +e
  CASE_MODE="$mode" CASE_STATE="$case_state" COMPOSE_LIBRARY="$compose_library" \
    PATH="$signal_bin:$PATH" "$cleanup_runner" >"$case_state/output.log" 2>&1
  status=$?
  set -e
  test "$status" -eq "$expected_status"
  test ! -e "$case_state"
}

run_cleanup_case failure 1
run_cleanup_case success 0
run_cleanup_case signal-int 130
run_cleanup_case signal-term 143
printf 'compose e2e static checks passed\n'
