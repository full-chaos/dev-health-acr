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
- `operation` and HTTP status class: finite endpoint families and `2xx`/`4xx`/`5xx`.
  Operations: `health`, `readiness`, `capabilities`, `context`, `evidence`,
  `snapshot`, `episode`, `investigation`, `investigation_result`,
  `submission_status`, `submission_attach` and `submission_cancel` (a leased
  run's attach and cancel, CHAOS-11949), `threads` (the Ask Dev conversation
  list), `thread` (one conversation's read or delete), `result_feedback` (the
  Ask Dev answer feedback read or set, CHAOS-11950), `data_catalog`,
  `data_subjects`, `data_facts`,
  `data_relationships`, `data_operations`, `data_graphql`, or `unknown`
- `outcome`: `success`, `failure`, `denied`, `canceled`, or `unknown`
- packet: lifecycle `status`, bytes, tokens, schema/baseline versions,
  compatibility, and source coverage
- store query class: `packet`, `evidence`, `episode`, or `unknown`
- store source: one fixed versioned catalog source ID or `unknown`
- store phase: `query`, `scan`, `iteration`, `close`, or `unknown`
- `source_fallback`: `none`, `catalog`, `unavailable`, or `unknown`
- query/ranking versions: canonical `context-query.v1`, `ranker.v11`, or `unknown`
- denial class: `authentication`, `organization_scope`, `repository_scope`,
  `license`, `rate_limit`, `none`, or `unknown`. `rate_limit` covers the
  request rate limit (`rate_limited`) and the shared Ask Dev admission
  refusals (`concurrency_limited`, `allowance_exhausted`).

The Ask Dev conversation routes (CHAOS-11948) write one Info line per request,
`context fabric thread`: `route` (`list`, `get`, `delete`), a closed `outcome`
(`listed`, `read`, `closed`, `not_open`, `not_found`, `invalid_request`,
`store_unavailable`, `gate_unavailable`, `invalid_record`, `owner_unknown`), and
on a read the aggregated serving decision of its done turns (`turns`,
`done_turns`, `served_turns`, `withheld_denied`, `withheld_not_found`,
`withheld_tuple`, `withheld_flood`, `earlier_turns`). A thread read writes no
`StoredResultAuthorization` line per turn: that event is one line per request.
A turn naming a deleted, expired or full conversation is refused on the
admission line with outcome `refused_thread_closed` and a
`thread_closed_reason`.

