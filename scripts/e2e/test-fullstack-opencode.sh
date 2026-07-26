#!/usr/bin/env bash
set -euo pipefail

# Offline contract checks for the full-stack Context Fabric acceptance gate (CHAOS-3065).
# These run in `make verify` without Docker, a network, or OpenCode, and pin the safety
# invariants the real gate depends on so they cannot regress silently.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$root/scripts/e2e/fullstack-opencode.sh"
driver="$root/scripts/e2e/compose.sh"
compose_overlay="$root/deploy/compose/acr.compose.yml"
fixtures="$root/testdata/fullstack/v1"
template="$root/tests/fullstack/opencode/opencode.json.template"
prompt="$root/tests/fullstack/opencode/task-prompt.md"

fail() { printf '[fullstack-contract] FAIL: %s\n' "$*" >&2; exit 1; }

bash -n "$script"
command -v jq >/dev/null || fail 'jq is required'

# --- the shared driver still exposes the seam this suite depends on -----------------------
grep -Fq 'ACR_E2E_SEED_HOOK' "$driver" || fail 'compose.sh lost the evidence seeding hook'
grep -Fq 'ACR_E2E_REPOSITORY_SCOPE' "$driver" || fail 'compose.sh lost the repository scope override'
grep -Fq 'prepare_stack()' "$driver" || fail 'compose.sh lost the prepared-stack boundary'
grep -Fq 'assert_scoped_repository' "$driver" || fail 'compose.sh lost the single-repository assertion'
grep -Fq 'SETUPTOOLS_SCM_PRETEND_VERSION: "0.0.0"' "$driver" \
  || fail 'the staged Ops API must receive a deterministic setuptools-scm version'
grep -Fq 'jwt="$(random_secret)"' "$driver" \
  || fail 'the isolated Ops API must generate a per-run JWT secret'
grep -Fq 'JWT_SECRET_KEY: "${jwt}"' "$driver" \
  || fail 'the isolated Ops API must receive its generated JWT secret'
[[ "$(grep -Fc 'environment: !override' "$driver")" -eq 2 ]] \
  || fail 'the ACR migration and runtime environments must replace inherited local-dev variables'
[[ "$(grep -Fc 'ports: !override []' "$driver")" -eq 4 ]] \
  || fail 'the acceptance overlay must remove inherited infrastructure and ACR API ports'
grep -Fq 'volumes: !override []' "$driver" \
  || fail 'the ACR API must remove inherited local-dev bind mounts'
grep -Fq 'depends_on: !override' "$driver" \
  || fail 'the ACR API must replace inherited local-dev dependencies'
if grep -Eq 'ACR_(POSTGRES_MIGRATION_DSN|POSTGRES_DSN|CLICKHOUSE_DSN|ALLOW_INSECURE_POSTGRES): !reset' "$driver"; then
  fail 'individual environment resets do not remove inherited Compose map entries'
fi
[[ "$(grep -Fc 'entrypoint: ["/usr/local/bin/acr-db-init"]' "$compose_overlay")" -eq 2 ]] \
  || fail 'the ACR role and ACL jobs must replace inherited Compose entrypoints'
# prepare_stack must not perform client assertions of its own; start_happy owns those.
prepared="$(sed -n '/^prepare_stack()/,/^}/p' "$driver")"
printf '%s' "$prepared" | grep -Fq 'run_mcp' && fail 'prepare_stack must not run the driver smoke probes'

# --- isolation and teardown ---------------------------------------------------------------
grep -Fq 'acr-fs-[a-z0-9][a-z0-9-]{2,40}$' "$script" || fail 'isolated project naming is not enforced'
grep -Fq 'refusing the operator default Compose project' "$script" || fail 'operator project guard is missing'
grep -Fq 'assert_project_unused' "$script" || fail 'pre-existing project guard is missing'
grep -Fq 'assert_host_config_untouched' "$script" || fail 'host config isolation proof is missing'
grep -Fq 'assert_throwaway_home_was_used' "$script" || fail 'the positive half of the isolation proof is missing'
grep -Fq 'LC_ALL=C comm -13' "$script" || fail 'snapshot comparison must use the same C locale as client_state_lines'
# The operator data directory is routinely tens of GB; hashing it would outlast the run.
if grep -Eq 'find "\$path" -type f -exec shasum' "$script"; then
  fail 'the isolation proof must fingerprint metadata, never hash the operator installation'
