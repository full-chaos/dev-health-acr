#!/usr/bin/env bash
set -euo pipefail

# Full-stack Context Fabric acceptance (CHAOS-3065).
#
# Boots the isolated Dev Health product slice plus ACR through the shared Compose driver,
# replaces the synthetic corpus with the versioned deterministic projection under
# testdata/fullstack/v1, then drives a real headless OpenCode session through the host-local
# acr-mcp sidecar and validates every layer against the task oracle.
#
# Contract: docs/fullstack-acceptance.md

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck disable=SC1091
source "${SCRIPT_DIR}/compose.sh"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/opencode-runtime-fixture.sh"

# allow: SIZE_OK — one trap owns the stack, the client sandbox, and the artifact lifecycle.

WEB_ROOT=""
COMPOSE_FILE=""
OVERLAY_FILE=""
PROJECT=""
SCENARIO="smoke"
MODEL_BACKEND="scripted"
WEB_CHECK="auto"
RUN_ID=""
ARTIFACTS=""
FIXTURE_ROOT=""
CLIENT_HOME=""
MODEL_PORT=""
MODEL_PID=""
# Read from the rendered client config rather than written twice. `--model` takes
# `<providerKey>/<modelKey>` — the keys in the config object, NOT the display names — and
# naming a provider the config does not define makes OpenCode fail inside its own server with
# an opaque "Unexpected server error" before it ever issues a model request.
OPENCODE_MODEL_ID=""
WEB_PORT=""
WEB_EMAIL=""
WEB_PASSWORD=""
WEB_AUTH_SECRET=""
DEVICE_LOGIN_HOME=""
DEVICE_LOGIN_BIN=""
DEVICE_LOGIN_PID=""
FULLSTACK_REPO_SLUG="example-org/widget-service"
FULLSTACK_FOREIGN_SLUG="example-org/other-service"
FULLSTACK_FUTURE_SLUG="example-org/future-service"
FULLSTACK_CROSS_ORG_SLUG="foreign-org/secret-service"
FULLSTACK_CROSS_ORG_ID="00000000-0000-4000-8000-000000003239"
# The Explorer's licensing entitlement. Named once because the grant and the read-back
# assertion must never drift apart; see assert_acr_entitlement.
ACR_WEB_FEATURE_KEY="agent_context_runtime"
# The ops control-plane database, distinct from ACR's own $ACR_DB_NAME on the same server.
OPS_POSTGRES_DB="${OPS_POSTGRES_DB:-devhealth}"
OPENCODE_BIN="${OPENCODE_BIN:-opencode}"
OPENCODE_PINNED_VERSION="${OPENCODE_PINNED_VERSION:-1.18.4}"
OPENCODE_OBSERVED_VERSION=""
# The scripted model speaks the OpenAI chat/completions wire format. OpenCode's built-in
# openai provider drives the Responses API instead, so the acceptance client uses the pinned
# openai-compatible provider package. CI pre-warms it into the npm cache; see
# docs/fullstack-acceptance.md.
PROVIDER_NPM_SPEC="${PROVIDER_NPM_SPEC:-@ai-sdk/openai-compatible@3.0.14}"
OPENCODE_RUNTIME_FIXTURE="${OPENCODE_RUNTIME_FIXTURE:-}"
OPENCODE_RUNTIME_FIXTURE_SHA256=""
# OpenCode installs the provider adapter, and its own ~83MB @opencode-ai/{plugin,sdk}
# bootstrap, on first use. CI pre-warms both and then sets PROVIDER_NPM_OFFLINE=true so the
# graded run cannot reach the registry; locally the default stays false so a cold cache works.
HOST_CONFIG_DIGEST_BEFORE=""

usage() {
  printf 'usage: %s --web-root <dev-health-web> --compose <root-compose.yml> --overlay <acr.compose.yml> --project <acr-fs-name> [--scenario smoke|full|self-test] [--model scripted|ollama] [--web auto|on|off]\n' "$0" >&2
}

fs_die() { printf '[acr-fullstack] FAIL: %s\n' "$*" >&2; exit 1; }
fs_note() { printf '[acr-fullstack] %s\n' "$*" >&2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --web-root) WEB_ROOT="${2:-}"; shift 2 ;;
    --compose) COMPOSE_FILE="${2:-}"; shift 2 ;;
    --overlay) OVERLAY_FILE="${2:-}"; shift 2 ;;
    --project) PROJECT="${2:-}"; shift 2 ;;
    --scenario) SCENARIO="${2:-}"; shift 2 ;;
    --model) MODEL_BACKEND="${2:-}"; shift 2 ;;
    --web) WEB_CHECK="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

case "$SCENARIO" in smoke|full|self-test) ;; *) usage; exit 2 ;; esac
case "$MODEL_BACKEND" in scripted|ollama) ;; *) usage; exit 2 ;; esac
case "$WEB_CHECK" in auto|on|off) ;; *) usage; exit 2 ;; esac
[[ -f "$COMPOSE_FILE" && -f "$OVERLAY_FILE" ]] || { usage; exit 2; }
[[ "$PROJECT" =~ ^acr-fs-[a-z0-9][a-z0-9-]{2,40}$ ]] || fs_die 'project must be an isolated acr-fs-* name'
[[ "$PROJECT" != "dev-health" && "$PROJECT" != "default" ]] || fs_die 'refusing the operator default Compose project'
for tool in docker openssl curl git jq go python3; do command -v "$tool" >/dev/null || fs_die "$tool is required"; done
command -v "$OPENCODE_BIN" >/dev/null || fs_die 'opencode is required on PATH (set OPENCODE_BIN to override)'
if [[ "$WEB_CHECK" != 'off' ]]; then
  [[ -d "$WEB_ROOT" && -f "$WEB_ROOT/package.json" ]] || { usage; exit 2; }
fi

FIXTURE_ROOT="$REPO_ROOT/testdata/fullstack/v1"
[[ -d "$FIXTURE_ROOT" ]] || fs_die 'versioned full-stack fixture corpus is missing'

# The scoped ACR credential and the shared driver's built-in probe must target the fixture
# repository, not the historical synthetic one.
ACR_E2E_REPOSITORY_SCOPE="$FULLSTACK_REPO_SLUG"
ACR_E2E_SEED_HOOK=seed_fullstack_evidence

# One task expands every evidence reference the packet returned — a dozen or more reads —
# and a self-test run drives seven sessions against the same credential inside a couple of
# minutes. The service's production-shaped default of 60 requests/minute throttles that, and
# the symptom is badly misleading: acr-mcp's startup capabilities call gets a 429, the sidecar
# exits, OpenCode logs "server unavailable" at WARN and completes the session with no tools,
# which surfaces as "context_for_task was not offered by the client". Raised deliberately here
# because this gate is not a rate-limit test; scripts/e2e/svs.sh keeps the default and owns
# that boundary.
export ACR_E2E_REQUESTS_PER_MINUTE="${ACR_E2E_REQUESTS_PER_MINUTE:-600}"

trap fullstack_cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

export CONTAINER_ALLOW_DIRTY="${CONTAINER_ALLOW_DIRTY:-1}"

# ---------------------------------------------------------------------------
# lifecycle
# ---------------------------------------------------------------------------

stop_model_service() {
  [[ -n "$MODEL_PID" ]] || return 0
  if kill -0 "$MODEL_PID" 2>/dev/null; then
    kill -TERM "$MODEL_PID" 2>/dev/null || true
    wait "$MODEL_PID" 2>/dev/null || true
  fi
  MODEL_PID=""
}

stop_device_login_service() {
  [[ -n "$DEVICE_LOGIN_PID" ]] || return 0
  if kill -0 "$DEVICE_LOGIN_PID" 2>/dev/null; then
    kill -TERM "$DEVICE_LOGIN_PID" 2>/dev/null || true
    wait "$DEVICE_LOGIN_PID" 2>/dev/null || true
  fi
  DEVICE_LOGIN_PID=""
}

# fullstack_cleanup owns everything this run created: the scripted model process, the
# throwaway client HOME, and — through the shared driver — the Compose project. Artifacts
# are deliberately outside $STATE so they survive teardown.
fullstack_cleanup() {
  local status=$?
  trap '' INT TERM HUP
  stop_device_login_service
  stop_model_service
  assert_host_config_untouched || status=1
  cleanup_opencode_runtime_fixture "$CLIENT_HOME" || status=1
  if [[ -n "$CLIENT_HOME" && -d "$CLIENT_HOME" && "${E2E_DEBUG:-0}" != '1' ]]; then
    rm -rf "$CLIENT_HOME" || status=1
  fi
  if [[ "$status" -ne 0 && -n "$ARTIFACTS" ]]; then
    fs_note "artifacts retained: $ARTIFACTS"
  fi
  # The shared driver's cleanup reads the failing status from `$?` and exits with it — it
  # decides both whether to dump service logs and what the run's exit code is. Calling it
  # after any command of our own hands it that command's status instead, which silently turns
  # every post-boot failure into a green run with no diagnostics. This subshell restores the
  # real status as `$?` immediately before the call; the explicit exit is the backstop for the
  # paths where cleanup returns rather than exits. `set +e` is required — `set -e` is still in
  # force inside a trap, so a failing subshell would abort this handler before cleanup ran.
  set +e
  ( exit "$status" )
  cleanup
  exit "$status"
}

prepare_artifacts() {
  RUN_ID="$(python3 -c 'import uuid; print(uuid.uuid4().hex[:12])')"
  ARTIFACTS="$REPO_ROOT/.tmp/fullstack/$RUN_ID"
  mkdir -p "$ARTIFACTS/expanded-evidence" "$ARTIFACTS/logs" "$ARTIFACTS/playwright"
  CLIENT_HOME="$ARTIFACTS/client-home"
  mkdir -p "$CLIENT_HOME/config/opencode"
  chmod 700 "$CLIENT_HOME"
  OPENCODE_RUNTIME_FIXTURE_SHA256="$(stage_opencode_runtime_fixture "$OPENCODE_RUNTIME_FIXTURE" "$CLIENT_HOME/config/opencode")" \
    || fs_die 'OPENCODE_RUNTIME_FIXTURE staging failed'
}

# ---------------------------------------------------------------------------
# host-config isolation proof
# ---------------------------------------------------------------------------

# stat_metadata prints "name mtime size" for each path, on both BSD and GNU stat.
#
# The flavour must be probed with GNU's -c and never with BSD's -f: on GNU coreutils -f means
# "filesystem status", so `stat -f <fmt>` SUCCEEDS and prints free-block counts that change
# every second. Probing -f first therefore yields a digest that is unstable on every Linux
# runner, which reads as "the operator's installation was modified" on a clean run.
STAT_FLAVOUR=""
detect_stat_flavour() {
  [[ -z "$STAT_FLAVOUR" ]] || return 0
  if stat -c '%n' . >/dev/null 2>&1; then
    STAT_FLAVOUR=gnu
  elif stat -f '%N' . >/dev/null 2>&1; then
    STAT_FLAVOUR=bsd
  else
    fs_die 'neither GNU nor BSD stat is available'
  fi
}

stat_metadata() {
  detect_stat_flavour
  if [[ "$STAT_FLAVOUR" == gnu ]]; then
    stat -c '%n %Y %s' "$@" 2>/dev/null || true
  else
    stat -f '%N %m %z' "$@" 2>/dev/null || true
  fi
}

