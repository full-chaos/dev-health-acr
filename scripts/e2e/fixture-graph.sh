#!/usr/bin/env bash
set -euo pipefail

# fixture-graph.sh: the standing use-case venue. It seeds ClickHouse from the ops fixture
# generator (two frozen worlds, so the organization holds two repositories), runs the real
# acr-projector into a real FalkorDB until it reports the graph built, then drives the real
# host-local acr-mcp as a client and asserts use-cases from the entity tree
# (repository <> pull request <> issue <> project). Expected values are derived from the seeded
# rows by ClickHouse queries inside tests/fixturegraph, never typed.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck disable=SC1091
source "${SCRIPT_DIR}/compose.sh"

# allow: SIZE_OK — one trap owns the isolated graph-venue lifecycle.

FG_WORLD_ONE_SLUG='acme/live-e2e'
FG_WORLD_TWO_SLUG='ci-metrics-executed-proof/repo'

fg_note() { printf '[fixture-graph] %s\n' "$*" >&2; }
fg_die() { printf '[fixture-graph] FAIL: %s\n' "$*" >&2; exit 1; }

# The generator refuses a POSTGRES_URI (it writes analytics rows only), so it runs with the
# ClickHouse DSN alone, unlike compose.sh's dho wrapper.
dho_analytics() {
  compose run --rm --no-deps -T -e "CLICKHOUSE_URI=${DHO_CLICKHOUSE_URI}" query-api "$@"
}

# One generator run per frozen world, both for the provisioned organization. The generator
# writes its own repos row; nothing here inserts one by hand.
seed_fixture_worlds() {
  local db org_id sink
  db="$(ops_clickhouse_database)"
  org_id="$(<"$STATE/org-id")"
  sink="clickhouse://default:ch@clickhouse:9000/${db}"
  DHO_CLICKHOUSE_URI="$sink" dho_analytics fixtures generate --sink "$sink" --db-type clickhouse --org "$org_id" \
    --repo-name "$FG_WORLD_ONE_SLUG" --provider synthetic --repo-count 1 --days 14 --commits-per-day 6 --pr-count 24 --team-count 10 \
    --seed 20260219 --with-metrics --with-work-graph >"$STATE/fixtures-world-one.json" || fg_die 'fixture world one did not load'
  DHO_CLICKHOUSE_URI="$sink" dho_analytics fixtures generate --sink "$sink" --db-type clickhouse --org "$org_id" \
    --repo-name "$FG_WORLD_TWO_SLUG" --provider synthetic --repo-count 1 --days 7 --commits-per-day 5 --pr-count 20 --team-count 1 \
    --seed 4276 >"$STATE/fixtures-world-two.json" || fg_die 'fixture world two did not load'
  local slug count
  for slug in "$FG_WORLD_ONE_SLUG" "$FG_WORLD_TWO_SLUG"; do
    count="$(clickhouse_query "SELECT count() FROM ${db}.repos FINAL WHERE org_id = '${org_id}' AND repo = '${slug}'")"
    [[ "$count" == "1" ]] || fg_die "generator wrote ${count} repos rows for a world, want exactly 1"
  done
}

write_graph_override() {
  local org_id
  org_id="$(<"$STATE/org-id")"
  cat > "$STATE/graph.override.yml" <<EOF
services:
  acr-api:
    environment:
      ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED: "true"
      ACR_CONTEXT_FABRIC_FALKOR_ADDR: falkordb:6379
      ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE: "true"
      ACR_CONTEXT_FABRIC_FALKOR_GRAPH_PREFIX: acr-cf
  acr-projector:
    environment:
      ACR_CONTEXT_FABRIC_PROJECTION_ENABLED: "true"
      ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS: "${org_id}"
      ACR_CONTEXT_FABRIC_FALKOR_ADDR: falkordb:6379
      ACR_CONTEXT_FABRIC_PROJECTION_POLL_INTERVAL: 3s
      ACR_LOG_LEVEL: info
EOF
}

# The projector is caught up for the one configured organization when two consecutive tick
# summaries each say the tick was complete, the organization is ok, and nothing failed, was
# withheld or is waiting for a rebuild. No such line inside the deadline fails the run.
wait_projector_caught_up() {
  local attempts=0 good=0 line
  while [[ "$attempts" -lt 120 ]]; do
    attempts=$((attempts + 1))
    line="$(compose logs --no-color --no-log-prefix acr-projector 2>/dev/null | grep 'projection tick freshness summary' | tail -1 || true)"
    if [[ -n "$line" ]] && printf '%s\n' "$line" | jq -e '
        .orgs_configured == 1 and .orgs_ok == 1 and .tick_complete == true
        and .orgs_rebuild_required == 0 and .orgs_backoff == 0 and .orgs_source_failed == 0
        and .orgs_pair_failed == 0 and .orgs_truncated == 0 and .orgs_unevaluated == 0
        and .orgs_stale == 0 and .sources_failed == 0 and .build_sources_failed == 0
        and .sources_in_failure_backoff == 0 and .pair_failures == 0' >/dev/null 2>&1; then
      good=$((good + 1))
      [[ "$good" -ge 2 ]] && { fg_note 'projector reports the graph built for the organization'; return 0; }
    else
      good=0
    fi
    sleep 3
  done
  compose logs --no-color acr-projector 2>&1 | redact_log | tail -40 >&2 || true
  fg_die 'the projector never reported a complete, ok tick for the organization'
}

