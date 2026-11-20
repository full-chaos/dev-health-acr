# O4 fact-oracle venue

The O4 oracle (`internal/contextfabric/factoracle`) replays a recorded capture in
`go test`. A capture is taken again when an operation contract or a fact query
version moves past it (`advancedPastCapture` in `recorded_test.go`). This page
says what a venue must serve, how to check it, and how to capture.

## What a capture needs (any host)

The capture reads only the environment below. Docker Compose, Kubernetes and a
local macOS or Linux stack are all valid venues if they serve these services.

| Service | Requirement |
| --- | --- |
| ops `query-api` | the build acr vendors (`contracts/mcp/ops-catalogue/registry.v1.json` `commit`); `AUTH_ACCESS_TOKEN_ISSUER` set; its internal listener and its MCP listener reachable by acr-api |
| ops `go-api` | same build; a workload credential file (`AUTH_WORKLOAD_SUBJECT_FILE` or `AUTH_WORKLOAD_BOOTSTRAP_FILE`) |
| ops `auth-service` and its issuer front | the access-token issuer and the workload exchange |
| `acr-api` | acr main; the workload client (`ACR_AUTH_WORKLOAD_*`); `ACR_DATA_QUERY_URL` and `ACR_DATA_GRAPHQL_URL` set to query-api |
| `acr-mcp` | the hosted HTTP transport (`ACR_MCP_TRANSPORT=http`) in front of acr-api |
| ClickHouse | rows of one **synthetic** organization in the 31-day window; a read-only user for the capture (`readonly=2`: the capture sets its own memory limit per query) |
| Postgres, Valkey | as the ops and acr services need them |

### Data

Seed one synthetic organization; never use real or production data. ops provides
the seed and the derive chain, run from the same ops image:

1. `dho fixtures generate --sink <clickhouse dsn> --db-type clickhouse --org <org> --provider mixed --repo-name chaos-fixtures/shared-api --repo-count 18 --days 180 --commits-per-day 0 --pr-count 189 --team-count 8 --seed 9440`
2. With a reconciler and a worker on the queues `investment,metrics,reports,workgraph`:
   `dho workers metrics daily-start --org <org> --day <first> --to <last>` (at most 31 days),
   `dho workers investment trigger --org <org> --review-evidence testing`,
   `dho workers workgraph trigger --org <org> --review-evidence testing`; when those jobs are
   done, `dho workers metrics remaining trigger-backstop --family capacity --all-teams --org <org> --review-evidence testing`
   (the forecast reads the daily metrics). Each verb takes `--reason` and `--correlation-id`.
   Stop the worker and the reconciler before the capture: a store that moves during the run
   is refused.
3. If `work_unit_investments` or `work_unit_membership_runs` stays empty (the venue has no LLM
   provider, and membership has no manual ops trigger), also load the `_mg` world for the same
   organization (`--provider synthetic --repo-name acme/live-e2e --days 14 --commits-per-day 6 --pr-count 24 --team-count 10 --seed 20260219 --with-metrics --with-work-graph --allow-mixed-org`).

`work_unit_issue_size` must stay empty (acr seeds it empty: every work unit has weight 1, the
weight the pairs are recorded under). Limit: the issue-size weighting of repository effort
(ops `internal/queryapi/analytics/sankeycoverage.go:320-322`) is NOT MEASURED by O4. The
capture manifest says so in `venue_note`.

Window rule per reader: the default window ends at the next UTC midnight. The ops analytics
readers (investment breakdowns, team work items, evidence stats; ops D6101,
`internal/queryapi/analytics/window.go`) leave a day that is not over out of every window, so the
oracle's analytics pairs read the store and the acr investment facts up to the start of the
capture day. capacityForecasts, compoundingRisk and throughputForecast count the capture day.

Limit: root `catalog` compares only `owner/name` repository names, as the ops repository
inventory lists only those. The capture reports every other name (for example a GitLab
nested-group slug) as "not joined: N", never as a silent drop.

Acceptance gate cells the capture cannot build are NOT MEASURED by name, only when listed in
`internal/contextfabric/factoracle/testdata/venue/not_measured_gates.json` with the unit shape
the capture lacks. An unlisted cell with no unit fails the gate, and a listed cell whose shape the
capture holds fails too. On the synthetic venue capture of 2026-11-20:

| Gate cell | Shape the capture holds none of |
| --- | --- |
| `AcceptanceGate/MembershipScope` | a categorized work unit in the analytics window outside a complete membership run |
| `AcceptanceGate/NullRepoID` | a team-owned repository with work-unit effort in the analytics window |
| `TeamRollupLossWithNoNullRowIsAFinding` | a team-owned repository with work-unit effort in the analytics window |