stat_tree() {
  local path="$1" depth="$2"
  detect_stat_flavour
  if [[ "$STAT_FLAVOUR" == gnu ]]; then
    find "$path" -maxdepth "$depth" -mindepth 1 -exec stat -c '%n %Y %s' {} + 2>/dev/null || true
  else
    find "$path" -maxdepth "$depth" -mindepth 1 -exec stat -f '%N %m %z' {} + 2>/dev/null || true
  fi
}

# host_config_digest fingerprints the operator's OpenCode installation using metadata only.
#
# It deliberately does NOT hash file contents. A real installation's data directory is
# routinely tens of gigabytes across hundreds of thousands of session files, so hashing it
# would take longer than the acceptance run itself. Bounding the walk to each root and its
# immediate children keeps the check instant while still catching what this suite could
# plausibly get wrong: writing a config, a credential, or a session into the operator's home
# instead of the throwaway one. Those all land as a new or modified top-level entry.
#
# The configuration root is fingerprinted two levels deep because it is small and is the
# thing the acceptance criteria actually name.
host_config_digest() {
  local path digest=''
  # Configuration roots: full metadata, two levels deep. These are small and must not move.
  for path in "$HOME/.config/opencode" "$HOME/.opencode"; do
    if [[ -e "$path" ]]; then
      digest+="$(stat_metadata "$path")"$'\n'
      digest+="$(stat_tree "$path" 2)"$'\n'
    else
      digest+="absent:$path"$'\n'
    fi
  done
  # Session data root: names only, no timestamps. The operator may legitimately be running
  # OpenCode for their own work while this suite runs, which churns mtimes inside here. What
  # must not happen is this suite creating a store of its own, and that shows up as a new
  # top-level entry.
  path="$HOME/.local/share/opencode"
  if [[ -e "$path" ]]; then
    digest+="$(find "$path" -maxdepth 1 -mindepth 1 -exec basename {} \; 2>/dev/null | LC_ALL=C sort)"$'\n'
  else
    digest+="absent:$path"$'\n'
  fi
  printf '%s' "$digest" | LC_ALL=C sort | shasum -a 256 | cut -d' ' -f1
}

record_host_config_digest() { HOST_CONFIG_DIGEST_BEFORE="$(host_config_digest)"; }

assert_host_config_untouched() {
  [[ -n "$HOST_CONFIG_DIGEST_BEFORE" ]] || return 0
  local after
  after="$(host_config_digest)"
  if [[ "$after" != "$HOST_CONFIG_DIGEST_BEFORE" ]]; then
    printf '[acr-fullstack] FAIL: the run modified the operator OpenCode installation\n' >&2
    return 1
  fi
  return 0
}

# assert_throwaway_home_was_used is the positive half of the isolation proof. "The operator's
# home did not change" is satisfied vacuously if the client never ran at all, so this checks
# the client actually wrote its state into the throwaway root instead.
# assert_client_tool_surface reads the tool list the client actually offered the model, as
# recorded by the scripted model itself. The config denies the mutating tools, but a config
# that fails to apply looks exactly like one that did from the outside; this checks the
# delivered surface rather than the declared one.
# Every recorded request is checked, not just the first: OpenCode's first call to the model is
# its own session-title generation, which carries no tools at all.
assert_client_tool_surface() {
  # `local a="$1" b="…$a…"` does NOT work: `local` is a builtin, so every argument is expanded
  # before it runs and `$a` still holds the CALLER's value. Every path derived from a parameter
  # is therefore declared on its own line. This was a live bug — see run_opencode_task.
  local task_id="$1" offending
  local dir="$ARTIFACTS/model/$task_id"
  compgen -G "$dir/model-request-*.json" >/dev/null \
    || fs_die "the scripted model recorded no request for ${task_id}"
  # Built-in tools are matched on the whole name; only the writeback tool is matched on a
  # namespaced suffix. Suffix-matching the built-ins would flag the sidecar's own
  # `acr_context_for_task`, which ends in "_task".
  offending="$(jq -rs '[.[].offered_tools[]
      | select(test("^(bash|edit|write|patch|webfetch|websearch|task|todowrite|todoread)$")
               or test("(^|[_.-])record_episode$"))]
    | unique | join(", ")' "$dir"/model-request-*.json)"
  [[ -z "$offending" ]] || fs_die "the client offered mutating or writeback tools for ${task_id}: ${offending}"
  jq -es '[.[].offered_tools[] | select(test("(^|_)context_for_task$"))] | length >= 1' \
    "$dir"/model-request-*.json >/dev/null \
    || fs_die "the client never offered context_for_task for ${task_id}"
}

# snapshot_client_state records the throwaway tree before a task runs, so the positive half of
# the isolation proof can require state written by THAT task.
snapshot_client_state() {
  client_state_lines > "$STATE/client-state-before"
}

# client_state_lines fingerprints the throwaway tree by path, mtime and size. Path alone is
# not enough: OpenCode reuses the same session-storage and snapshot paths for every session in
# a project, so a second task legitimately rewrites files rather than creating new ones.
client_state_lines() {
  detect_stat_flavour
  if [[ "$STAT_FLAVOUR" == gnu ]]; then
    find "$CLIENT_HOME/data" "$CLIENT_HOME/state" "$CLIENT_HOME/cache" -type f \
      -exec stat -c '%n %Y %s' {} + 2>/dev/null | LC_ALL=C sort
  else
    find "$CLIENT_HOME/data" "$CLIENT_HOME/state" "$CLIENT_HOME/cache" -type f \
      -exec stat -f '%N %m %z' {} + 2>/dev/null | LC_ALL=C sort
  fi
}

# assert_throwaway_home_was_used proves the session that just ran wrote into the throwaway
# root. It compares against the pre-task snapshot rather than asking whether the tree is
# non-empty: `assert_pinned_opencode` already initialises files there before any task, so a
# non-emptiness check would be satisfied by that earlier write even if the task itself had
# written into the operator's real home.
assert_throwaway_home_was_used() {
  local after written
  after="$STATE/client-state-after"
  # `find ... | head -1` cannot be used here: head exits after the first line, find dies of
  # SIGPIPE, and under `set -o pipefail` the assignment fails with 141 — aborting the run
  # silently, right after a successful client session. `-print -quit` stops find itself.
  client_state_lines > "$after"
  if [[ -f "$STATE/client-state-before" ]]; then
    written="$(LC_ALL=C comm -13 "$STATE/client-state-before" "$after" | sed -n '1s/ [0-9]* [0-9]*$//p' || true)"
  else
    written="$(find "$CLIENT_HOME/data" "$CLIENT_HOME/state" "$CLIENT_HOME/cache" \
      -type f -print -quit 2>/dev/null)"
  fi
  [[ -n "$written" ]] \
    || fs_die 'this task wrote no new state under the throwaway HOME; isolation is unproven'
  fs_note "client state confined to the throwaway HOME (${written#"$CLIENT_HOME"/})"
}

# ---------------------------------------------------------------------------
# deterministic fixture seeding (replaces the shared driver's synthetic corpus)
# ---------------------------------------------------------------------------

seed_fullstack_evidence() {
  local db org_id file rendered substitutions
  db="$(ops_clickhouse_database)"
  org_id="$(<"$STATE/org-id")"
  mkdir -p "$STATE/fullstack-seed"
  for file in "$FIXTURE_ROOT"/seed/clickhouse/*.sql; do
    [[ -f "$file" ]] || fs_die 'no full-stack seed statements found'
    substitutions="$(grep -c '__ORG_ID__' "$file" || true)"
    [[ "$substitutions" -gt 0 ]] || fs_die "seed file ${file##*/} declares no organization binding"
    rendered="$STATE/fullstack-seed/${file##*/}"
    sed -e "s/__ORG_ID__/${org_id}/g" -e "s/__DATABASE__/${db}/g" "$file" > "$rendered"
    if grep -q '__ORG_ID__\|__DATABASE__' "$rendered"; then
      fs_die "seed file ${file##*/} was not fully rendered"
    fi
    compose exec -T clickhouse clickhouse-client --user default --password ch --database "$db" --multiquery < "$rendered" >/dev/null \
      || fs_die "seed file ${file##*/} failed to apply"
  done
  assert_scoped_repository "$FULLSTACK_REPO_SLUG"
  assert_scoped_repository "$FULLSTACK_FOREIGN_SLUG"
}

# verify_fixture re-derives the corpus hashes and every row-count probe declared by the
# manifest, so a corpus edit that was never projected into the seed fails before the client
# runs rather than as an unexplained oracle mismatch.
# find_ops_migrations_dir echoes the ops ClickHouse migrations directory if a sibling ops
# checkout is present, or nothing if not. A git worktree checkout (acr/worktrees/<name>) sits
# one level deeper than a plain checkout, so several candidate depths are tried.
find_ops_migrations_dir() {
  local candidate
  for candidate in \
    "${DEV_HEALTH_ROOT:-}/ops" \
    "$REPO_ROOT/../ops" \
    "$REPO_ROOT/../../ops" \
    "$REPO_ROOT/../../../ops"; do
    [[ -n "${candidate#/ops}" ]] || continue
    if [[ -d "$candidate/src/dev_health_ops/migrations/clickhouse" ]]; then
      printf '%s' "$candidate/src/dev_health_ops/migrations/clickhouse"
      return 0
    fi
  done
  return 1
}

verify_fixture() {
  local db org_id migrations_dir migrations_dir_args=()
  db="$(ops_clickhouse_database)"
  org_id="$(<"$STATE/org-id")"
  # compose is a shell function, so the verifier cannot exec it; hand it the resolved argv.
  { compose_argv; printf '%s\0' exec -T clickhouse clickhouse-client --user default --password ch --database "$db" --query; } > "$STATE/probe-argv"
  # Best-effort only: disclose unattributable Python migration DDL in fixture-verification.json
  # too when a sibling ops checkout happens to be available; never required for the live run.
  if migrations_dir="$(find_ops_migrations_dir)"; then
    migrations_dir_args=(--migrations-dir "$migrations_dir")
  fi
  go run "$REPO_ROOT/tests/fullstack/assertrun" verify-fixture \
    --manifest "$FIXTURE_ROOT/fixture-manifest.json" \
    --corpus "$REPO_ROOT/testdata/evaluation/v1" \
    --seed-dir "$FIXTURE_ROOT/seed/clickhouse" \
    --org-id "$org_id" \
    --database "$db" \
    --out "$ARTIFACTS/fixture-verification.json" \
    --probe-command-file "$STATE/probe-argv" \
    "${migrations_dir_args[@]}" \
    || fs_die 'fixture verification failed'
}

# ---------------------------------------------------------------------------
# readiness receipt
# ---------------------------------------------------------------------------

record_service_readiness() {
  local services=(postgres clickhouse valkey api acr-api acr-tls-proxy)
  local service state health
  : > "$ARTIFACTS/.readiness.jsonl"
  for service in "${services[@]}"; do
    state="$(compose ps --format json "$service" 2>/dev/null | jq -r 'if type == "array" then .[0] else . end | .State // "absent"' 2>/dev/null || printf 'absent')"
    health="$(compose ps --format json "$service" 2>/dev/null | jq -r 'if type == "array" then .[0] else . end | .Health // ""' 2>/dev/null || printf '')"
    jq -cn --arg service "$service" --arg state "$state" --arg health "$health" \
      '{service:$service,state:$state,health:$health}' >> "$ARTIFACTS/.readiness.jsonl"
  done
  jq -s --arg project "$PROJECT" '{project:$project,services:.}' "$ARTIFACTS/.readiness.jsonl" > "$ARTIFACTS/service-readiness.json"
  rm -f "$ARTIFACTS/.readiness.jsonl"
  jq -e '[.services[] | select(.state != "running")] | length == 0' "$ARTIFACTS/service-readiness.json" >/dev/null \
    || fs_die 'required services were not running'
}

