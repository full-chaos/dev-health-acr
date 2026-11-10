#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$root/scripts/e2e/compose.sh"
script_dir="$root/scripts/e2e"
pki="$root/scripts/deploy/local-pki.sh"
receipt_validator="$root/scripts/e2e/validate-error-receipt.sh"

bash -n "$script" "$pki" "$receipt_validator"
bash -n "$root/scripts/e2e/test-acr-db-init.sh"
for scenario in happy existing-volume missing-ops-token invalid-ca revoked-acr-token clickhouse-read-denied migration-failure; do
  grep -Fq "$scenario" "$script"
done
grep -Fq 'refusing the operator default Compose project' "$script"
grep -Fq 'down --volumes --remove-orphans' "$script"
grep -Fq 'sslmode=disable' "$script"
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
if grep -Eq 'acr-ops-tls|acr-pki-init|acr_e2e_(postgres|clickhouse)_tls|sslmode=verify-full|clickhouse:9440|ACR_CLICKHOUSE_CA_BUNDLE|ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE' "$script"; then
  printf 'Compose E2E must not recreate internal TLS proxies or dependency PKI\n' >&2
  exit 1
fi
grep -Fq 'Ops organization provisioning failed' "$script"
# The ops internal acr routes accept only the acr-api workload token (ops CHAOS-12977): the harness writes the
# acr-api bootstrap credential, the readiness probe exchanges it at auth-service and sends the workload token, and
# the svc_acr_ service credential is then measured to get 401 on the same route.
grep -Fq 'http://auth-service:8095/internal/v1/token/exchange' "$script"
grep -Fq 'urn%3Adev-health%3Aparams%3Aoauth%3Atoken-type%3Aworkload-bootstrap' "$script"
grep -Fq -- '--post-file /tmp/form' "$script"
# The bootstrap credential never reaches a command line: it is read from the volume into a tmpfs form file.
# shellcheck disable=SC2016 # the pin is a literal source line.
grep -Fq 'b="$(enc < /run/workload/acr-api.bootstrap)"' "$script"
if sed -n '/^workload_probe_script=/,/^$/p' "$script" | grep -nE -- '--post-data'; then
  printf 'the bootstrap credential must be posted from a file, not a command line\n' >&2
  exit 1
fi
# shellcheck disable=SC2016 # the pin is a literal source line.
sed -n '/^wait_go_api()/,/^}/p' "$script" | grep -Fq 'workload_probe "${go_api_internal}${acr_health_path}"'
# The svc_acr_ control: the token reaches the probe by environment name, and anything but 401 fails the run.
grep -Fq -- '-e ACR_PROBE_TOKEN -e ACR_PROBE_URL --entrypoint sh acr-tls-proxy -c' "$script"
# shellcheck disable=SC2016 # the pin is a literal source line.
grep -Fq -- '--header "Authorization: Bearer $ACR_PROBE_TOKEN" "$ACR_PROBE_URL"' "$script"
grep -Fq 'acr_health_path=/api/v1/internal/acr/health' "$script"
grep -Fq 'want 401' "$script"
if sed -n '/^wait_go_api()/,/^}/p' "$script" | grep -nE 'Bearer \$\{token\}|Bearer \$token'; then
  printf 'the readiness probe must not expand a bearer value on this side\n' >&2
  exit 1
fi
grep -Fq 'mint_ops_service_token()' "$script"
mint_line="$(grep -n 'token="$(mint_ops_service_token)"' "$script" | head -n 1 | cut -d: -f1)"
bootstrap_line="$(grep -n '^  provision_acr_workload$' "$script" | head -n 1 | cut -d: -f1)"
wait_line="$(grep -n '^  wait_go_api$' "$script" | head -n 1 | cut -d: -f1)"
control_line="$(grep -n '^  assert_service_credential_refused$' "$script" | head -n 1 | cut -d: -f1)"
[[ -n "$mint_line" && -n "$bootstrap_line" && -n "$wait_line" && -n "$control_line" ]]
[[ "$mint_line" -lt "$bootstrap_line" && "$bootstrap_line" -lt "$wait_line" && "$wait_line" -lt "$control_line" ]]
if grep -Fq 'wget -q -O /dev/null http://go-api:8091' "$script"; then
  printf 'the ops readiness probe must send a credential\n' >&2
  exit 1
fi
grep -Fq 'rejected the acr-api workload token (last HTTP status' "$script"
# acr-api itself sends the workload token: no service-token file reaches it.
grep -Fq 'ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL: workload' "$script"
if sed -n '/^  acr-api:$/,/^  acr-credentials:$/p' "$script" | grep -Fq 'acr_ops_token'; then
  printf 'acr-api must not mount the service token in workload mode\n' >&2
  exit 1
fi
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
expected_clickhouse_migration="DHO_CLICKHOUSE_URI=\"clickhouse://default:ch@clickhouse:9000/\${${db_variable}}\" dho migrate clickhouse upgrade"
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
grep -Fq 'run_oneshot()' "$script"
if grep -Eq 'compose wait ' "$script"; then exit 1; fi
grep -Fq 'ACR_POSTGRES_MIGRATION_DSN_FILE: /run/secrets/acr_migration_dsn' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'ACR_REQUIRE_BACKING_STORES: "true"' "$script"
if grep -Eq 'ACR_(POSTGRES_DSN|CLICKHOUSE_DSN|EVIDENCE_ID_ACTIVE_KID|EVIDENCE_ID_KEYS):' "$script"; then exit 1; fi
grep -Fq 'acr-credentials:' "$script"
grep -Fq 'compose run --rm --no-deps acr-credentials credentials create' "$script"
if grep -Fq 'compose run --rm --no-deps acr-api credentials' "$script"; then exit 1; fi
grep -Fq 'ACR_EVIDENCE_ID_ACTIVE_KID_FILE: /run/secrets/acr_evidence_active_kid' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'ACR_REQUIRE_BACKING_STORES: "true"' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'ACR_DEVICE_VERIFICATION_URL:' "$root/deploy/compose/acr.compose.yml"
if grep -Eq 'ACR_DEV_HEALTH_ENTITLEMENT_(URL|TOKEN_FILE|CA_BUNDLE)|acr_ops_token|api: \{ condition: service_healthy \}' "$root/deploy/compose/acr.compose.yml"; then
  printf 'root-local Compose must use the offline development entitlement provider\n' >&2
  exit 1