fi
# GNU stat -f means "filesystem status" and succeeds while printing free-block counts, so a
# BSD-first probe silently yields an unstable digest on every Linux runner.
gnu_probe_line="$(grep -n "if stat -c '%n' \." "$script" | head -1 | cut -d: -f1)"
bsd_probe_line="$(grep -n "elif stat -f '%N' \." "$script" | head -1 | cut -d: -f1)"
[[ -n "$gnu_probe_line" && -n "$bsd_probe_line" ]] || fail 'the stat flavour probe is missing'
[[ "$gnu_probe_line" -lt "$bsd_probe_line" ]] || fail 'the stat flavour probe must try GNU -c before BSD -f' 
grep -Fq 'trap fullstack_cleanup EXIT' "$script" || fail 'cleanup trap is missing'
for signal in "trap 'exit 130' INT" "trap 'exit 143' TERM" "trap 'exit 129' HUP"; do
  grep -Fq "$signal" "$script" || fail "signal trap missing: $signal"
done
grep -Fq 'E2E_DEBUG' "$script" || fail 'debug retention flag is missing'
# The shared driver's cleanup takes the failing status from `$?` and exits with it; it is what
# decides both the run's exit code and whether service logs are dumped. Wrapping it without
# restoring that status turns every post-boot failure into a silent green run — the single
# worst defect this gate can have — so the handoff is pinned here.
grep -Fq '( exit "$status" )' "$script" || fail 'the cleanup wrapper must hand the real status to the shared driver'
grep -Eq '^\s*exit "\$status"' "$script" || fail 'the cleanup wrapper must exit with the failing status'
# `find … | head -1` returns 141 under pipefail when find is killed by SIGPIPE, aborting the
# run with no message at all.
if grep -Eq '^[^#]*find [^|]*\| *head' "$script"; then
  fail 'find piped into head aborts silently under pipefail; use -print -quit'
fi

# --- credential hygiene --------------------------------------------------------------------
grep -Fq 'a credential leaked into the OpenCode configuration file' "$script" || fail 'config credential scan is missing'
grep -Fq 'redact_log' "$script" || fail 'artifacts are not redacted'
# OpenCode does not pass its environment to a local MCP child, so the credential must reach
# the sidecar through ACR_API_TOKEN_FILE: a path in the config, never the secret itself.
jq -e '.mcp.acr.environment.ACR_API_TOKEN_FILE == "__ACR_TOKEN_FILE__"' "$template" >/dev/null \
  || fail 'the template must supply the credential through ACR_API_TOKEN_FILE'
if jq -e '.mcp.acr.environment | has("ACR_API_TOKEN")' "$template" >/dev/null 2>&1; then
  fail 'the OpenCode config template must not carry the ACR bearer credential inline'
fi
grep -Fq 'the ACR credential file must be mode 0600' "$script" \
  || fail 'the credential file permission check is missing'
if grep -Eq -- '--insecure|--accept-invalid-certificate|NODE_TLS_REJECT_UNAUTHORIZED' "$script"; then
  fail 'TLS verification must never be weakened'
fi
grep -Fq -- '--cacert "$STATE/pki/ca.crt"' "$script" || fail 'API calls must pin the run CA'

# --- read-only posture ---------------------------------------------------------------------
grep -Fq 'writeback was enabled by default' "$script" || fail 'the writeback assertion is missing'
grep -Fq '["context_for_task","source_evidence"]' "$script" || fail 'the read-only tool surface assertion is missing'
jq -e '.permission.webfetch == "deny" and .permission.bash == "deny" and .permission.edit == "deny"' "$template" >/dev/null \
  || fail 'the OpenCode client must deny edit, bash and webfetch'
jq -e '[.tools | to_entries[] | select(.value == true)] | length == 0' "$template" >/dev/null \
  || fail 'no built-in OpenCode tool may be enabled'
jq -e '.mcp.acr.command[0] == "__ACR_MCP_BIN__" and .mcp.acr.command[1] == "serve" and .mcp.acr.type == "local"' "$template" >/dev/null \
  || fail 'the template must register the host-local acr-mcp serve command'
jq -e '(.mcp | keys) == ["acr"]' "$template" >/dev/null || fail 'exactly one MCP server may be registered'
jq -e '.share == "disabled" and .autoupdate == false' "$template" >/dev/null || fail 'sharing and autoupdate must be off'
jq -e '(.provider | keys) == ["contextfabric"]' "$template" >/dev/null || fail 'exactly one provider may be configured'
jq -e '.provider.contextfabric.options.baseURL | startswith("http://127.0.0.1:")' "$template" >/dev/null \
  || fail 'the scripted model must be reachable only over loopback'