Cause: the work units with investments come from the `_mg` world, whose one repository no team
owns, and ops has no existing verb that derives team ownership for a synthetic world (the
derivation runs only after a git or work-item sync).

### Venue steps before the capture

- Project the graph of the organization (acr-projector) and stop it: acr-api reads subjects
  and repository grants from it.
- Enable the MCP class roots on query-api: `dho goapi routing enable -operations all-mcp
  -mode canary` (ops `docs/operate/runbooks/query-api-bootstrap.md`, Step 6), with a platform
  admin's user access token of the venue in `GO_API_ROUTING_ACCESS_TOKEN_FILE`. Without it
  query-api's MCP listener answers `root_field_not_enabled`.
- Mint the MCP credential on the venue's acr-api (device grant with `data:read`).

### Environment of the capture

| Variable | Value |
| --- | --- |
| `ACR_O4_MCP_URL` | the hosted MCP endpoint (`.../mcp`) |
| `ACR_O4_TOKEN_FILE` | file (0600) with an ACR credential that carries `data:read` for the organization |
| `ACR_O4_CLICKHOUSE_DSN_FILE` | file (0600) with the read-only ClickHouse DSN |
| `ACR_O4_CLICKHOUSE_ADDR` | optional: replaces the DSN host |
| `ACR_O4_ORG_ID` | the synthetic organization |
| `ACR_O4_OPS_BUILD`, `ACR_O4_ACR_BUILD` | repository names, commits, versions (other words are dropped) |
| `ACR_O4_WINDOW_END` | optional: the window end day (`YYYY-MM-DD`) |
| `ACR_O4_VENUE_NOTE` | where the data comes from; written to the manifest as `venue_note` |

### Steps

```sh
set -a; . <env file>; set +a
make o4-oracle-preflight   # read only: env, file modes, read-only user, rows per table, MCP answer
make o4-oracle-capture     # writes testdata/venue
go test -run '^TestTheCaptureHoldsNoHostEmailOrRealOrganization$' ./internal/contextfabric/factoracle
make o4-oracle-repin
```

Then, in `recorded_test.go`: set `captureSchemaDigest` to the new manifest's
`schema_digest` and empty `advancedPastCapture`.

## One way: an isolated Docker Compose project

`scripts/e2e/o4-venue.sh` brings the venue up as its own Compose project
(`acr-e2e-o4venue-*`). It uses its own volumes, networks and ports, and never
touches another project. It needs a `dev-health-ops` checkout at the vendored pin
(`O4_VENUE_OPS_DIR`), Docker with the compose plugin, git, curl and openssl.
It runs on bash 3.2 (macOS) and on Linux.

```sh
export O4_VENUE_OPS_DIR=<ops checkout at the pin>
scripts/e2e/o4-venue.sh check    # tools, host load and disk gates, free ports and subnets
scripts/e2e/o4-venue.sh up       # pinned images, empty volumes, synthetic organization
scripts/e2e/o4-venue.sh seed     # world + derive chain + graph; worker, reconciler, projector stop
scripts/e2e/o4-venue.sh counts   # rows per table (all > 0; work_unit_issue_size exactly 0)
scripts/e2e/o4-venue.sh token    # MCP credential from this project's acr-api (0600)
scripts/e2e/o4-venue.sh routes   # MCP class roots on; the admin token is removed after use
scripts/e2e/o4-venue.sh env      # writes <state>/o4.env (0600)
scripts/e2e/o4-venue.sh down     # down -v of this project, residue check, credentials removed
```

| Variable | Default |
| --- | --- |
| `O4_VENUE_OPS_TAG` / `O4_VENUE_ACR_TAG` | `sha-2c11540` / `sha-24513ae` |
| `O4_VENUE_API_PORT`, `O4_VENUE_MCP_PORT` | `28443`, `28081` (127.0.0.1 only) |
| `O4_VENUE_CH_HTTP_PORT`, `O4_VENUE_CH_NATIVE_PORT` | `28123`, `29000` (127.0.0.1 only) |
| `O4_VENUE_PLANES_SUBNET`, `O4_VENUE_QUERY_SUBNET` | `10.199.91.0/28`, `10.199.89.0/28` |
| `O4_VENUE_MAX_LOAD` | `40` (1-minute load; above it the script refuses to start: wait and run again) |
| `O4_VENUE_MIN_DISK_GB` | `50` (free space under the Docker root) |

The venue is not a pull request gate.