# ---------------------------------------------------------------------------
# run manifest
# ---------------------------------------------------------------------------

write_run_manifest() {
  local web_ref="${E2E_WEB_REF:-}" runtime_fixture_receipt
  runtime_fixture_receipt="$(opencode_runtime_fixture_receipt_json "$OPENCODE_RUNTIME_FIXTURE_SHA256")" \
    || fs_die 'OPENCODE_RUNTIME_FIXTURE receipt is invalid'
  jq -n \
    --arg run_id "$RUN_ID" \
    --arg project "$PROJECT" \
    --arg scenario "$SCENARIO" \
    --arg model "$MODEL_BACKEND" \
    --arg web "$WEB_CHECK" \
    --arg web_ref "$web_ref" \
    --argjson runtime_fixture_sha256 "$runtime_fixture_receipt" \
    --arg opencode_version "$OPENCODE_OBSERVED_VERSION" \
    --arg opencode_pinned "$OPENCODE_PINNED_VERSION" \
    --arg acr_image "$IMAGE" \
    --arg repo_sha "$(git -C "$REPO_ROOT" rev-parse HEAD)" \
    --arg fixture_version "$(jq -r '.fixture_version' "$FIXTURE_ROOT/fixture-manifest.json")" \
    --arg repository "$FULLSTACK_REPO_SLUG" \
    '{schema_version:"fullstack_run_manifest.v1",run_id:$run_id,project:$project,scenario:$scenario,model_backend:$model,web_check:$web,web_ref:$web_ref,opencode_runtime_fixture_sha256:$runtime_fixture_sha256,opencode:{observed_version:$opencode_version,pinned_version:$opencode_pinned},acr_image:$acr_image,repository_sha:$repo_sha,fixture_version:$fixture_version,repository:$repository,writeback:"disabled"}' \
    > "$ARTIFACTS/run.json"
}

# assert_pinned_opencode runs the client inside the throwaway root: even `--version`
# initialises OpenCode's data directory, which would otherwise trip the isolation proof.
opencode_sandboxed() {
  env -i \
    PATH="$PATH" \
    HOME="$CLIENT_HOME" \
    TMPDIR="${TMPDIR:-/tmp}" \
    XDG_CONFIG_HOME="$CLIENT_HOME/config" \
    XDG_DATA_HOME="$CLIENT_HOME/data" \
    XDG_CACHE_HOME="$CLIENT_HOME/cache" \
    XDG_STATE_HOME="$CLIENT_HOME/state" \
    OPENCODE_DISABLE_AUTOUPDATE=true \
    OPENCODE_DISABLE_MODELS_FETCH=true \
    "$OPENCODE_BIN" "$@"
}

assert_pinned_opencode() {
  OPENCODE_OBSERVED_VERSION="$(opencode_sandboxed --version 2>/dev/null | tr -d '\n')"
  [[ "$OPENCODE_OBSERVED_VERSION" == "$OPENCODE_PINNED_VERSION" ]] \
    || fs_die "OpenCode ${OPENCODE_PINNED_VERSION} is the release-test contract; found ${OPENCODE_OBSERVED_VERSION:-none}"
}

# ---------------------------------------------------------------------------
# throwaway OpenCode client sandbox
# ---------------------------------------------------------------------------

