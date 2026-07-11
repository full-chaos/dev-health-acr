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

## Metric mapping

The hosting telemetry backend should derive these metrics from snapshots:

| Metric | Type | Labels |
| --- | --- | --- |
| `acr_observations_total` | counter | `kind`, `operation`, `http_status_class`, `outcome`, `packet_status`, `compatibility`, `source_coverage`, `store_query_class`, `source_fallback`, `query_version`, `ranking_version`, `denial_class` |
| `acr_observation_duration_seconds` | histogram | same bounded labels as `acr_observations_total` |
| `acr_packet_size_bytes` | histogram | `packet_status` |
| `acr_packet_tokens` | histogram | `packet_status` |
| `acr_packet_items` | histogram | `packet_status` |
| `acr_packet_empty_total` | counter | none; derived from `packet_status=empty` |
| `acr_packet_source_state_total` | counter | `packet_status`, `source_coverage`; values come from bounded stale/unavailable counts |
| `acr_packet_version_mismatch_total` | counter | `compatibility` |
| `acr_store_query_timeout_total` | counter | `store_query_class`, `store_backend` |
| `acr_episode_outcomes_total` | counter | `episode_outcome`, `audit_delivery` |

`request_id` is a correlation field only: it MUST NOT be a metric label. Missing
dimensions use `unknown`; no backend-specific dynamic label may be added.
Per-organization and per-credential request/resource totals come from the bounded
`internal/limits.Manager.Usage` interface and are not emitted as metric labels.

## SLOs and alerts

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

CHAOS-2907 HTTP metric export is intentionally deferred. This package supplies
bounded snapshots and a standard-library `SlogSink`; an HTTP/Prometheus/OpenTelemetry
exporter must be introduced through the owning service integration with its own
availability, tenancy, and cardinality review.

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
A deployed seeded HTTP packet route remains blocked on CHAOS-2907 and is not
claimed by this change.
