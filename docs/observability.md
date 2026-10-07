# Observability contract

`internal/observability` is an in-process, dependency-free observation boundary.
It ships bounded `MemorySink` and `SlogSink` implementations; a hosting service may
also supply a `Sink` that maps `SupportSnapshot` values to its existing metrics
system.

## Safe event contract

Each completed request, store, ranking, evidence, or episode operation may emit one
snapshot through the corresponding `Hooks.Observe*` method. A snapshot has a
generated request ID for correlation and these bounded dimensions:

- `kind`: `request`, `store`, `ranking`, `evidence`, or `episode`
- `operation` and HTTP status class: finite endpoint families and `2xx`/`4xx`/`5xx`
- `outcome`: `success`, `failure`, `denied`, `canceled`, or `unknown`
- packet: lifecycle `status`, bytes, tokens, schema/baseline versions,
  compatibility, and source coverage
- store query class: `packet`, `evidence`, `episode`, or `unknown`
- store source: one fixed versioned catalog source ID or `unknown`
- store phase: `query`, `scan`, `iteration`, `close`, or `unknown`
- `source_fallback`: `none`, `catalog`, `unavailable`, or `unknown`
- query/ranking versions: canonical `context-query.v1`, `ranker.v2`, or `unknown`
- denial class: `authentication`, `organization_scope`, `repository_scope`,
  `license`, `rate_limit`, `none`, or `unknown`

The sink receives the snapshot only, never a `context.Context`, request body,
evidence reference, transcript, debug payload, credential, or arbitrary attribute
map. Unknown values are normalized to `unknown`; this prevents untrusted or
high-cardinality strings becoming labels. Request IDs must match generated
`req_` + 32 hexadecimal characters before they are propagated. Other inbound IDs
are ignored and `EnsureRequestID` creates a fresh secure random ID.

Use the service's existing structured logger to record snapshots. Never add packet
content, evidence URLs, error text, bearer values, license artifacts, transcripts,
repository names, organization IDs, or request paths as an attribute or metric
label.

Store source and phase are support-snapshot and structured-log fields. They are
intentionally excluded from the default metric labels so a source failure can be
diagnosed without expanding the service's metric cardinality.

## Metric mapping

Each process exports these OpenTelemetry instruments on its own meter provider
(`OTEL_ENABLED=true`). Every label is a closed vocabulary; a value outside its
vocabulary is recorded as `other`. Each instrument is recorded at the site of the
log line that already certifies the outcome, never on a second path.

| Metric | Type | Labels | Recorded at |
| --- | --- | --- | --- |
| `acr_mcp_tool_calls_total` | counter | `tool`, `result_class`, `status` (HTTP status class) | acr-mcp request line (`tools/call` only) |
| `acr_mcp_tool_latency_seconds` | histogram | `tool` | acr-mcp request line (`tools/call` only) |
| `acr_answers_total` | counter | `status`, `tool` | acr-mcp answer display (`investigate_question`, `investigate_with_interpretation`) |
| `acr_budget_refusals_total` | counter | none | acr-api investigation failure line (class `budget_refusal`) and the response-budget exceed line (`context fabric response exceeded service limits`, 413), on both the investigation and the stored-result route |
| `acr_answer_reuse_total` | counter | `outcome` | acr-api answer reuse outcome line |
| `acr_requirement_outcomes_total` | counter | `outcome` | acr-api completeness authority line (one count per outcome row; an investigation without a plan adds no rows) |
| `acr_fact_read_aborts_total` | counter | `cause` | acr-api investigation failure line, fact read abort |
| `acr_investigation_latency_seconds` | histogram | `status` (`error` when no answer was delivered, including a 413 from a response-size gate) | acr-api investigation route |

Both histograms use explicit boundaries (seconds): 0.05, 0.1, 0.25, 0.5, 1, 2.5,
5, 10, 20, 30, 45, 60, 90, 120, 180, 300. They reach 300 s because the observed
p99 is about 125 s for an MCP request and about 21 s for an investigation.

The server span of every MCP request carries `acr.tool` and `acr.result_class`;
a delivered investigation answer also sets `acr.query_version` (the build's
query version, never a caller value).

`request_id` is a correlation field only: it MUST NOT be a metric label. Missing
dimensions use `unknown`; no backend-specific dynamic label may be added.
Per-organization and per-credential request/resource totals come from the bounded
`internal/limits.Manager.Usage` interface and are not emitted as metric labels.

## SLOs and alerts

The measurements below name snapshot-derived series (`acr_observation_*`,
`acr_packet_*`); no instrument of those names is exported. They are computed from
the `observability snapshot` log lines until an instrument exists.

All windows below are rolling, production-only, and require at least 100 relevant
requests in the evaluated window. API availability is exactly
`non-5xx / all responses`, using `http_status_class`; it includes `5xx` failures
and excludes no server responses. Denials/cancellations stay visible separately.
No traffic is not success: it triggers pipeline-health after 15 minutes of expected
traffic. The data scope is all service ingress and sidecar requests that call the
hook, with a snapshot emitted exactly once at each terminal lifecycle boundary.