# render_client_sandbox builds a HOME, XDG root, workspace and config that exist only for
# this run. The ACR bearer credential is deliberately absent from the JSON: it reaches the
# sidecar through the inherited process environment, so no credential is written to disk.
render_client_sandbox() {
  local template="$REPO_ROOT/tests/fullstack/opencode/opencode.json.template"
  [[ -f "$template" ]] || fs_die 'OpenCode config template is missing'
  mkdir -p "$CLIENT_HOME/config/opencode" "$CLIENT_HOME/data" "$CLIENT_HOME/cache" "$CLIENT_HOME/state" "$CLIENT_HOME/workspace"
  chmod 700 "$CLIENT_HOME/config" "$CLIENT_HOME/data" "$CLIENT_HOME/cache" "$CLIENT_HOME/state"
  # A coherent Git workspace keeps the sidecar's auto-discovery from disagreeing with the
  # explicit scope every task sends.
  # Re-rendered before every task (the model port changes), so this must be idempotent:
  # `git remote add` on an existing remote exits non-zero and would abort the run at task two.
  git -C "$CLIENT_HOME/workspace" init --quiet --initial-branch=main
  git -C "$CLIENT_HOME/workspace" remote remove origin 2>/dev/null || true
  git -C "$CLIENT_HOME/workspace" remote add origin "https://github.com/${FULLSTACK_REPO_SLUG}.git"

  # OpenCode does not pass its own environment to a local MCP child, so the sidecar cannot
  # inherit ACR_API_TOKEN. The config therefore names ACR_API_TOKEN_FILE — the documented
  # credential source — and only the path travels through JSON; the token itself stays in a
  # 0600 file the driver already wrote.
  local token_file="$STATE/secrets/acr-rotated-token"
  [[ -f "$token_file" ]] || fs_die 'the rotated ACR credential file is missing'
  local mode
  detect_stat_flavour
  if [[ "$STAT_FLAVOUR" == gnu ]]; then
    mode="$(stat -c '%a' "$token_file")"
  else
    mode="$(stat -f '%Lp' "$token_file")"
  fi
  [[ "$mode" == '600' ]] || fs_die "the ACR credential file must be mode 0600, found ${mode}"

  sed \
    -e "s#__MODEL_PORT__#${MODEL_PORT}#g" \
    -e "s#__PROVIDER_NPM_SPEC__#${PROVIDER_NPM_SPEC}#g" \
    -e "s#__ACR_MCP_BIN__#${STATE}/acr-mcp#g" \
    -e "s#__ACR_API_URL__#https://localhost:${PORT}#g" \
    -e "s#__ACR_TOKEN_FILE__#${token_file}#g" \
    -e "s#__ACR_CA_BUNDLE__#${STATE}/pki/ca.crt#g" \
    -e "s#__ACR_SIDECAR_VERSION__#1.0.0#g" \
    "$template" > "$CLIENT_HOME/config/opencode/opencode.json"
  chmod 600 "$CLIENT_HOME/config/opencode/opencode.json"
  jq -e . "$CLIENT_HOME/config/opencode/opencode.json" >/dev/null || fs_die 'rendered OpenCode config is not valid JSON'
  if grep -q '__' "$CLIENT_HOME/config/opencode/opencode.json"; then
    fs_die 'the OpenCode config template was not fully rendered'
  fi
  if grep -Eq 'fcacr_|svc_acr_' "$CLIENT_HOME/config/opencode/opencode.json"; then
    fs_die 'a credential leaked into the OpenCode configuration file'
  fi

  OPENCODE_MODEL_ID="$(jq -r '.model // empty' "$CLIENT_HOME/config/opencode/opencode.json")"
  [[ "$OPENCODE_MODEL_ID" == */* ]] || fs_die 'client config must name a default model as <provider>/<model>'
  jq -e --arg id "$OPENCODE_MODEL_ID" '
    ($id | split("/")) as $parts
    | .provider[$parts[0]].models[$parts[1]] // empty
  ' "$CLIENT_HOME/config/opencode/opencode.json" >/dev/null \
    || fs_die "client config names model ${OPENCODE_MODEL_ID}, which its own provider block does not define"
}

render_task_prompt() {
  local task_id="$1" goal="$2" branch="$3" commit="$4" min_expansions="$5" scope_instruction
  local as_of
  as_of="$(fixture_as_of)"
  if [[ -n "$commit" ]]; then
    scope_instruction="   - \`scope.commit_sha\`: ${commit}"
  elif [[ -n "$branch" ]]; then
    scope_instruction="   - \`scope.branch\`: ${branch}"
  else
    scope_instruction="   - omit \`scope\` entirely"
  fi
  # The same instant the driver's independent request pins, so both surfaces ask the same
  # question of the same corpus.
  [[ -z "$as_of" ]] || scope_instruction+=$'\n'"   - \`scope.as_of\`: ${as_of}"
  python3 - "$REPO_ROOT/tests/fullstack/opencode/task-prompt.md" "$CLIENT_HOME/prompt-${task_id}.md" \
    "$goal" "$FULLSTACK_REPO_SLUG" "$scope_instruction" "$min_expansions" "$task_id" <<'PY'
import sys

template, out, goal, slug, scope, expansions, task_id = sys.argv[1:8]
body = open(template, encoding="utf-8").read()
for token, value in (
    ("{{GOAL}}", goal),
    ("{{REPOSITORY_SLUG}}", slug),
    ("{{SCOPE_INSTRUCTION}}", scope),
    ("{{MIN_EVIDENCE_EXPANSIONS}}", expansions),
    ("{{TASK_ID}}", task_id),
):
    body = body.replace(token, value)
if "{{" in body:
    raise SystemExit("task prompt was not fully rendered")
open(out, "w", encoding="utf-8").write(body)
PY
}

# ---------------------------------------------------------------------------
# deterministic model service
# ---------------------------------------------------------------------------

start_model_service() {
  local plan="$1" task_id="$2" port_file
  port_file="$CLIENT_HOME/model-port"
  rm -f "$port_file"
  go run "$REPO_ROOT/tests/fullstack/modeloracle" \
    --plan "$plan" \
    --port-file "$port_file" \
    --log-dir "$ARTIFACTS/model/$task_id" \
    >"$ARTIFACTS/logs/model-${task_id}.log" 2>&1 &
  MODEL_PID=$!
  local attempts=0
  until [[ -s "$port_file" ]]; do
    attempts=$((attempts + 1))
    if [[ "$attempts" -ge 120 ]] || ! kill -0 "$MODEL_PID" 2>/dev/null; then
      redact_log < "$ARTIFACTS/logs/model-${task_id}.log" >&2 || true
      fs_die 'deterministic model service failed to start'
    fi
    sleep 0.5
  done
  MODEL_PORT="$(<"$port_file")"
  until curl --fail --silent --noproxy '*' "http://127.0.0.1:${MODEL_PORT}/health" >/dev/null; do
    attempts=$((attempts + 1))
    [[ "$attempts" -lt 160 ]] || fs_die 'deterministic model service never became ready'
    sleep 0.5
  done
}

# ---------------------------------------------------------------------------
# headless OpenCode
# ---------------------------------------------------------------------------

run_opencode_task() {
  local task_id="$1" prompt_file="$2" status=0
  local opencode_args=()
  # Declared separately, not on the `local` line above: `local` expands all of its arguments
  # before assigning any of them, so `${task_id}` there resolves to the CALLER's variable of
  # the same name. Every honest task called this with the caller's own task_id, so the paths
  # matched by coincidence; the self-test calls it as "<task>-<fault>" and the session's events
  # silently overwrote the honest task's event stream, which the assertions then graded.
  local events="$ARTIFACTS/opencode-events-${task_id}.jsonl"
  set +e
  while IFS= read -r -d '' argument; do opencode_args+=("$argument"); done < <(
    opencode_task_argv "$task_id" "$CLIENT_HOME/workspace" "$OPENCODE_MODEL_ID" "${OPENCODE_LOG_LEVEL:-INFO}" "$(<"$prompt_file")"
  )
  timeout --preserve-status --signal=TERM --kill-after=15s "${OPENCODE_TIMEOUT:-300}" \
  env -i \
    PATH="$PATH" \
    HOME="$CLIENT_HOME" \
    TMPDIR="${TMPDIR:-/tmp}" \
    XDG_CONFIG_HOME="$CLIENT_HOME/config" \
    XDG_DATA_HOME="$CLIENT_HOME/data" \
    XDG_CACHE_HOME="$CLIENT_HOME/cache" \
    XDG_STATE_HOME="$CLIENT_HOME/state" \
    OPENCODE_CONFIG="$CLIENT_HOME/config/opencode/opencode.json" \
    OPENCODE_CONFIG_DIR="$CLIENT_HOME/config/opencode" \
    OPENCODE_DISABLE_DEFAULT_PLUGINS=true \
    OPENCODE_DISABLE_AUTOUPDATE=true \
    OPENCODE_DISABLE_MODELS_FETCH=true \
    OPENCODE_DISABLE_PROJECT_CONFIG=true \
    OPENCODE_DISABLE_CLAUDE_CODE=true \
    OPENCODE_DISABLE_EXTERNAL_SKILLS=true \
    OPENCODE_DISABLE_SHARE=true \
    npm_config_offline="${PROVIDER_NPM_OFFLINE:-false}" \
    npm_config_fund=false \
    npm_config_audit=false \
    "$OPENCODE_BIN" "${opencode_args[@]}" \
      >"$events" 2>"$ARTIFACTS/logs/opencode-${task_id}.stderr"
  status=$?
  set -e
  redact_log < "$events" > "${events}.redacted" && mv "${events}.redacted" "$events"
  redact_log < "$ARTIFACTS/logs/opencode-${task_id}.stderr" > "$ARTIFACTS/logs/opencode-${task_id}.log"
  rm -f "$ARTIFACTS/logs/opencode-${task_id}.stderr"
  # OpenCode exits 0 on success and 1 on a hard provider error, but it does NOT exit at all
  # when the upstream refuses a connection or it enters a retry loop — it simply hangs. The
  # external deadline above is therefore load-bearing, and a timeout is reported as its own
  # failure class rather than being mistaken for an ordinary non-zero exit.
  if [[ "$status" -eq 124 || "$status" -eq 143 || "$status" -eq 137 ]]; then
    tail -n 60 "$ARTIFACTS/logs/opencode-${task_id}.log" >&2 || true
    fs_die "OpenCode did not return within ${OPENCODE_TIMEOUT:-300}s for ${task_id}; it hung rather than failing"
  fi
  if [[ "$status" -ne 0 ]]; then
    tail -n 60 "$ARTIFACTS/logs/opencode-${task_id}.log" >&2 || true
    fs_die "OpenCode exited ${status} for ${task_id}"
  fi
  [[ -s "$events" ]] || fs_die "OpenCode produced no events for ${task_id}"
}

# extract_agent_result takes the LAST text part. A strict-JSON task that emits more than one
# non-empty text part is a failure, not something to silently pick from.
extract_agent_result() {
  local task_id="$1" texts
  local events="$ARTIFACTS/opencode-events-${task_id}.jsonl"
  texts="$(jq -rs '[.[] | select(.type == "text") | .part.text | select(. != null and . != "")] | length' "$events")"
  [[ "$texts" == "1" ]] || fs_die "expected exactly one final text part for ${task_id}, found ${texts}"
  jq -rs '[.[] | select(.type == "text") | .part.text][-1]' "$events" > "$ARTIFACTS/agent-result-${task_id}.json"
  jq -e . "$ARTIFACTS/agent-result-${task_id}.json" >/dev/null \
    || fs_die "the final OpenCode response for ${task_id} was not a JSON document"
  # The scripted model refuses rather than inventing an answer when the client did not offer
  # it the Context Fabric tools. OpenCode treats a dead MCP server as a WARN and completes the
  # session anyway, so without this the run fails much later as an opaque schema mismatch.
  local failure
  failure="$(jq -r 'select(.schema_version == "fullstack_model_failure.v1") | .reason // "unspecified"' \
    "$ARTIFACTS/agent-result-${task_id}.json")"
  if [[ -n "$failure" ]]; then
    fs_die "the scripted model refused to answer ${task_id}: ${failure}$(sidecar_failure_hint "$task_id")"
  fi
}

# sidecar_failure_hint asks the API why the sidecar could not start. OpenCode reports a dead
# MCP server only as "server unavailable" at WARN, so without this the operator sees a missing
# tool and has no way to tell a broken build from a throttled or expired credential.
sidecar_failure_hint() {
  local status
  status="$(curl --silent --output /dev/null --write-out '%{http_code}' \
    --cacert "$STATE/pki/ca.crt" --noproxy '*' \
    -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer $(<"$STATE/secrets/acr-rotated-token")" \
    "https://localhost:${PORT}/api/v1/agent-context/capabilities" 2>/dev/null || true)"
  case "$status" in
    200) printf ' (the API answers 200, so the sidecar itself failed to start — see logs/opencode-%s.log)' "$1" ;;
    429) printf ' (the API is rate limiting this credential: raise ACR_E2E_REQUESTS_PER_MINUTE)' ;;
    401|403) printf ' (the API rejected the credential with HTTP %s)' "$status" ;;
    *) printf ' (the capabilities probe returned HTTP %s)' "${status:-none}" ;;
  esac
}

# ---------------------------------------------------------------------------
# direct API and MCP surfaces (captured alongside the client run for agreement checks)
# ---------------------------------------------------------------------------

acr_curl() {
  curl --fail --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
    -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer $(<"$STATE/secrets/acr-rotated-token")" "$@"
}

capture_capabilities() {
  acr_curl "https://localhost:${PORT}/api/v1/agent-context/capabilities" > "$ARTIFACTS/capabilities.json"
  jq -e '.permissions.episode_write == false and ([.enabled_tools[]] | index("record_episode") | not)' \
    "$ARTIFACTS/capabilities.json" >/dev/null || fs_die 'writeback was enabled by default'
}

# capture_mcp_tools proves the read-only surface directly from the sidecar. OpenCode's own
# view is asserted separately from its event stream; both must agree.
capture_mcp_tools() {
  local response mcp_pid mcp_input mcp_output
  coproc FS_MCP { ACR_API_URL="https://localhost:${PORT}" ACR_API_TOKEN="$(<"$STATE/secrets/acr-rotated-token")" ACR_API_CA_BUNDLE="$STATE/pki/ca.crt" ACR_SIDECAR_VERSION=1.0.0 ACR_SIDECAR_CLIENT_VERSION=1.0.0 "$STATE/acr-mcp" serve; }
  mcp_pid="$FS_MCP_PID"
  mcp_input="${FS_MCP[1]}"
  mcp_output="${FS_MCP[0]}"
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"acr-fullstack","version":"1.0.0"}}}' >&"$mcp_input"
  if ! IFS= read -r -t 30 response <&"$mcp_output"; then
    mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
    fs_die 'MCP initialize timed out'
  fi
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' >&"$mcp_input"
  if ! IFS= read -r -t 30 response <&"$mcp_output"; then
    mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
    fs_die 'MCP tools/list timed out'
  fi
  printf '%s\n' "$response" > "$ARTIFACTS/mcp-tools.json"
  mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
  jq -e '([.result.tools[].name] | sort) == ["context_for_task","source_evidence"]' "$ARTIFACTS/mcp-tools.json" >/dev/null \
    || fs_die 'the sidecar exposed a tool surface other than the read-only pair'
}

# The fixture pins as_of so the January 2026 corpus timestamps survive wall-clock drift and
# any future server-side default for the source catalogue's time filters.
fixture_as_of() { jq -r '.as_of_pin.value // empty' "$FIXTURE_ROOT/fixture-manifest.json"; }

write_context_request() {
  local task_id="$1" branch="$2" commit="$3"
  jq -n \
    --arg goal "$(task_field "$task_id" '.goal')" \
    --arg slug "$FULLSTACK_REPO_SLUG" \
    --arg branch "$branch" \
    --arg commit "$commit" \
    --arg as_of "$(fixture_as_of)" \
    --arg request_id "fs-${RUN_ID}-${task_id}" \
    '{schema_version:"context_packet_request.v1",request_id:$request_id,goal:$goal,repository:{slug:$slug},
      scope:({} + (if $branch == "" then {} else {branch:$branch} end)
                + (if $commit == "" then {} else {commit_sha:$commit} end)
                + (if $as_of == "" then {} else {as_of:$as_of} end)),
      options:{max_items:30,max_output_tokens:4000,max_serialized_bytes:262144,include_debug:false,include_low_confidence:true},
      client:{name:"acr-fullstack",version:"1.0.0",sidecar_version:"1.0.0"}}' \
    > "$ARTIFACTS/context-request-${task_id}.json"
}

task_field() { jq -r --arg id "$1" '.tasks[] | select(.task_id == $id) | '"$2" "$FIXTURE_ROOT/tasks.json"; }

# capture_api_packet keeps the response body even when the request is rejected, so a failure
# reports ACR's typed error receipt rather than a bare curl exit code.
capture_api_packet() {
  local task_id="$1" status
  local body="$ARTIFACTS/context-packet-${task_id}.json"
  status="$(curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
    -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer $(<"$STATE/secrets/acr-rotated-token")" \
    -H 'Content-Type: application/json' \
    --data-binary @"$ARTIFACTS/context-request-${task_id}.json" \
    --output "$body" --write-out '%{http_code}' \
    "https://localhost:${PORT}/api/v1/agent-context/context-packets")"
  if [[ "$status" != '200' ]]; then
    fs_note "packet request for ${task_id} returned HTTP ${status}: $(jq -c '{code: .error.code, message: .error.message, details: .error.details}' "$body" 2>/dev/null | redact_log || redact_log < "$body")"
    fs_die "context packet request failed for ${task_id}"
  fi
  jq -e '.schema_version == "context_packet.v1"' "$body" >/dev/null \
    || fs_die "API packet contract mismatch for ${task_id}"
}

capture_expanded_evidence() {
  local task_id="$1" index=0 ref
  mkdir -p "$ARTIFACTS/expanded-evidence/$task_id"
  while IFS= read -r ref; do
    [[ -n "$ref" ]] || continue
    index=$((index + 1))
    acr_curl "https://localhost:${PORT}/api/v1/agent-context/evidence/${ref}" \
      > "$ARTIFACTS/expanded-evidence/${task_id}/${index}.json" \
      || fs_die "evidence expansion failed for ${task_id}"
  done < <(jq -r '[.items[].evidence_ref_ids[]] | unique | .[]' "$ARTIFACTS/context-packet-${task_id}.json")
}

# ---------------------------------------------------------------------------
# negative tasks (authorization and evidence boundaries)
# ---------------------------------------------------------------------------

expect_task_http_error() {
  local task_id="$1" expected_status="$2" expected_code="$3" body http_status curl_status
  shift 3
  body="$ARTIFACTS/negative-${task_id}.json"
  set +e
  http_status="$("$@" --output "$body" --write-out '%{http_code}')"
  curl_status=$?
  set -e
  [[ "$curl_status" -eq 0 ]] || fs_die "${task_id}: expected an HTTP response, curl exited ${curl_status}"
  [[ "$http_status" == "$expected_status" ]] || fs_die "${task_id}: expected HTTP ${expected_status}, got ${http_status}"
  "$REPO_ROOT/scripts/e2e/validate-error-receipt.sh" "$expected_status" "$expected_code" "$http_status" "$body" \
    || fs_die "${task_id}: expected a typed ${expected_code} error receipt"
  fs_note "${task_id}: HTTP ${expected_status} ${expected_code} enforced before any packet assembly"
}

run_foreign_repo_task() {
  local task_id='task-004-foreign-repo-denied'
  jq --arg slug "$FULLSTACK_FOREIGN_SLUG" '.repository.slug = $slug' \
    "$ARTIFACTS/context-request-task-001-checkout-flake-exact-commit.json" \
    > "$ARTIFACTS/context-request-${task_id}.json"
  expect_task_http_error "$task_id" 403 repo_forbidden \
    curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
      -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer $(<"$STATE/secrets/acr-rotated-token")" \
      -H 'Content-Type: application/json' --data-binary @"$ARTIFACTS/context-request-${task_id}.json" \
      "https://localhost:${PORT}/api/v1/agent-context/context-packets"
}

run_unavailable_evidence_task() {
  local task_id='task-005-unavailable-evidence' forged
  forged="$(task_field "$task_id" '.forged_evidence_ref_id')"
  [[ -n "$forged" && "$forged" != 'null' ]] || fs_die "${task_id}: the fixture declares no forged evidence reference"
  expect_task_http_error "$task_id" 404 not_found \
    curl --silent --show-error --cacert "$STATE/pki/ca.crt" --noproxy '*' \
      -H "$ACR_CLIENT_VERSION_HEADER" -H "Authorization: Bearer $(<"$STATE/secrets/acr-rotated-token")" \
      "https://localhost:${PORT}/api/v1/agent-context/evidence/${forged}"
}

# ---------------------------------------------------------------------------
# per-task client run
# ---------------------------------------------------------------------------

write_model_plan() {
  local task_id="$1" branch="$2" commit="$3"
  local plan="$CLIENT_HOME/plan-${task_id}.json"
  jq -n \
    --arg task_id "$task_id" \
    --arg goal "$(task_field "$task_id" '.goal')" \
    --arg slug "$FULLSTACK_REPO_SLUG" \
    --arg branch "$branch" \
    --arg commit "$commit" \
    --arg fault "${E2E_FAULT:-}" \
    --argjson expansions "$(jq -r '.min_expandable_evidence // 2' "$FIXTURE_ROOT/$(task_field "$task_id" '.oracle')")" \
    --argjson findings "$(jq -c '[.required_findings[]? | {claim_id, claim_kind, summary: ("evidence-backed claim for " + .claim_id), evidence_selector: ("entity:" + .must_cite_entity.entity_type + "/" + .must_cite_entity.entity_id)}]' "$FIXTURE_ROOT/$(task_field "$task_id" '.oracle')")" \
    --argjson checks "$(jq -c '[.required_checks[]? | {check_id: ., label: ("Verify " + .), reason: "required by the task oracle and supported by returned evidence"}]' "$FIXTURE_ROOT/$(task_field "$task_id" '.oracle')")" \
    --arg as_of "$(fixture_as_of)" \
    '{schema_version:"fullstack_model_plan.v1",task_id:$task_id,goal:$goal,repository_slug:$slug,
      scope:({} + (if $branch == "" then {} else {branch:$branch} end)
                + (if $commit == "" then {} else {commit_sha:$commit} end)
                + (if $as_of == "" then {} else {as_of:$as_of} end)),
      min_evidence_expansions:$expansions,findings:$findings,recommended_checks:$checks}
     + (if $fault == "" then {} else {fault:$fault} end)' \
    > "$plan"
  printf '%s' "$plan"
}

run_client_task() {
  local task_id="$1" branch commit plan
  branch="$(task_field "$task_id" '.scope.branch // ""')"
  commit="$(task_field "$task_id" '.scope.commit_sha // ""')"
  fs_note "running ${task_id}"

  write_context_request "$task_id" "$branch" "$commit"
  capture_api_packet "$task_id"
  capture_expanded_evidence "$task_id"

  plan="$(write_model_plan "$task_id" "$branch" "$commit")"
  start_model_service "$plan" "$task_id"
  render_client_sandbox
  render_task_prompt "$task_id" "$(task_field "$task_id" '.goal')" "$branch" "$commit" \
    "$(jq -r '.min_expandable_evidence // 2' "$FIXTURE_ROOT/$(task_field "$task_id" '.oracle')")"
  snapshot_client_state
  run_opencode_task "$task_id" "$CLIENT_HOME/prompt-${task_id}.md"
  assert_throwaway_home_was_used
  assert_client_tool_surface "$task_id"
  extract_agent_result "$task_id"
  stop_model_service

  assert_task "$task_id"
}

assert_task() {
  local task_id="$1"
  go run "$REPO_ROOT/tests/fullstack/assertrun" assert-run \
    --task "$task_id" \
    --oracle "$FIXTURE_ROOT/$(task_field "$task_id" '.oracle')" \
    --artifacts "$ARTIFACTS" \
    --result-schema "$FIXTURE_ROOT/schema/context_fabric_agent_result.v1.schema.json" \
    --packet-schema-dir "$REPO_ROOT/contracts/jsonschema/v1" \
    --fixture-manifest "$FIXTURE_ROOT/fixture-manifest.json" \
    --junit "$ARTIFACTS/junit-${task_id}.xml" \
    --report "$ARTIFACTS/assertion-report-${task_id}.json" \
    || fs_die "assertions failed for ${task_id}"
}

# PROFILE_MINIMUM_TASKS is the number of tasks each profile must run. A gate that silently
# runs zero tasks and reports success is the worst failure this suite can have, and it is one
# fixture edit away: drop the profile tag from every task and the selection is simply empty.
profile_minimum_tasks() {
  case "$1" in
    smoke) printf '2' ;;
    full) printf '5' ;;
    *) fs_die "no task-count floor is defined for profile ${1}" ;;
  esac
}

# selected_tasks fails rather than returning nothing. It is deliberately not called from a
# `for task in $(selected_tasks)` word list: a command substitution there cannot fail the run,
# because `set -e` does not apply to the words of a for loop.
selected_tasks() {
  local profile="$SCENARIO" tasks minimum
  [[ "$profile" != 'self-test' ]] || profile=smoke
  minimum="$(profile_minimum_tasks "$profile")"
  tasks="$(jq -er --arg profile "$profile" \
    '[.tasks[] | select(.profiles | index($profile)) | .task_id] | join("\n")' \
    "$FIXTURE_ROOT/tasks.json")" || fs_die "could not read the ${profile} task set from tasks.json"
  local count=0
  [[ -z "$tasks" ]] || count="$(printf '%s\n' "$tasks" | grep -c .)"
  [[ "$count" -ge "$minimum" ]] \
    || fs_die "profile ${profile} selected ${count} task(s); it must run at least ${minimum}"
  printf '%s\n' "$tasks"
}

# ---------------------------------------------------------------------------
# harness self-test: prove the assertions actually reject bad agent behaviour
# ---------------------------------------------------------------------------

# A suite that only ever sees a well-behaved scripted model proves nothing about its own
# ability to catch a misbehaving one. This replays suitable tasks through the deterministic
# model with a deliberate fault injected and requires the assertion layers to FAIL. A fault
# that slips through is a hole in the gate, so it is reported as a failure of this run.
run_fault_self_test() {
  local task_id fault branch commit plan expected_check spec
  # Each fault is replayed against a task whose live packet makes that fault meaningful, and
  # the rejection must come from the check that fault targets. Status inflation and fabricated
  # findings use task-003 because its live packet is partial while its oracle requires no
  # branch-specific findings. Replaying either against complete task-001 would be a no-op.
  #
  #   fault | task replayed | check that must fail
  # Check names are the exact ones the assertion tool emits, because assert_rejected_for now
  # matches whole names. `packet_status` and `source_evidence` used to appear here and were
  # only ever satisfied as substrings of the real checks below.
  for spec in \
    'invent-evidence|task-001-checkout-flake-exact-commit|no_invented_evidence_ids' \
    'inflate-status|task-003-unindexed-branch-empty|agent_result_packet_status_matches_live_packet' \
    'fabricate-findings|task-003-unindexed-branch-empty|findings_must_be_empty' \
    'skip-evidence|task-001-checkout-flake-exact-commit|source_evidence_meets_expansion_floor' \
    'wrong-scope|task-001-checkout-flake-exact-commit|agent_result_scope_resolution_matches_live_packet' \
    'unsupported-claim|task-001-checkout-flake-exact-commit|observed_finding_has_citation' \
    'downgrade-claim-kind|task-001-checkout-flake-exact-commit|required_finding_claim_kind_matches'; do
    fault="${spec%%|*}"
    task_id="${spec#*|}"
    expected_check="${task_id#*|}"
    task_id="${task_id%%|*}"
    branch="$(task_field "$task_id" '.scope.branch // ""')"
    commit="$(task_field "$task_id" '.scope.commit_sha // ""')"
    fs_note "self-test: injecting ${fault} against ${task_id}"
    plan="$(E2E_FAULT="$fault" write_model_plan "$task_id" "$branch" "$commit")"
    start_model_service "$plan" "${task_id}-${fault}"
    render_client_sandbox
    render_task_prompt "$task_id" "$(task_field "$task_id" '.goal')" "$branch" "$commit" \
      "$(jq -r '.min_expandable_evidence // 2' "$FIXTURE_ROOT/$(task_field "$task_id" '.oracle')")"
    snapshot_client_state
    run_opencode_task "${task_id}-${fault}" "$CLIENT_HOME/prompt-${task_id}.md"
    extract_agent_result "${task_id}-${fault}"
    stop_model_service
    # The faulted result is asserted under the task's own oracle, so reuse its artifacts.
    # Expansion directories are copied when present; no-evidence packets may legitimately omit one.
    cp "$ARTIFACTS/context-packet-${task_id}.json" "$ARTIFACTS/context-packet-${task_id}-${fault}.json"
    rm -rf "$ARTIFACTS/expanded-evidence/${task_id}-${fault}"
    if [[ -d "$ARTIFACTS/expanded-evidence/${task_id}" ]]; then
      cp -R "$ARTIFACTS/expanded-evidence/${task_id}" "$ARTIFACTS/expanded-evidence/${task_id}-${fault}"
    else
      mkdir -p "$ARTIFACTS/expanded-evidence/${task_id}-${fault}"
    fi
    # The faulted session's own event stream is what proves the fault was exercised through
    # the real client. If it is not on disk at grading time the rejection below would be
    # meaningless, so this is checked rather than assumed.
    if [[ ! -s "$ARTIFACTS/opencode-events-${task_id}-${fault}.jsonl" ]]; then
      ls -la "$ARTIFACTS" >&2 || true
      fs_die "self-test: the ${fault} session left no event stream to grade"
    fi
    if assert_task_quiet "${task_id}-${fault}" "$task_id"; then
      fs_die "self-test: the assertions accepted an agent that used ${fault}; the gate is not enforcing its own contract"
    fi
    assert_rejected_for "${task_id}-${fault}" "$fault" "$expected_check"
    fs_note "self-test: ${fault} was correctly rejected by ${expected_check}"
  done
}

# assert_rejected_for requires the fault to be caught by the check that fault targets. A
# rejection for some unrelated reason looks identical from the exit status alone, and would
# leave the targeted check silently unproven — which is exactly what a self-test exists to
# rule out.
assert_rejected_for() {
  local artifact_task="$1" fault="$2" expected_check="$3" report failing
  report="$ARTIFACTS/assertion-report-selftest-${artifact_task}.json"
  [[ -f "$report" ]] || fs_die "self-test: ${fault} produced no assertion report"
  failing="$(jq -r '[.layers[].checks[] | select(.ok == false) | .name] | join(",")' "$report")"
  [[ -n "$failing" ]] || fs_die "self-test: ${fault} was rejected but no individual check failed"
  # Matched on the whole check name, never as a substring. Several checks are named as
  # extensions of others — `packet_status` is a substring of
  # `agent_result_packet_status_matches_live_packet` — so a substring match credits a fault to
  # a check that did not actually catch it, which is the same "passed for an unrelated reason"
  # failure this self-test exists to rule out. Parameterized checks carry an `[id]` suffix, so
  # the name is compared up to that bracket.
  jq -e --arg want "$expected_check" \
    '[.layers[].checks[] | select(.ok == false) | .name | sub("\\[.*$"; "")] | index($want)' \
    "$report" >/dev/null \
    || fs_die "self-test: ${fault} was rejected by [${failing}], not by the ${expected_check} check it targets"
}

# assert_task_quiet runs the assertion tool for an alternate artifact prefix without treating
# a non-zero exit as fatal; the caller decides what the exit code means.
assert_task_quiet() {
  local artifact_task="$1" oracle_task="$2"
  go run "$REPO_ROOT/tests/fullstack/assertrun" assert-run \
    --task "$artifact_task" \
    --logical-task "$oracle_task" \
    --oracle "$FIXTURE_ROOT/$(task_field "$oracle_task" '.oracle')" \
    --artifacts "$ARTIFACTS" \
    --result-schema "$FIXTURE_ROOT/schema/context_fabric_agent_result.v1.schema.json" \
    --packet-schema-dir "$REPO_ROOT/contracts/jsonschema/v1" \
    --fixture-manifest "$FIXTURE_ROOT/fixture-manifest.json" \
    --junit "$ARTIFACTS/junit-selftest-${artifact_task}.xml" \
    --report "$ARTIFACTS/assertion-report-selftest-${artifact_task}.json" \
    >"$ARTIFACTS/logs/selftest-${artifact_task}.log" 2>&1
}

web_check_enabled() {
  [[ "$WEB_CHECK" == 'on' || ( "$WEB_CHECK" == 'auto' && "$SCENARIO" == 'full' ) ]]
}

# ---------------------------------------------------------------------------
# live web agreement (Context Packet Explorer against the same live services)
# ---------------------------------------------------------------------------

write_web_assertion_material() {
  local key jwks public
  mkdir -p "$STATE/web"
  key="$STATE/web/web-assertion.key"
  jwks="$STATE/web/web-assertions.jwks.json"
  openssl genpkey -algorithm ED25519 -out "$key"
  # This private key is bind-mounted read-only into web-fullstack, whose Dockerfile has no
  # USER directive and so runs as root -- unlike acr-api's distroless nonroot UID 65532, no
  # ownership mismatch applies here, and dev-health-web's own readPrivateKey()
  # (src/lib/acr/config.ts) explicitly REJECTS any group/other permission bits
  # ((mode & 0o077) !== 0) as "Agent Context Runtime is not configured." Mode 600, not 644,
  # for this file specifically -- confirmed live: leaving it 644 makes device approval fail
  # 503 "configuration" on a real Linux Docker host, since Docker Desktop's macOS file
  # sharing does not surface that mode-bit rejection the same way a native bind mount does.
  chmod 600 "$key"
  public="$(openssl pkey -in "$key" -pubout -outform DER | python3 -c 'import base64,sys; print(base64.urlsafe_b64encode(sys.stdin.buffer.read()[-32:]).rstrip(b"=").decode())')"
  printf '{"keys":[{"kty":"OKP","crv":"Ed25519","kid":"acr-fullstack-web","use":"sig","alg":"EdDSA","x":"%s"}]}\n' "$public" > "$jwks"
  # This one stays 644: acr-api reads it fine at that mode (confirmed live,
  # web_assertions_configured=true), and it is public key material by definition.
  chmod 644 "$jwks"
}

# The web overlay lands in svs.override.yml because the shared compose() helper already
# includes that file when present; adding a second override path would change the driver's
# rendering contract for every suite.
render_web_override() {
  WEB_PORT="$(free_port)"
  WEB_EMAIL="admin@test.com"
  WEB_PASSWORD="default1234"
  WEB_AUTH_SECRET="$(random_secret)"
  cat > "$STATE/svs.override.yml" <<EOF
services:
  acr-api:
    environment:
      ACR_WEB_ASSERTION_ISSUER: dev-health-web
      ACR_WEB_ASSERTION_AUDIENCE: dev-health-acr
      ACR_WEB_ASSERTION_JWKS_FILE: /run/acr-e2e/web-assertions.jwks.json
    volumes:
      - ${STATE}/web/web-assertions.jwks.json:/run/acr-e2e/web-assertions.jwks.json:ro
  bugsink:
    container_name: ${PROJECT}-bugsink
    ports: []
    restart: "no"
  web-fullstack:
    build:
      context: ${WEB_ROOT}
      dockerfile: Dockerfile
      target: runner
    ports: ["127.0.0.1:${WEB_PORT}:3000"]
    environment:
      AUTH_SECRET: ${WEB_AUTH_SECRET}
      AUTH_URL: http://127.0.0.1:${WEB_PORT}
      BACKEND_URL: http://api:8000
      REDIS_URL: redis://valkey:6379/0
      ACR_API_ORIGIN: http://acr-api:8080
      ACR_WEB_ASSERTION_AUDIENCE: dev-health-acr
      ACR_WEB_ASSERTION_ISSUER: dev-health-web
      ACR_WEB_ASSERTION_KEY_FILE: /run/acr-e2e/web-assertion.key
      ACR_WEB_ASSERTION_KID: acr-fullstack-web
      NEXT_PUBLIC_DEV_HEALTH_TEST_MODE: "false"
    volumes:
      - ${STATE}/web/web-assertion.key:/run/acr-e2e/web-assertion.key:ro
    depends_on:
      api: { condition: service_healthy }
      bugsink: { condition: service_started }
      valkey: { condition: service_healthy }
    networks: [dev-health]
EOF
}

device_login_env() {
  env -i \
    HOME="$DEVICE_LOGIN_HOME" \
    XDG_CONFIG_HOME="$DEVICE_LOGIN_HOME/config" \
    XDG_DATA_HOME="$DEVICE_LOGIN_HOME/data" \
    XDG_CACHE_HOME="$DEVICE_LOGIN_HOME/cache" \
    XDG_STATE_HOME="$DEVICE_LOGIN_HOME/state" \
    PATH="$DEVICE_LOGIN_BIN" \
    ACR_API_URL="https://localhost:${PORT}" \
    ACR_API_CA_BUNDLE="$STATE/pki/ca.crt" \
    ACR_API_TOKEN= \
    ACR_API_TOKEN_FILE= \
    ACR_API_TOKEN_KEYRING_SERVICE= \
    ACR_API_TOKEN_KEYRING_ACCOUNT= \
    ACR_API_TOKEN_KEYRING_DISABLED=true \
    ACR_SIDECAR_VERSION=1.0.0 \
    ACR_SIDECAR_CLIENT_VERSION=1.0.0 \
    "$@"
}

prepare_device_login_environment() {
  DEVICE_LOGIN_HOME="$ARTIFACTS/device-login-home"
  DEVICE_LOGIN_BIN="$ARTIFACTS/device-login-bin"
  mkdir -p "$DEVICE_LOGIN_HOME" "$DEVICE_LOGIN_BIN"
  chmod 700 "$DEVICE_LOGIN_HOME" "$DEVICE_LOGIN_BIN"
}

redact_device_login_log() {
  sed -E \
    -e 's/[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}/REDACTED_DEVICE_CODE/g' \
    -e 's/(fcacr|svc_acr)_[[:alnum:]_-]+/REDACTED/g' \
    -e 's#(postgresql?|clickhouse)://[^[:space:]]+#REDACTED_DSN#g'
}

wait_for_device_login_prompt() {
  local output="$STATE/secrets/device-login-output" attempts=0
  until grep -Eq '^Open https?://[^[:space:]]+ and enter code [ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$' "$output"; do
    attempts=$((attempts + 1))
    if [[ "$attempts" -ge 60 ]] || ! kill -0 "$DEVICE_LOGIN_PID" 2>/dev/null; then
      redact_device_login_log < "$output" > "$ARTIFACTS/logs/device-login-prompt.log" || true
      redact_device_login_log < "$output" >&2 || true
      fs_note "device login prompt diagnostics retained at logs/device-login-prompt.log"
      fs_die 'acr-mcp login did not emit a device verification prompt'
    fi
    sleep 0.5
  done
}

# device_login_mcp_roundtrip drives the installed stdio MCP server with the
# credential written by `acr-mcp login`. It always runs from a throwaway,
# non-Git directory and always supplies the repository explicitly, proving
# local checkout discovery and analytics inventory are not authorization
# inputs. When expand_evidence=yes, it also expands a reference returned by
# that exact context_for_task response.
device_login_mcp_roundtrip() {
  local slug="$1" artifact="$2" expand_evidence="${3:-no}"
  local non_git="$ARTIFACTS/device-login-non-git" response evidence_id
  local mcp_pid mcp_input mcp_output
  mkdir -p "$non_git"
  coproc DEVICE_SCOPE_MCP {
    cd "$non_git"
    device_login_env "$STATE/acr-mcp" serve 2>"$ARTIFACTS/logs/device-login-mcp.log"
  }
  mcp_pid="$DEVICE_SCOPE_MCP_PID"
  mcp_input="${DEVICE_SCOPE_MCP[1]}"
  mcp_output="${DEVICE_SCOPE_MCP[0]}"
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"device-login-acceptance","version":"1.0.0"}}}' >&"$mcp_input"
  if ! IFS= read -r -t 30 response <&"$mcp_output" || ! printf '%s\n' "$response" | jq -e '(.result.protocolVersion | type) == "string"' >/dev/null; then
    mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
    fs_die "device-login MCP initialize failed for ${slug}"
  fi
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' \
    "$(jq -cn --arg slug "$slug" '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:"context_for_task",arguments:{goal:"verify organization-wide device login",repository:{slug:$slug}}}}')" >&"$mcp_input"
  if ! IFS= read -r -t 30 response <&"$mcp_output"; then
    mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
    fs_die "device-login context_for_task timed out for ${slug}"
  fi
  printf '%s\n' "$response" > "$artifact"

  if [[ "$expand_evidence" == yes ]]; then
    printf '%s\n' "$response" | jq -e --arg slug "$slug" '(.result.isError // false) == false and .result.structuredContent.schema_version == "mcp_context_for_task_response.v1" and .result.structuredContent.structured.repository.slug == $slug' >/dev/null || {
      mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
      fs_die 'the actual device-login credential could not run context_for_task'
    }
    evidence_id="$(printf '%s\n' "$response" | jq -r '[.result.structuredContent | .. | objects | .evidence_ref_ids? | arrays | .[] | select(type == "string")][0] // empty')"
    [[ -n "$evidence_id" ]] || {
      mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
      fs_die 'the actual device-login context packet returned no evidence reference'
    }
    printf '%s\n' "$(jq -cn --arg id "$evidence_id" '{jsonrpc:"2.0",id:3,method:"tools/call",params:{name:"source_evidence",arguments:{evidence_ref_id:$id}}}')" >&"$mcp_input"
    if ! IFS= read -r -t 30 response <&"$mcp_output"; then
      mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
      fs_die 'device-login source_evidence timed out'
    fi
    printf '%s\n' "$response" > "${artifact%.json}-evidence.json"
    printf '%s\n' "$response" | jq -e --arg id "$evidence_id" '(.result.isError // false) == false and .result.structuredContent.schema_version == "mcp_source_evidence_response.v1" and .result.structuredContent.structured.evidence.evidence_ref_id == $id' >/dev/null || {
      mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
      fs_die 'the actual device-login credential could not run source_evidence'
    }
  fi
  mcp_shutdown "$mcp_pid" "$mcp_input" "$mcp_output"
}

run_device_login_read_acceptance() {
  local db org_id future_count own_cross_count foreign_cross_count
  db="$(ops_clickhouse_database)"
  org_id="$(<"$STATE/org-id")"

  device_login_mcp_roundtrip "$FULLSTACK_REPO_SLUG" "$ARTIFACTS/device-login-context.json" yes

  future_count="$(clickhouse_query "SELECT count() FROM ${db}.repos FINAL WHERE org_id = '${org_id}' AND repo = '${FULLSTACK_FUTURE_SLUG}'")"
  [[ "$future_count" == 0 ]] || fs_die 'the future-repository acceptance fixture unexpectedly exists in the analytics catalog'
  device_login_mcp_roundtrip "$FULLSTACK_FUTURE_SLUG" "$ARTIFACTS/device-login-future-context.json"
  jq -e '
    .id == 2 and .result != null
    and (if (.result.isError // false)
         then (.result.content | tostring | test("no_data"; "i"))
         else .result.structuredContent.schema_version == "mcp_context_for_task_response.v1"
           and .result.structuredContent.structured.resolved_scope.resolution == "unresolved"
           and (.result.structuredContent.structured.resolved_scope.fallback_reasons | index("authorized_repository_not_found")) != null
         end)
    and ((.result | tostring) | test("repo_forbidden|credential scope|category: auth"; "i") | not)
  ' "$ARTIFACTS/device-login-future-context.json" >/dev/null \
    || fs_die 'the organization-wide device credential rejected an uncataloged same-organization repository at authorization'

  # A foreign-organization canary exists in storage under a different OrgID.
  # The wildcard grant may name its slug, but every read must remain bound to
  # the authenticated principal's OrgID and return none of the canary row.
  clickhouse_query "INSERT INTO ${db}.repos (id, repo, ref, created_at, settings, tags, last_synced, org_id, provider) VALUES (generateUUIDv4(), '${FULLSTACK_CROSS_ORG_SLUG}', 'main', now64(3), NULL, NULL, now64(3), '${FULLSTACK_CROSS_ORG_ID}', 'cross-org-canary')" >/dev/null
  own_cross_count="$(clickhouse_query "SELECT count() FROM ${db}.repos FINAL WHERE org_id = '${org_id}' AND repo = '${FULLSTACK_CROSS_ORG_SLUG}'")"
  foreign_cross_count="$(clickhouse_query "SELECT count() FROM ${db}.repos FINAL WHERE org_id = '${FULLSTACK_CROSS_ORG_ID}' AND repo = '${FULLSTACK_CROSS_ORG_SLUG}'")"
  [[ "$own_cross_count" == 0 && "$foreign_cross_count" == 1 ]] || fs_die 'the cross-organization isolation canary was not provisioned deterministically'
  device_login_mcp_roundtrip "$FULLSTACK_CROSS_ORG_SLUG" "$ARTIFACTS/device-login-cross-org-context.json"
  jq -e '
    .id == 2 and .result != null
    and ((tostring | test("cross-org-canary"; "i")) | not)
    and (if (.result.isError // false) then (.result.content | tostring | test("no_data|auth"; "i"))
         else .result.structuredContent.structured.resolved_scope.resolution == "unresolved"
           and (.result.structuredContent.structured.resolved_scope.fallback_reasons | index("authorized_repository_not_found")) != null
           and ([.result.structuredContent.structured.items[]?.evidence_ref_ids[]?] | length) == 0
         end)
  ' "$ARTIFACTS/device-login-cross-org-context.json" >/dev/null \
    || fs_die 'the organization-wide device credential leaked cross-organization repository evidence'
}

run_device_login_lifecycle() {
  local output="$STATE/secrets/device-login-output" code_file="$STATE/secrets/device-user-code"
  local token_file mode
  [[ "$WEB_CHECK" == 'on' || "$WEB_CHECK" == 'auto' ]] || fs_die 'device login lifecycle requires the isolated web service'
  prepare_device_login_environment
  token_file="$DEVICE_LOGIN_HOME/.acr/token"
  compose up -d bugsink web-fullstack >/dev/null
  wait_web_ready
  bootstrap_web_user
  assert_acr_entitlement
  : > "$output"
  chmod 600 "$output"
  device_login_env "$STATE/acr-mcp" login --repo "$FULLSTACK_REPO_SLUG" >"$output" 2>&1 &
  DEVICE_LOGIN_PID=$!
  wait_for_device_login_prompt
  sed -nE 's/^Open [^[:space:]]+ and enter code ([ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8})$/\1/p' "$output" > "$code_file"
  chmod 600 "$code_file"
  [[ "$(wc -l < "$code_file" | tr -d '[:space:]')" == '1' ]] || fs_die 'device login did not produce exactly one user code'
  if ! env \
    DEVICE_LOGIN_WEB_URL="http://127.0.0.1:${WEB_PORT}" \
    DEVICE_LOGIN_WEB_EMAIL="$WEB_EMAIL" \
    DEVICE_LOGIN_WEB_PASSWORD="$WEB_PASSWORD" \
    DEVICE_LOGIN_CODE_FILE="$code_file" \
    DEVICE_LOGIN_ARTIFACTS="$ARTIFACTS/playwright" \
    DEVICE_LOGIN_PLAYWRIGHT_MODULE="$WEB_ROOT/node_modules/@playwright/test/index.js" \
    node "$SCRIPT_DIR/device-login-browser.mjs" > "$ARTIFACTS/logs/device-login-browser.log" 2>&1; then
    redact_log < "$ARTIFACTS/logs/device-login-browser.log" > "$ARTIFACTS/logs/device-login-browser.redacted.log"
    mv "$ARTIFACTS/logs/device-login-browser.redacted.log" "$ARTIFACTS/logs/device-login-browser.log"
    record_web_browser_failure
    cat "$ARTIFACTS/logs/device-login-browser.log" >&2
    fs_die 'web device approval browser flow failed'
  fi
  if ! wait "$DEVICE_LOGIN_PID"; then
    redact_device_login_log < "$output" >&2 || true
    fs_die 'acr-mcp login did not complete after web approval'
  fi
  DEVICE_LOGIN_PID=""
  grep -Eq 'fcacr_|svc_acr_' "$output" && fs_die 'acr-mcp login output leaked a credential'
  detect_stat_flavour
  if [[ "$STAT_FLAVOUR" == gnu ]]; then mode="$(stat -c '%a' "$token_file")"; else mode="$(stat -f '%Lp' "$token_file")"; fi
  [[ "$mode" == '600' ]] || fs_die "credential file must be mode 0600, found ${mode}"
  device_login_env "$STATE/acr-mcp" doctor --live > "$ARTIFACTS/device-login-doctor.json"
  grep -Fq '"credential_source":"file"' "$ARTIFACTS/device-login-doctor.json" || fs_die 'bare doctor did not discover the fallback credential file'
  run_device_login_read_acceptance
  device_login_env "$STATE/acr-mcp" login --refresh > "$ARTIFACTS/device-login-refresh.log"
  grep -Eq 'fcacr_|svc_acr_' "$ARTIFACTS/device-login-refresh.log" && fs_die 'credential refresh output leaked a credential'
  device_login_env "$STATE/acr-mcp" doctor --live > "$ARTIFACTS/device-login-doctor-refreshed.json"
  device_login_env "$STATE/acr-mcp" logout > "$ARTIFACTS/device-login-logout.log"
  grep -Eq 'fcacr_|svc_acr_' "$ARTIFACTS/device-login-logout.log" && fs_die 'logout output leaked a credential'
  device_login_env "$STATE/acr-mcp" doctor --live > "$ARTIFACTS/device-login-doctor-post-logout.log" 2>&1
  jq -e '.status == "incomplete_configuration" and .credential_set == false and .live_check.reachable == false' \
    "$ARTIFACTS/device-login-doctor-post-logout.log" >/dev/null \
    || fs_die 'doctor did not report the expected missing credential after logout'
  redact_device_login_log < "$ARTIFACTS/device-login-doctor-post-logout.log" > "$ARTIFACTS/device-login-doctor-post-logout.redacted.log"
  mv "$ARTIFACTS/device-login-doctor-post-logout.redacted.log" "$ARTIFACTS/device-login-doctor-post-logout.log"
  rm -f "$code_file" "$output"
}

wait_web_ready() {
  local attempts=0 started_at
  started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  until curl --fail --silent --show-error --noproxy '*' "http://127.0.0.1:${WEB_PORT}/health" >/dev/null; do
    attempts=$((attempts + 1))
    if [[ "$attempts" -ge 90 ]]; then
      record_web_readiness_failure "$started_at" "$attempts"
      fs_die 'web readiness timed out'
    fi
    sleep 2
  done
}

record_web_readiness_failure() {
  local started_at="$1" attempts="$2" service
  local receipt="$ARTIFACTS/logs/web-readiness.json"
  local service_log="$ARTIFACTS/logs/web-fullstack.log"
  service="$(compose ps --format json web-fullstack 2>/dev/null || printf 'null')"
  [[ -n "$service" ]] || service='null'
  compose logs --no-color web-fullstack 2>&1 | redact_log > "$service_log" || true
  jq -n --arg started_at "$started_at" --argjson attempts "$attempts" --argjson service "$service" \
    '{started_at:$started_at,attempts:$attempts,service:$service}' > "$receipt"
  fs_note "web readiness failed after ${attempts} attempts from ${started_at}; sanitized diagnostics: logs/web-readiness.json and logs/web-fullstack.log"
}

record_web_browser_failure() {
  local service_log="$ARTIFACTS/logs/web-fullstack.log"
  compose logs --no-color web-fullstack 2>&1 | redact_log > "$service_log" || true
  fs_note 'web device approval failed; sanitized diagnostics: logs/device-login-browser.log and logs/web-fullstack.log'
}

bootstrap_web_user() {
  local create_output create_status
  set +e
  create_output="$(compose exec -T api dev-hops admin users create --email "$WEB_EMAIL" --password "$WEB_PASSWORD" --full-name 'ACR Full-stack Account' 2>&1)"
  create_status=$?
  set -e
  if [[ "$create_status" -ne 0 ]] && ! grep -Fq "User with email ${WEB_EMAIL} already exists" <<<"$create_output"; then
    printf '%s\n' "$create_output" | redact_log >&2
    fs_die "could not create the isolated web user (dev-hops exited ${create_status})"
  fi
  # --superuser: dev-health-web's /agent-context/context-packet is now a compatibility alias
  # for pre-move bookmarks (see its own comment) -- Context Fabric validation moved to its
  # platform-admin home at /superadmin/context-fabric/validation, gated on
  # session.user.is_superuser, not on org role or the agent_context_runtime entitlement alone.
  # Without this the alias silently redirects a non-superuser org owner to /diagnose or /dev
  # instead, and run_web_agreement_check's wait for the "Context Fabric" heading times out
  # 30s later with no signal of why. This isolated harness user is torn down with the rest of
  # the project at the end of the run, so platform-superuser scope here is not a standing grant.
  compose exec -T api dev-hops admin users update --email "$WEB_EMAIL" --verified --superuser --org "$(<"$STATE/org-id")" --role owner >/dev/null
  assert_acr_entitlement
}

# The Context Packet Explorer is gated on a licensing entitlement, not just on data.
# `agent_context_runtime` is the sole member of the product's EXPLICIT_PURCHASE_FEATURES set,
# and decide_feature() closes an explicit-purchase feature for every organization unless an
# OrgFeatureOverride exists — checked *before* any tier-eligibility fallback, so a fresh
# community-tier org is closed by design, not by misconfiguration. Two consumers depend on it:
# the BFF's repository-catalog call, and the sidecar's /capabilities entitlements, which the web
# client hard-requires before it will request a packet at all. Without the override the picker
# renders permanently empty (an unexplained 30-second selectOption timeout) and, past that, the
# client fails 426.
#
# The shared driver already grants it: provision_ops_control_plane in compose.sh issues
# `bundles assign-org --feature-key agent_context_runtime --expires-days 1` when it creates the
# organization. This function therefore only *verifies* — an attempt to grant it a second time
# fails on the existing row. What was missing was never the grant; it was any check that the
# grant is in effect, so a regression in the driver would surface as that opaque timeout rather
# than as a named failure here.
assert_acr_entitlement() {
  local org_id entitled status=0
  org_id="$(<"$STATE/org-id")"
  # Read back the row the driver's grant writes — org_feature_overrides joined to feature_flags,
  # enabled and unexpired — which is the same state decide_feature() consults. Output is
  # captured and reported rather than discarded: swallowing it is exactly the defect this suite
  # filed against the product in CHAOS-3069, where a failure path recorded no error text.
  set +e
  entitled="$(compose exec -T -e "PGPASSWORD=${POSTGRES_PASSWORD}" postgres \
    psql -h postgres -U "$POSTGRES_USER" -d "$OPS_POSTGRES_DB" -At -v ON_ERROR_STOP=1 -c "
    SELECT count(*) FROM org_feature_overrides o
    JOIN feature_flags f ON f.id = o.feature_id
    WHERE o.org_id = '${org_id}'
      AND f.key = '${ACR_WEB_FEATURE_KEY}'
      AND o.is_enabled
      AND (o.expires_at IS NULL OR o.expires_at > now())" 2>&1)"
  status=$?
  set -e
  if [[ "$status" -ne 0 ]]; then
    printf '%s\n' "$entitled" | redact_log >&2
    fs_die "could not read back the ${ACR_WEB_FEATURE_KEY} entitlement (psql exited ${status})"
  fi
  entitled="$(printf '%s' "$entitled" | tr -d '[:space:]')"
  [[ "$entitled" == '1' ]] \
    || fs_die "the ${ACR_WEB_FEATURE_KEY} entitlement is not in effect for the harness organization (matched ${entitled:-0} override rows); provision_ops_control_plane in compose.sh is expected to have granted it, and without it the Explorer returns 403 not_entitled and the picker renders empty"
}

# run_web_agreement_check renders the same packet through the browser BFF and leaves the
# artifacts for the assertion tool's L6 comparison; it deliberately reuses the SVS browser
# driver rather than forking a second Playwright entry point.
# The Explorer's repository picker is populated from the Ops GraphQL repository catalog, which
# reads `repos` directly. This probes that same source so a data-shaped failure reports itself
# as one instead of as a 30-second selectOption timeout.
#
# It proves only that the data is there. It cannot tell you what the picker rendered: the web
# layer disables the control identically whether the catalog came back empty or errored, so a
# selectOption timeout is compatible with a fault anywhere in the chain — session org scoping,
# a GraphQL rejection, a schema mismatch — not just with an empty repository list. Treat a
# timeout as "something upstream failed", never as "the catalog was empty".
assert_repository_catalog_visible() {
  local db org_id visible api_db
  db="$(ops_clickhouse_database)"
  org_id="$(<"$STATE/org-id")"
  # Assert the application is pointed at the database this suite seeds, before asserting the
  # data is in it. Querying ClickHouse directly proves only that the rows exist somewhere: the
  # ops `api` service resolves its own CLICKHOUSE_URI at container start, and when that named a
  # different database it served a clean, empty, entirely valid catalog while this probe passed.
  # A probe that reads past the component under test cannot speak for it.
  api_db="$(compose exec -T api sh -c 'printf %s "${CLICKHOUSE_URI:-}"' 2>/dev/null | sed -nE 's#.*/([^/?]+)(\?.*)?$#\1#p')"
  [[ "$api_db" == "$db" ]] \
    || fs_die "the ops api service reads ClickHouse database '${api_db:-<unset>}' but this suite seeds '${db}'; the repository catalog would answer 200 with an empty list"
  visible="$(clickhouse_query "SELECT count() FROM (SELECT lowerUTF8(trimBoth(repo)) AS canonical_repo FROM ${db}.repos FINAL WHERE org_id = '${org_id}') WHERE canonical_repo = '${FULLSTACK_REPO_SLUG}'")"
  [[ "$visible" == '1' ]] \
    || fs_die "the Ops repository catalog cannot see ${FULLSTACK_REPO_SLUG} (matched ${visible:-0} rows); the Explorer's picker would render empty and disabled"
}

run_web_agreement_check() {
  local task_id='task-002-auth-refactor-branch'
  [[ -d "$WEB_ROOT/node_modules/@playwright/test" ]] || fs_die 'web dependencies with @playwright/test are required for the web agreement check'
  compose up -d bugsink web-fullstack >/dev/null
  wait_web_ready
  bootstrap_web_user
  assert_repository_catalog_visible
  # SVS_TASK_REFERENCE is deliberately empty: task_ref is an exact-match filter on
  # work_items.v1/work_item_dependencies.v1/work_graph.v1 server side, and the direct-HTTP/MCP
  # paths this check is compared against never set it. A ticket reference here (e.g.
  # "CHAOS-3065") does not match this fixture's seeded work items and silently drops those
  # sources to "unavailable" only on the browser path -- see svs-browser.mjs's optionalValue().
  # A comment MUST NOT sit inside this backslash-continued env-var chain: bash only escapes
  # the newline that immediately follows a `\`, so a `#...` line here would silently end the
  # logical command right there, turning every SVS_* assignment before it into inert local
  # shell variables that never reach `node` below -- exactly the "SVS_WEB_URL is required"
  # crash this comment's own first draft caused.
  SVS_WEB_URL="http://127.0.0.1:${WEB_PORT}" \
  SVS_WEB_EMAIL="$WEB_EMAIL" \
  SVS_WEB_PASSWORD="$WEB_PASSWORD" \
  SVS_GOAL="$(task_field "$task_id" '.goal')" \
  SVS_REPOSITORY="$FULLSTACK_REPO_SLUG" \
  SVS_BRANCH="$(task_field "$task_id" '.scope.branch // ""')" \
  SVS_TASK_REFERENCE="" \
  SVS_BROWSER_PACKET="$ARTIFACTS/web-packet.json" \
  SVS_BROWSER_EVIDENCE="$ARTIFACTS/web-evidence.json" \
  SVS_BROWSER_SCREENSHOT="$ARTIFACTS/playwright/context-packet.png" \
  SVS_PLAYWRIGHT_MODULE="$WEB_ROOT/node_modules/@playwright/test/index.js" \
  node "$SCRIPT_DIR/svs-browser.mjs" || {
    compose logs --no-color web-fullstack 2>&1 | redact_log >&2 || true
    fs_die 'the live Context Packet Explorer check failed'
  }
  assert_task "$task_id"
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

assert_project_unused
prepare_artifacts
record_host_config_digest
assert_pinned_opencode
prepare_state
ensure_image
if web_check_enabled; then
  write_web_assertion_material
  render_web_override
  export ACR_E2E_DEVICE_VERIFICATION_URL="http://127.0.0.1:${WEB_PORT}/acr/device"
fi
render_override
assert_safe_render
prepare_stack
rotate_acr_credential
verify_fixture
record_service_readiness
write_run_manifest
capture_capabilities
capture_mcp_tools

if web_check_enabled; then
  run_device_login_lifecycle
fi

TASKS=()
while IFS= read -r selected_task; do
  [[ -n "$selected_task" ]] && TASKS+=("$selected_task")
done < <(selected_tasks)
# `while read` runs in a subshell of the process substitution, so a fs_die inside
# selected_tasks cannot exit this shell on its own; the emptiness check below is what enforces
# it here.
[[ "${#TASKS[@]}" -gt 0 ]] || fs_die "no tasks were selected for scenario ${SCENARIO}"

for task in "${TASKS[@]}"; do
  case "$task" in
    task-004-foreign-repo-denied) run_foreign_repo_task ;;
    task-005-unavailable-evidence) run_unavailable_evidence_task ;;
    *) run_client_task "$task" ;;
  esac