# The scripted model speaks chat/completions; OpenCode's built-in openai provider drives the
# Responses API instead, so the acceptance client must use the openai-compatible package and
# must pin it so a CI pre-warm can serve it from cache.
jq -e '.provider.contextfabric.npm == "__PROVIDER_NPM_SPEC__"' "$template" >/dev/null \
  || fail 'the template must take its provider package from the driver'
grep -Eq 'PROVIDER_NPM_SPEC:-@ai-sdk/openai-compatible@[0-9]+\.[0-9]+\.[0-9]+\}' "$script" \
  || fail 'the provider npm package must be pinned to an exact version'
grep -Fq 'npm_config_offline' "$script" || fail 'the client run must prefer the pre-warmed npm cache'
# `--model` takes provider/model KEYS from the config object, not display names. Naming a
# provider the config does not define fails inside OpenCode's own server with an opaque
# "Unexpected server error" and zero model requests, so the driver must derive the id from
# the rendered config instead of repeating it.
grep -Fq -- '--model "$OPENCODE_MODEL_ID"' "$script" \
  || fail 'the client model id must be derived from the rendered config, not hardcoded'
jq -e '
  (.model | split("/")) as $parts
  | (.provider[$parts[0]].models[$parts[1]] // empty) != null
' "$template" >/dev/null \
  || fail 'the template default model must resolve to a provider/model key it defines'

# --- hermetic client sandbox ----------------------------------------------------------------
for variable in HOME XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME; do
  grep -Fq "${variable}=\"\$CLIENT_HOME" "$script" || fail "client sandbox does not redirect ${variable}"
done
grep -Fq 'env -i' "$script" || fail 'the OpenCode process must start from a cleared environment'
grep -Fq -- '--pure' "$script" || fail 'OpenCode must run without external plugins'
grep -Fq 'OPENCODE_DISABLE_PROJECT_CONFIG=true' "$script" || fail 'project config discovery must be disabled'
grep -Fq 'OPENCODE_DISABLE_MODELS_FETCH=true' "$script" || fail 'model catalogue fetching must be disabled'
grep -Fq 'assert_pinned_opencode' "$script" || fail 'the pinned OpenCode version is not enforced'
# OpenCode does not self-terminate on a refused upstream or a retry loop; without an external
# deadline a CI job hangs until the runner's own 6-hour limit.
grep -Fq 'timeout --preserve-status' "$script" || fail 'opencode run must be wrapped in an external timeout'
grep -Fq 'hung rather than failing' "$script" || fail 'a hung client must be its own failure class'
# The service ceiling (internal/config defaultMaxItems=30) is stricter than the contract's
# 1..50 bound, so a request the JSON Schema accepts can still be rejected as invalid_request.
grep -Eq 'max_items:(30|[12][0-9]|[1-9])\b' "$script" \
  || fail 'the context request must stay within the service max_items ceiling of 30'

# --- prompt contract --------------------------------------------------------------------------
for token in '{{GOAL}}' '{{REPOSITORY_SLUG}}' '{{SCOPE_INSTRUCTION}}' '{{MIN_EVIDENCE_EXPANSIONS}}' '{{TASK_ID}}'; do
  grep -Fq "$token" "$prompt" || fail "task prompt is missing the ${token} placeholder"
done
grep -Fq 'context_fabric_agent_result.v1' "$prompt" || fail 'the prompt does not require the versioned result schema'
grep -Fq 'untrusted data' "$prompt" || fail 'the prompt does not label retrieved content untrusted'
grep -Fq 'Do not fetch any URL' "$prompt" || fail 'the prompt does not forbid URL fetching'

# --- fixture and oracle set --------------------------------------------------------------------
[[ -f "$fixtures/fixture-manifest.json" ]] || fail 'fixture manifest is missing'
[[ -f "$fixtures/tasks.json" ]] || fail 'fixture task set is missing'
[[ -f "$fixtures/schema/context_fabric_agent_result.v1.schema.json" ]] || fail 'agent result schema is missing'
jq -e . "$fixtures/fixture-manifest.json" >/dev/null || fail 'fixture manifest is not valid JSON'
jq -e '.schema_version == "fullstack_tasks.v1" and (.tasks | length >= 3)' "$fixtures/tasks.json" >/dev/null \
  || fail 'task set is not a valid fullstack_tasks.v1 document'
jq -e '[.tasks[] | select(.profiles | index("smoke"))] | length >= 2' "$fixtures/tasks.json" >/dev/null \
  || fail 'the PR smoke profile must cover at least a complete and a partial task'

seed_count=0
for seed in "$fixtures"/seed/clickhouse/*.sql; do
  [[ -f "$seed" ]] || continue
  seed_count=$((seed_count + 1))
  grep -Fq '__ORG_ID__' "$seed" || fail "${seed##*/} does not bind the isolated organization"
  # SQL comments may legitimately name the forbidden functions while documenting the rule.
  if sed -e 's/--.*$//' "$seed" | grep -Eq 'generateUUIDv4\(\)|now64\(|[^a-z_]now\(\)'; then
    fail "${seed##*/} uses a generated identity or wall-clock timestamp"
  fi