The Ask Dev answer feedback route (CHAOS-11950) writes one line per request,
`context fabric result feedback`: `route` (`get`, `set`) and a closed `outcome`
(`read`, `set`, `invalid_request`, `not_served`, `not_owned`,
`gate_unavailable`, `not_found`, `store_unavailable`, `invalid_record`); a
`not_served` line names the read by id's `serving` outcome. It carries no flag
value and no result id. The read by id's `stored result authorization` line is
written too, with surface `feedback`.

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
| `acr_answer_reuse_total` | counter | `outcome` | acr-api answer reuse outcome line. A `miss_authorization` line also carries `reason` (`gate_denied`, `submission_owner_missing`, `submission_owner_mismatch`, `subject_cap_exceeded`, `subject_recheck_failed`, `subject_not_committed`) and, for `subject_cap_exceeded`, `subject_count`. Both are log and event fields only; the counter keeps `outcome` as its one label. `unattributed` can appear in the log only as a fallback when a refusal site named no reason; it is not in the declared vocabulary, so certification refuses such a line. A line whose recheck reached the graph also carries `recheck_ms` (first graph read to return) and `recheck_subjects`, on any outcome: log and event fields only, never a metric label. `miss_recheck_deadline` is the outcome of a recheck that spent its own time budget: nothing was proven either way, it says nothing about authorization, and the turn runs fresh on the request's deadline. |
| `acr_requirement_outcomes_total` | counter | `outcome` | acr-api completeness authority line (one count per outcome row; an investigation without a plan adds no rows) |
| `acr_outcome_rows_evicted_total` | counter | `lever` (`claim_depth`, `fact_row`, `path_drop`, `candidate`) | acr-api lever Info lines (`context fabric claim depth narrowing`, `fact row truncation`, `path drop`, plan narrowing): one count per served lever application whose answer carries the outcome-row eviction disclosure (field `outcome_rows_evicted`, the cumulative count) |
| `acr_limitation_disclosure_skipped_total` | counter | none | acr-api WARN line `context fabric limitation disclosure skipped` (recogniser id and list length only): a service disclosure was left out of a served answer because the limitation list was full of service-looking entries; expected 0 on prod, since the service strips such entries from model drafts |
| `acr_query_embed_fallbacks_total` | counter | `cause` (`timeout`, `error`, `other`) | acr-api WARN line `context_fabric: query embed failed, vector retrieval fell back to lexical for a request` (organization id and cause only; no question text): one count per question whose query embed failed or timed out, so semantic retrieval fell back to lexical; the cause is `timeout` for a deadline or transport timeout and `error` otherwise; `other` is the counter's defensive bucket for a cause outside that closed set (not produced today) |
| `acr_projection_pair_failures_total` | counter | `failure_class` (`canceled`, `checkpoint_conflict`, `organization_locked`, `rebuild_required`, `dependency_rate_limited`, `query_budget_exceeded`, `dependency_unavailable`, `invalid_result`, `unclassified`, `other`) | `acr-projector serve` ERROR line `context_fabric: projection tick failed; checkpoint held for replay` (class only, no error text): one count per (organization, source) tick outcome that ended in an error, including a cancelled tick; a successful tick counts nothing; a projection worker that fails to construct is logged but not counted; `other` is the counter's defensive bucket for a class outside that closed set (not produced today) |
| `acr_graph_bootstrap_waits_total` | counter | `outcome` (`bootstrapped`, `took_over`, `other`) | acr-api and `acr-projector serve`, at the graph key's bootstrap gate: one count per writer that found the gate held by another writer and blocked on it (a writer that finds the gate free, including the one that runs the bootstrap, counts nothing); `bootstrapped` is a wait that ended with the key already bootstrapped, `took_over` a wait that ended after the holder failed so the waiter ran the bootstrap itself; `other` is the defensive bucket (not produced today) |
| `acr_graph_bootstrap_wait_seconds` | histogram | `outcome` (`bootstrapped`, `took_over`, `other`) | same site as `acr_graph_bootstrap_waits_total`: seconds the waiter spent blocked on the gate. A waiter cannot time out and a cancelled context does not end the wait today (the gate is an uncancellable mutex), so this histogram is unbounded above by design: a stuck bootstrap shows as waits that never record, then one very large sample when it ends |
| `acr_fact_read_aborts_total` | counter | `cause` | acr-api investigation failure line, fact read abort |
| `acr_clickhouse_statement_near_memory_cap_total` | counter | none | acr-api and `acr-projector serve` WARN line (acr-projector rebuild and rollback log only, no counter) `clickhouse statement memory near cap` (statement hash, first words, peak and cap bytes; a statement the server refused with a memory-limit exception counts too, with the cap as its peak and `peak_is_lower_bound=true`) |
| `acr_clickhouse_statement_peak_unobserved_total` | counter | none | acr-api and `acr-projector serve` WARN line (acr-projector rebuild and rollback log only, no counter) `clickhouse statement sent no peak memory value` (one per statement hash per process; the counter counts every statement) |
| `acr_investigation_latency_seconds` | histogram | `status` (`error` when no answer was delivered, including a 413 from a response-size gate and an event stream that ends with `error{budget_refusal}` or no terminal) | acr-api investigation route |
| `acr_investigation_events_dropped_total` | counter | `stage` (`interpreting`, `resolving_subjects`, `reading_facts`, `synthesizing`, `validating`, `other`) | acr-api investigation event stream (CHAOS-11910): one count per stage transition the request's sink did not send because its buffer was full, so the transition never reached the stream; the route records the counts after the run. The engine never waits on the stream writer; a production sink holds 32 transitions and a run makes at most 10, so the expected value is 0 and any count is a defect signal; `other` is the defensive bucket (not produced) |
| `acr_entitlement_credential_total` | counter | `class` (`service_token`, `workload`, `other`), `outcome` (`sent`, `token_unavailable`, `other`) | acr-api (CHAOS-11203; `acr-projector purge` sends the same credential but records no counter): one count per call of the Dev Health entitlement client (health, entitlement and organization-unknown reads, llm-config, principal access) by the credential `ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL` selects. `token_unavailable` is a workload call that was not sent because no token could be minted; the service token is never a fallback. `credential=service_token` reaching zero is the signal that `svc_acr_` use has ended |