done

if [[ "$SCENARIO" == 'self-test' ]]; then
  run_fault_self_test
fi

if web_check_enabled; then
  run_web_agreement_check
fi

# The summary is mandatory: if no per-task report exists, no task was graded, and the run must
# not be able to end in PASS.
#
# Self-test reports are kept in their own key rather than mixed into `reports`. They are
# *expected* failures — a faulted replay that passed would be the bug — so a consumer that
# reasonably reads `reports[].ok` as the run's verdict would otherwise see a green self-test
# run reporting failed tasks. They are still emitted, not dropped: a rejection nobody can see
# is no better than one that never happened.
compgen -G "$ARTIFACTS/assertion-report-*.json" >/dev/null \
  || fs_die 'no per-task assertion report was produced; nothing was graded'
SUMMARY_TASK_REPORTS=()
SUMMARY_SELFTEST_REPORTS=()
while IFS= read -r report_file; do
  case "$(basename "$report_file")" in
    assertion-report-selftest-*) SUMMARY_SELFTEST_REPORTS+=("$report_file") ;;
    assertion-report.json) ;;
    *) SUMMARY_TASK_REPORTS+=("$report_file") ;;
  esac
done < <(find "$ARTIFACTS" -maxdepth 1 -name 'assertion-report-*.json' -print | sort)
[[ "${#SUMMARY_TASK_REPORTS[@]}" -gt 0 ]] \
  || fs_die 'no per-task assertion report was produced; nothing was graded'
jq -n \
  --slurpfile tasks <(cat "${SUMMARY_TASK_REPORTS[@]}") \
  --slurpfile rejected <(cat "${SUMMARY_SELFTEST_REPORTS[@]}" 2>/dev/null || printf '') \
  '{schema_version:"fullstack_assertion_summary.v1",reports:$tasks,expected_rejections:$rejected}' \
  > "$ARTIFACTS/assertion-report.json" \
  || fs_die 'the assertion summary could not be assembled'
SAFE_BOUNDARY="a real OpenCode session drove the live ACR read path through host-local acr-mcp and every layer matched the ${SCENARIO} oracle"
fs_note "PASS: ${SCENARIO}: ${SAFE_BOUNDARY}"