| Objective | Measurement | Target | Alert |
| --- | --- | --- | --- |
| Request availability | `non-5xx / all` for `kind=request` | >= 99.9% over 30d | page when < 99.0% for 10m; ticket when < 99.9% for 1h |
| Request latency | p95 `acr_observation_duration_seconds` for successful requests | <= 500ms over 30d | page when > 1s for 10m; ticket when > 500ms for 1h |
| Packet health | `complete / (complete + partial + degraded + empty)` for `kind=store` | >= 99.5% over 30d | ticket when partial+degraded > 1.0% for 30m; page when > 5.0% for 10m |
| Empty packets | `empty / (complete + partial + degraded + empty)` for `kind=store` | <= 0.5% over 30d | ticket when > 1.0% for 30m; page when > 5.0% for 10m |
| Ranking latency | p95 duration for successful rankings | <= 250ms over 30d | ticket when > 250ms for 1h |
| Evidence fallback | `source_fallback!=none / kind=evidence` | <= 1.0% over 30d | ticket when > 3.0% for 30m; page when > 10.0% for 10m |
| Evidence expansion latency | p95 duration for successful `kind=evidence` | <= 250ms over 30d | ticket when > 250ms for 1h; page when > 1s for 10m |
| Audit delivery | `audit_delivery=delivered / (delivered + failed)` | >= 99.9% over 30d | ticket when < 99.9% for 1h; page when < 99.0% for 10m |
| API/sidecar compatibility | `compatibility=compatible / all known compatibility states` | >= 99.9% over 30d | ticket when < 99.9% for 1h; page on any incompatible state for 10m |
| Observation completeness | observations with a valid request ID / all observations | >= 99.99% over 30d | ticket when < 99.9% for 30m |
| Authorization denials | `denied / all` grouped by `denial_class` | alert-only, no SLO | ticket when any class > 5% for 30m; page when `authentication` or `organization_scope` > 20% for 10m |

Alert messages may include aggregate label values and a safe request ID sampled from
an event. They must not include input strings or unbounded identifiers. If a metric
series is absent, treat it as no traffic, not success; emit a separate telemetry
pipeline-health alert when expected production traffic produces no observations for
15 minutes.

## SVS and durability boundaries

`MemorySink` is a single-replica diagnostic buffer. It has bounded retention,
provides no cross-process visibility, and resets on process restart. Its values are
not billing, entitlement, audit, or incident-source-of-truth data. Durable audit
delivery is represented only as the bounded `audit_delivery` observation dimension;
the authoritative audit record remains the owning storage implementation.

The in-memory request-control manager and authentication limiter have the same
single-replica, restart-reset boundary. Their usage totals support SVS quota and
future billing decisions but are not durable billing truth. Horizontal scaling
requires an atomic shared backend before these controls can be considered global.

Packet health reports `complete`, `partial`, `degraded`, or `empty`, together with
item, stale-source, unavailable-source, compatibility, and version-mismatch
dimensions. `context-query.v1` and `ranker.v2` are aliases of the canonical
context-packet constants, not copyable telemetry literals. Query timeout and store
backend dimensions expose database/query behavior without statement text or IDs.

This package supplies bounded snapshots and a standard-library `SlogSink`; it
exports nothing itself. Process-level OTLP export (HTTP server spans, HTTP
server metrics, and a copy of the structured log lines) is the separate
`internal/otelexport` package, off unless `OTEL_ENABLED=true` -- see
[operations](operations.md#opentelemetry-export).

The deterministic API driver exercises one canonical request ID through
authentication, per-class admission, the real evaluation evidence store and
assembler/ranker, resource completion, and the terminal response observation.
Production evidence-store factories inject packet and expansion observers;
concrete catalog queries report individual ClickHouse failures/timeouts; packet
assembly emits request, store-query, ranking, and final-assembly trace boundaries;
real episode create/redact terminals report episode outcomes; and actual episode
store calls independently report their own backend latency, outcome, and timeout.
Compatibility is derived from the client sidecar and assembled packet schema
versions rather than supplied as a telemetry-only value.
A deployed seeded HTTP packet route remains blocked pending that owning
integration decision and is not claimed by this change.

## Decision-event certificate contract

`internal/contextfabric/eventspec` is the one declaration authority for a
production **decision-event** log line -- a structured `slog` line a resolver,
engine, or model-runtime decision scope emits at a terminal or measurement
point, as distinct from this file's request/store/ranking/evidence/episode
support snapshots above. One `eventspec.Event` value declares, for one
variant: its stable `ID`, the exact `Msg` a producer emits it under, its
required `Level`, how many lines one scoped pass may produce (`Multiplicity`),
which fields jointly attribute a line to its owning scope and attempt
(`Attribution`), what keeps its own volume bounded (`BoundedAggregation`), and
every field's JSON type, presence rule (`required`, with an explicit zero
value, or `conditional` on a stated, testable applicability), and closed
vocabulary where the field has one.

`internal/contextfabric/eventspec/certify` is the reusable JSON assertion
runner. It parses real `slog.JSONHandler` output only -- a recorder or capture
struct's rendering is refused, not silently accepted -- locates the line(s)
matching an event's `Msg`, enforces the declared `Multiplicity`, asserts the
declared `Level`, and asserts every field a caller names against the event's
declaration (closed-vocabulary membership included). A producer's test drives
its real production entry point through the service's configured tracer
wrapping a real `slog.NewJSONHandler`, exactly as an operator's own log
pipeline would see it, and hands the captured bytes to `certify.Parse` /
`certify.Certify`.

To add a new event: declare it as an `eventspec.Event` value in `spec.go`, add
it to `eventspec.All`, extend the generated-lookup mapping in
`generate.go`'s `goVarName`, and run
`go generate ./internal/contextfabric/eventspec/...` to refresh
`zz_generated.go` and `schema.json`. `regen_test.go` fails if the checked-in
generated files and the spec ever diverge -- there is no second, hand-typed
list of an event's fields anywhere else in the tree. A producer's own test
then drives its real entry point through a real JSON handler and certifies
the resulting line(s) against the declaration with `certify.Certify`.