The investigation failure line of a budget refusal (the 413 above) carries what
was measured, for diagnosis without the response body: `budget_overrun` (`items`
or `bytes`), `budget_measured_bytes`, `budget_max_serialized_bytes`,
`budget_measured_items`, `budget_max_items`, `budget_nothing_left_to_cut`,
`budget_retry_attempted`, `budget_question_family` and `budget_sections`. The
last lists every top-level answer section holding at least 5% of the measured
bytes as `name=bytes`, largest first; section names are the answer contract's
own keys, never content.

A caller's `options.max_serialized_bytes` is the hard bound on the body served;
`options.answer_budget_bytes` (optional, default the same value) is the target the
engine sheds the canonical result toward. A result over the body bound is not a
refusal: the service ceiling (`ACR_MAX_SERIALIZED_BYTES`) is the only 413 line.
The investigation route then delivers the bounded projection sized to the option
(`full_result_omitted` true, `result_id` naming the stored result). That turn's
response-budget measured line adds `projected` (true when the projection was
served), `served_bytes` and `caller_max_serialized_bytes`; `measured_bytes` stays
the canonical result's size. A client synthesis turn is never projected, so its
input can ride the body and is outside the option by contract (the option
bounds the result; the input is bounded by the service ceiling), and a result over
the option on such a turn is refused with the option named.

When the engine could not reach the caller's byte target and fitted the answer against
the service ceiling instead of refusing, the plan-narrowing line carries
`ceiling_fit` (`fits_as_measured`, `row_truncation`, `path_drop`, `walk_list_cut` or `decisive_fit`) beside the
document's `measured_bytes` and `measured_items` and `max_serialized_bytes` set to
the ceiling it was fitted to.

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
A user's credentials count as one credential here: the user on one surface, so a
refresh or a new Connect neither starts a new quota nor takes a new tracked
subject. Workload, org-bearer and operator credentials count one by one, a
workload by its binding. A subject with no running request and no request for
the retention leaves the tracked set once the set is full, and its totals go with
it.

## What leaves a span

Exported spans pass through one scrub in `internal/otelexport`: only the attribute keys of `otelexport.SafeSpanKey` leave the process, each with its one value type (no server host or port, no client address, no raw path), string values are redacted for credential shapes and capped, the trace state is cleared (ids are kept), the span name is `METHOD <route template>`, `HTTP METHOD` or a fixed internal name, and events keep only the error type, links keep no attributes, and a status keeps its code without text. Resource attributes are redacted for credential shapes and credential-named keys.

## Emit-path census

`internal/cigates/emitscan` reads the Go source of `cmd` and `internal` on every run and fails the build on a protected or network key in a log line, span attribute or metric label, error text as a log or attribute value, a raw request path or query in a log attribute, a log key that is not a constant (pinned per file in `pins.tsv`), an slog handler built outside `telemetryscrub`, and an unbounded metric label. `labels.tsv` pins the label keys of every metric call site, `spans.tsv` pins the span attribute keys (each must satisfy `otelexport.SafeSpanKey`), and `pins.tsv` holds the reviewed exceptions with a reason; a stale row fails too. A package whose scan exhausts its work budget is a finding, never a pass.

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
dimensions. `context-query.v1` and `ranker.v11` are aliases of the canonical
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