# write_clickhouse_wrapper makes the one command the Go tests use to read the seeded rows:
# SQL on stdin, tab-separated rows on stdout, run inside the isolated ClickHouse.
write_clickhouse_wrapper() {
  local argv=() entry out="$STATE/chq.sh"
  while IFS= read -r -d '' entry; do argv+=("$entry"); done < <(compose_argv)
  {
    printf '#!/usr/bin/env bash\nset -euo pipefail\nsql="$(cat)"\nexec'
    printf ' %q' "${argv[@]}"
    printf ' exec -T clickhouse clickhouse-client --user default --password ch --database %q --format TSVRaw --query "$sql"\n' "$(ops_clickhouse_database)"
  } > "$out"
  chmod 700 "$out"
}

create_credential() {
  local scope="$1" name="$2" out="$3" token
  token="$(compose run --rm --no-deps acr-credentials credentials create --org-id "$(<"$STATE/org-id")" --repository-scope "$scope" --scope context:read,evidence:read --name "$name" --actor fixture-graph 2>"$STATE/cred.stderr")" \
    || { redact_log < "$STATE/cred.stderr" >&2 || true; fg_die "credential ${name} was not created"; }
  [[ "$token" == fcacr_* ]] || fg_die "credential ${name} has an invalid token shape"
  write_secret "$out" "$token"
}

# A test run that executed nothing is a failure: the go test JSON stream must name every
# top-level test the package declares as passed.
run_use_case_tests() {
  local json="$STATE/fixturegraph-tests.json" declared passed
  declared="$(cd "$REPO_ROOT" && go test -tags fixturegraph -list '^Test' ./tests/fixturegraph/ | grep '^Test' | LC_ALL=C sort)"
  [[ -n "$declared" ]] || fg_die 'the use-case package declares no test'
  set +e
  (cd "$REPO_ROOT" && FG_API_URL="https://localhost:${PORT}" FG_CA_FILE="$STATE/pki/ca.crt" \
    FG_ORG_TOKEN_FILE="$STATE/secrets/fg-org-token" FG_SCOPED_TOKEN_FILE="$STATE/secrets/fg-scoped-token" \
    FG_SCOPED_SLUG="$FG_WORLD_ONE_SLUG" FG_OTHER_SLUG="$FG_WORLD_TWO_SLUG" \
    FG_MCP_BIN="$STATE/acr-mcp" FG_CH_QUERY="$STATE/chq.sh" FG_ORG_ID="$(<"$STATE/org-id")" \
    go test -tags fixturegraph -count=1 -timeout 20m -json ./tests/fixturegraph/ >"$json")
  local status=$?
  set -e
  grep -E '"Action":"(output)"' "$json" | jq -r 'select(.Output != null) | .Output' | sed -e 's/[[:space:]]*$//' | grep -v '^$' >&2 || true
  [[ "$status" -eq 0 ]] || fg_die 'a use-case test failed'
  passed="$(jq -r 'select((.Action == "pass" or .Action == "skip") and .Test != null and (.Test | contains("/") | not)) | .Test' "$json" | LC_ALL=C sort)"
  [[ "$passed" == "$declared" ]] || { printf 'declared:\n%s\npassed or skipped:\n%s\n' "$declared" "$passed" >&2; fg_die 'a declared use-case test did not run'; }
  local skipped_other
  skipped_other="$(jq -r 'select(.Action == "skip" and .Test != null and (.Test | contains("/") | not) and (.Test | startswith("TestGap") | not)) | .Test' "$json")"
  [[ -z "$skipped_other" ]] || fg_die "a use-case test was skipped: ${skipped_other}"
}

main() {
  parse_args "$@"
  assert_project_unused
  prepare_state
  ensure_image
  export ACR_E2E_REQUESTS_PER_MINUTE=3000
  render_override
  assert_safe_render

  # No Go-API routing enablement: none of the tools this venue calls reads through it.
  provision_ops_control_plane
  provision_evidence_database
  seed_fixture_worlds
  grant_clickhouse_reader
  write_graph_override
  compose up -d --wait falkordb >/dev/null || fg_die 'falkordb did not become healthy'
  prepare_acr_database
  compose up -d acr-api acr-tls-proxy >/dev/null
  wait_https_ready
  compose up -d acr-projector >/dev/null
  wait_projector_caught_up
  build_host_mcp
  create_credential '*' fixture-graph-org "$STATE/secrets/fg-org-token"
  create_credential "$FG_WORLD_ONE_SLUG" fixture-graph-scoped "$STATE/secrets/fg-scoped-token"
  write_clickhouse_wrapper
  run_use_case_tests
  note 'PASS: fixture-graph'
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

main "$@"