done
[[ "$seed_count" -gt 0 ]] || fail 'no seed statements were found'

oracle_count=0
while IFS= read -r oracle; do
  [[ -n "$oracle" ]] || continue
  oracle_count=$((oracle_count + 1))
  [[ -f "$fixtures/$oracle" ]] || fail "oracle ${oracle} is missing"
  jq -e '.schema_version == "fullstack_task_oracle.v1"' "$fixtures/$oracle" >/dev/null \
    || fail "oracle ${oracle} is not a fullstack_task_oracle.v1 document"
  # Evidence-ref IDs are opaque signed tokens, so an oracle must never pin one literally.
  jq -e '[.required_evidence[]?, .forbidden_evidence[]?] | all(has("entity_type") and has("entity_id"))' "$fixtures/$oracle" >/dev/null \
    || fail "oracle ${oracle} does not identify evidence by entity"
done < <(jq -r '.tasks[].oracle' "$fixtures/tasks.json")
[[ "$oracle_count" -ge 3 ]] || fail 'expected an oracle for every task'

jq -e '.["$schema"] and .properties.schema_version.const == "context_fabric_agent_result.v1"' \
  "$fixtures/schema/context_fabric_agent_result.v1.schema.json" >/dev/null \
  || fail 'the agent result schema does not pin its version'

# --- documentation must not mislabel the existing suites -----------------------------------------
grep -Fq 'fullstack-opencode-e2e' "$root/docs/fullstack-acceptance.md" || fail 'the documented command is missing'
grep -Fq 'None of the existing suites may be relabelled as this gate' "$root/docs/fullstack-acceptance.md" \
  || fail 'the documentation must distinguish this gate from the existing suites'

# --- seed vs. effective ClickHouse schema (offline, no Docker/ClickHouse required) ----------
# This ACR checkout has no sibling ops checkout by default; the full-stack CI job does. Skip
# cleanly when the migrations directory is absent -- only fail when it IS present and the seed
# is wrong, per the migrations owner. See tests/fullstack/assertrun's verify-seed-schema:
# replays CREATE/ALTER/DROP TABLE from the migration directory into an effective schema and
# checks every seed INSERT's table/columns/VALUES arity against it.
# The ops tree sits at a different depth for a plain checkout than for a git worktree
# (acr/worktrees/<name>), so try the plausible roots rather than assuming one.
migrations_dir=""
for candidate in \
  "${DEV_HEALTH_ROOT:-}/ops" \
  "$root/../ops" \
  "$root/../../ops" \
  "$root/../../../ops"; do
  [[ -n "${candidate#/ops}" ]] || continue
  if [[ -d "$candidate/src/dev_health_ops/migrations/clickhouse" ]]; then
    migrations_dir="$candidate/src/dev_health_ops/migrations/clickhouse"
    break
  fi
done
if [[ -n "$migrations_dir" ]]; then
  command -v go >/dev/null || fail 'go is required to run verify-seed-schema'
  go run "$root/tests/fullstack/assertrun" verify-seed-schema \
    --seed-dir "$fixtures/seed/clickhouse" \
    --migrations-dir "$migrations_dir" \
    || fail 'the fixture seed references a table/column that does not exist in the effective ClickHouse schema'
else
  printf '[fullstack-contract] skipping verify-seed-schema: no sibling ops checkout found\n' >&2
fi