fi
if grep -Eq 'ACR_ALLOW_INSECURE_POSTGRES|ACR_CLICKHOUSE_CA_BUNDLE|acr_ca|ACR_CA_FILE' "$root/deploy/compose/acr.compose.yml"; then
  printf 'root-local Compose must not require an internal transport override or CA bundle\n' >&2
  exit 1
fi
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
grep -F -A 4 'compose up -d postgres clickhouse valkey pgbouncer mailpit migrate go-api-roles go-api >/dev/null' "$script" | grep -Fq "run_mcp \"\$(<\"\$STATE/secrets/acr-rotated-token\")\""
# CHAOS-11890: acr-api must start only after the ops api internal listener answers (it exits on a failed boot entitlement check).
awk '/^ensure_go_api_ready\(\)/,/^}/' "$script" | grep -Fq 'wait_go_api'
awk '/^up_acr_services\(\)/,/^}/' "$script" | awk '/ensure_go_api_ready/{r=NR} /compose up/{a=NR} END{exit !(r && a && r<a)}'
awk '/^start_acr_after_go_api\(\)/,/^}/' "$script" | grep -Fq 'up_acr_services acr-db-init acr-migrate acr-api acr-tls-proxy'
# The start must not recreate go-api behind the readiness proof, and must fail loudly if it did (run 40960897433).
awk '/^up_acr_services\(\)/,/^}/' "$script" | grep -Fq 'compose up -d --no-recreate "$@"'
awk '/^up_acr_services\(\)/,/^}/' "$script" | grep -Fq 'go-api was recreated while starting'
# A same-container restart keeps the id: acr-api must also be seen running through a settle window.
awk '/^up_acr_services\(\)/,/^}/' "$script" | grep -Fq 'assert_acr_api_survives_boot'
awk '/^assert_acr_api_survives_boot\(\)/,/^}/' "$script" | grep -Fq "!= 'running'"
# Every start of acr-api or acr-projector in the e2e scripts goes through up_acr_services: no direct `compose up` names them.
if grep -nE 'compose up .*(acr-api|acr-projector)' "$script_dir"/compose.sh "$script_dir"/fixture-graph.sh "$script_dir"/svs.sh "$script_dir"/fullstack-opencode.sh | grep -v '^[^:]*:[0-9]*:[[:space:]]*#'; then exit 1; fi
# The failure-log health dump names only services the harness compose gives a healthcheck (the retired api service has none).
grep -Fq "for service in postgres pgbouncer clickhouse valkey; do" "$script"
! grep -Fq "for service in clickhouse api; do" "$script"
grep -Fq 'inject_existing_volume_drift' "$script"
grep -Fq 'reconciled role, password, ownership, and ACL drift' "$script"
grep -Fq "'\"status\":\"ok\"'" "$script"
grep -Fq '"method":"notifications/initialized","params":{}' "$script"
grep -Fq 'coproc ACR_MCP' "$script"
grep -Fq '.evidence_ref_ids? | arrays | .[]' "$script"
grep -Fq 'HTTPS readiness timed out' "$script"
if grep -Fq -- '--accept-invalid-certificate' "$script"; then exit 1; fi
if grep -Fq -- '--insecure' "$script"; then exit 1; fi
grep -Fq 'clickhouse-client --config-file=/run/acr-e2e/clickhouse-client-readonly.xml --host clickhouse --port 9000 --user acr_reader' "$script"
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
grep -Fq 'secrets: [acr_migration_dsn]' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'acr-db-acl:' "$root/deploy/compose/acr.compose.yml"
[[ "$(grep -Fc 'entrypoint: ["/usr/local/bin/acr-db-init"]' "$root/deploy/compose/acr.compose.yml")" -eq 2 ]]
grep -Fq 'command: ["roles"]' "$root/deploy/compose/acr.compose.yml"
grep -Fq 'command: ["runtime-acl"]' "$root/deploy/compose/acr.compose.yml"
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
  'postgres:18-alpine@sha256:10a5c69a83035ab5239f5620d708520e8b28f5aa1f2b20143e6024532edadfb5' \
  'clickhouse/clickhouse-server:latest@sha256:739e099abd1157390960a59e4532d21781d8fde57e6e4151cb920ea02c30b1ce' \
  'edoburu/pgbouncer:latest@sha256:8638aa563607256d3c7440437534195f1066a92b68015d89ae9d6b935b444a6d' \
  'valkey/valkey:9-alpine@sha256:bf09d58e96d6b4524a15396b65b242bbe18beebbcc15a04d8ee75ddac62fae0b' \
  'axllent/mailpit:latest@sha256:9b3a463be15354c0f0eba91697d80e3e0d8a5623ef0e627cacc836bc17fd5231' \
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
"$pki" --out "$tmp" --dns 'localhost,acr-tls-proxy,127.0.0.1'
openssl verify -CAfile "$tmp/ca.crt" "$tmp/acr.crt" >/dev/null
openssl x509 -in "$tmp/acr.crt" -noout -ext subjectAltName | grep -q 'DNS:acr-tls-proxy'

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