# --- no artifact path may be built on the same `local` line as the parameter it uses --------
# `local a="$1" b="…$a…"` silently reads the CALLER's `a`, because `local` is a builtin and all
# of its arguments are expanded before it runs. Every honest task happened to call these helpers
# with a caller variable of the same name and value, so the paths matched by coincidence; the
# fault self-test passes "<task>-<fault>" and the mismatch made a faulted session overwrite the
# honest task's event stream, which the assertions then graded as if it were the honest run.
# A failure here means a path was folded back onto a `local` line — split it onto its own.
if grep -nE '^[[:space:]]*local [a-zA-Z_]+="\$[0-9]"[^#]*\$\{?[a-zA-Z_]+' "$script" >/dev/null; then
  grep -nE '^[[:space:]]*local [a-zA-Z_]+="\$[0-9]"[^#]*\$\{?[a-zA-Z_]+' "$script" >&2
  fail 'a local declaration derives a value from a parameter declared on the same line; `local` expands all arguments before assigning any of them, so that reads the caller variable'
fi

# --- the ops services must be pointed at the isolated ClickHouse database -------------------
# Every ops service's CLICKHOUSE_URI in the product compose file resolves
# ${CLICKHOUSE_DB:-default}. When the driver did not export it, the long-running `api` container
# read the literal `default` for its whole lifetime while all seeding went to the per-project
# database, so the web repository catalog answered 200 with an empty list and every direct
# ClickHouse probe still passed.
grep -Fq 'CLICKHOUSE_DB="$db"' "$driver" \
  || fail 'compose.sh no longer points the ops services at the isolated ClickHouse database'
grep -Fq 'the ops api service reads ClickHouse database' "$script" \
  || fail 'the suite no longer asserts the application reads the database it seeds'

grep -Fq 'ACR_DEVICE_VERIFICATION_URL' "$driver" \
  || fail 'the isolated ACR runtime must receive the concrete web device verification URL'
grep -Fq 'compose logs --no-color acr-pki-init clickhouse migrate acr-ops-tls acr-migrate acr-api acr-tls-proxy' "$driver" \
  || fail 'failed isolated runs must retain the TLS proxy diagnostics for device-login transport failures'
grep -Fq 'run_device_login_lifecycle' "$script" \
  || fail 'the live gate must exercise CLI login through web approval and lifecycle commands'
grep -Fq 'record_web_readiness_failure' "$script" \
  || fail 'web readiness failures must retain sanitized timestamped diagnostics'
grep -Fq 'device-login-browser.mjs' "$script" \
  || fail 'the live gate must drive the real approval page with Playwright'
grep -Fq 'ACR_API_TOKEN_KEYRING_SERVICE' "$script" \
  || fail 'the device login subprocess must explicitly isolate keyring selectors'
grep -Fq 'credential file must be mode 0600' "$script" \
  || fail 'the device login lifecycle must assert fallback-file permissions'
grep -Fq 'device-login-prompt.log' "$script" \
  || fail 'device-login prompt failures must retain a redacted diagnostic'
grep -Fq 'record_web_browser_failure' "$script" \
  || fail 'browser approval failures must retain the isolated web service diagnostics'
grep -Fq 'for (const width of [375, 768, 1280])' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'the live approval flow must retain all three connected screenshot widths'
grep -Fq 'await captureState("pending");' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'the live approval flow must retain pending connected screenshots'
grep -Fq 'await captureState("review");' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'the live approval flow must retain review connected screenshots'
grep -Fq 'await captureState("success");' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'the live approval flow must retain success connected screenshots'
grep -Fq 'device-login-network.json' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'the live approval flow must retain sanitized browser network evidence'
grep -Fq 'await page.waitForURL((url) => !url.pathname.startsWith("/auth/signin"));' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'the device driver must settle the sign-in redirect before opening the protected approval page'
grep -Fq 'await page.waitForLoadState("domcontentloaded");' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'the device driver must not wait for background traffic to become idle after sign-in'
grep -Fq '^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'the browser driver must accept every server-issued eight-character alphanumeric device code'
grep -Fq '[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}' "$script" \
  || fail 'the shell prompt parser must use the exact device-code alphabet'
grep -Fq 'stop_device_login_service' "$script" \
  || fail 'the full-stack cleanup must stop a pending device-login process'
grep -Fq 'User with email ${WEB_EMAIL} already exists' "$script" \
  || fail 'the browser bootstrap must tolerate only the expected duplicate fixture user'
grep -Fq 'device approval ${operation} failed:' "$root/scripts/e2e/device-login-browser.mjs" \
  || fail 'browser preview failures must retain their observed status and safe body'
[[ -f "$root/scripts/e2e/device-login-browser.mjs" ]] \
  || fail 'the device login Playwright driver is missing'

printf 'fullstack opencode contract checks passed\n'
