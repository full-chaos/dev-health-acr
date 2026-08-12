# ADR 0007: Use Graphiti/Zep through the official Go SDK behind ACR graph ports

- Status: Accepted
- Date: 2026-08-11
- Decision owners: Context Fabric / ACR
- Implements: CHAOS-3752

## Context

Context Fabric requires temporal entity and relationship storage, hybrid semantic retrieval, subject and cohort discovery, lineage, episodic context, deletion, rebuild, and bounded graph lifecycle behavior. The failed Python/Ops implementation proved that the graph capability is needed but also proved that production ownership belongs permanently in ACR.

The selected integration must not reintroduce a Python sidecar or expose graph-native contracts to Workbench, MCP, agents, or Ask Dev.

## Decision

ACR will use the Graphiti/Zep graph service through the official Go SDK:

```text
github.com/getzep/zep-go/v3 v3.22.0
```

The SDK is confined to `internal/contextfabric/zepgraph`. ACR-owned interfaces remain the stable boundary:

- `ProjectionBackend` for canonical projection and lifecycle writes;
- `GraphReader` for subject, cohort, relationship, temporal, and driver-context discovery;
- ACR-owned readiness, watermark, purge, and rebuild composition;
- consumer-neutral Context Fabric request and result contracts.

No `zep-go` request or response type is allowed in public Context Fabric contracts.

## Graph identity and tenancy

- ACR derives one opaque graph ID per authenticated organization from a server-owned prefix and SHA-256 digest of the organization ID.
- The caller cannot supply a graph ID.
- Canonical node and relationship UUIDs are deterministic over organization identity plus canonical Dev Health identity.
- Authorization attributes are projected with each node and edge and are filtered before a candidate or path can enter an investigation result.
- Repository, project, and team scopes never widen merely because the graph returns a result.

## Projection semantics

- Canonical entities are projected with labels, aliases, previous names, provider IDs, selected scalar properties, canonical evidence references, source version, observed time, and valid-time bounds.
- Aliases and previous names are included in the embedded text surface so semantic resolution is not limited to canonical labels.
- Canonical relationships use caller-owned UUIDs and explicit `valid_at`, `invalid_at`, and `expired_at` fields where applicable.
- Approved untrusted documents and episodes are indexed for retrieval but remain explicitly marked as untrusted content.
- Graphiti/Zep episode UUIDs are backend-native provenance and never become canonical Dev Health evidence identifiers.
- Projection batches are idempotent through deterministic identities. Checkpoints advance only after a durable backend receipt.

## Retrieval semantics

- Exact canonical hints are resolved through deterministic node identity before semantic retrieval.
- Remaining subject terms use bounded hybrid node search.
- Open investigations can retrieve nodes, relationships, episodes, observations, and auto-selected context through the same organization graph.
- Subjectless team or project cohorts are discovered from the interpreted question shape, not from an exact question allowlist.
- Graph associations are candidates and context. Canonical Dev Health services remain authoritative for measurements, status, completion, health, workload, investment, readiness, staffing qualification, and source health.
- A relationship or driver cannot enter the public result without canonical evidence references projected by ACR.

## Failure and network behavior

- The SDK uses an injected HTTP client with a bounded request timeout
  (proved: `TestSDKAPIUsesPinnedClientBaseURLAuthenticationAndSafeRateLimitClassification`).
- SDK retry attempts are bounded and are safe to permit because projected
  writes use deterministic identities — **with one confirmed, pinned-version
  caveat**: `github.com/getzep/zep-go/v3@v3.22.0`'s internal retrier
  (`internal/caller.go`, `internal/retrier.go`) re-issues the *same*
  `*http.Request` object across attempts and never rewinds `Request.Body`
  between them. For the bodyless read path (`GetNode`, `GetGraph`,
  `GetNodeEdges`) this is harmless and bounded retry was reproduced working
  (`TestSDKAPIGetCallsRetryBoundedAttemptsOnServerErrors`). For body-bearing
  calls — `Search`, `AddFactTriple`, `CreateGraph` — a retried attempt races
  Go's `net/http` transport: it was reproduced both succeeding and failing
  client-side with `http: ContentLength=N with Body length 0` across
  otherwise-identical local runs (`TestSDKAPIBodyBearingCallsDegradeSafelyUnderBoundedRetry`).
  ACR still degrades safely either way — no panic, no leaked dependency body
  — but **retries must not be relied on to make a transient 5xx/429 on a
  write or search call recoverable** until the SDK is upgraded past this
  version or ACR adds its own call-level retry (a fresh top-level SDK method
  call, not the SDK's internal one, builds a fresh request and does not hit
  this race).
- Context cancellation propagates to the SDK (proved:
  `TestSDKAPIPropagatesContextCancellation`).
- 404, authorization, and rate-limit failures are classified into bounded
  ACR errors (`ErrNotFound`, `ErrUnauthorized`, `ErrRateLimited`); dependency
  response bodies are never included in the returned error text (proved:
  `TestZepStatusCodeClassifiesTypedSDKErrors`,
  `TestSafeDependencyErrorClassifiesAndHidesDependencyBodies`).
- Telemetry records operation, duration, status class, backend version, and watermarks without credentials, raw source bodies, or unrestricted graph payloads.

## Deployment topology

**Zep v3 is a Zep Cloud-only API; there is no supportable self-hosted server
image for it.** This was researched directly (not assumed) as part of
CHAOS-3752:

- Zep discontinued Zep Community Edition (the former self-hosted server).
  Its own announcement states: "we've decided to stop maintaining and
  releasing Zep Community Edition. The existing repository will remain open
  under the Apache 2.0 license, but we will no longer provide updates or
  active support." The `getzep/zep` repository documents the same outcome
  in-repo: "Zep Community Edition is no longer supported. Its code has been
  moved to the `legacy/` folder."
- The only remaining open-source artifact is Graphiti (`getzep/graphiti`),
  a Python temporal-graph library, plus a community `zepai/graphiti` Docker
  image. That image speaks Graphiti's own API against a caller-provided
  Neo4j instance — a different wire protocol from the `/api/v2/graph/*`
  surface `github.com/getzep/zep-go/v3` calls — and was, at last check,
  several minor releases behind current `graphiti-core` (image pinned near
  v0.10 against a v0.22 library). Standing it up would also mean ACR either
  running a Python HTTP service or embedding the Graphiti library outside
  Go, both of which are explicit non-goals of CHAOS-3752 and of
  `internal/contextfabric/AGENTS.md`.
- Zep Cloud, reached at `https://api.getzep.com`, is therefore the only
  backend `zep-go/v3` can talk to. This is an **external credential
  blocker**, not a configuration gap: a Zep Cloud account and API key are
  required, and neither exists in this repository's CI or local dev
  environment today. `internal/contextfabric/zepgraph.TestLiveZepContextFabricLifecycle`
  documents and enforces this — it is env-gated and skips without
  `ACR_TEST_ZEP_BASE_URL` / `ACR_TEST_ZEP_API_KEY`, and it has not been run
  against a live endpoint as part of this change. See "What CHAOS-3752 did
  not resolve" below for exactly what is needed to unblock it.

Because there is no self-hostable image, there is nothing to add to
`deploy/compose/acr.compose.yml` or the Helm chart as a *service* the way
Postgres or ClickHouse are added — Zep Cloud is reached over the network
exactly like the Dev Health Ops entitlement origin already is, and (like
that origin in local development) it is simply left unconfigured today
rather than wired to a value nothing can supply.

What CHAOS-3752 *does* establish is the runtime configuration contract the
adapter itself understands, in
`internal/contextfabric/zepgraph.ConfigFromEnv`:

| Environment variable | Purpose | Convention |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_ZEP_BASE_URL` | Zep API origin, e.g. `https://api.getzep.com/api/v2` | plain value |
| `ACR_CONTEXT_FABRIC_ZEP_API_KEY` | Zep Cloud API key | `KEY` or `KEY_FILE`, same convention as `internal/config.SecretValue` |
| `ACR_CONTEXT_FABRIC_ZEP_GRAPH_PREFIX` | Server-owned graph ID prefix (default `acr-cf`) | plain value |
| `ACR_CONTEXT_FABRIC_ZEP_REQUEST_TIMEOUT` | Per-request timeout (default `30s`) | Go duration |
| `ACR_CONTEXT_FABRIC_ZEP_MAX_ATTEMPTS` | Bounded SDK attempts, 1-5 (default `3`) | integer |
| `ACR_CONTEXT_FABRIC_ZEP_MAX_RESULTS` | Bounded search page size, 1-50 (default `25`) | integer |
| `ACR_CONTEXT_FABRIC_ZEP_ALLOW_INSECURE` | Permit a non-HTTPS base URL (local loopback only) | boolean |

`zepgraph.Configured` reports whether `ACR_CONTEXT_FABRIC_ZEP_BASE_URL` is
set at all, so a deployment that has not opted into Context Fabric never
constructs the adapter and never fails closed over a dependency it did not
choose. **Nothing in `cmd/acr-api` or `internal/api` reads these variables
yet** — Context Fabric's hosted composition and its `RuntimeDependencies`
wiring are Reset 1 scope (CHAOS-3753/3754/3755), and the endpoint stays
unregistered per `internal/contextfabric/AGENTS.md` until Reset 1 publishes
the public contract. This table is the agreed contract Reset 1 wires
against; Compose and Helm should add the matching secret/env plumbing in
the same change that wires `zepgraph.ConfigFromEnv` into the hosted runtime
bundle, once a Zep Cloud credential exists to inject.

The chosen environment, whenever it is provisioned, must still provide:

- organization isolation and data residency appropriate to Zep Cloud's
  hosting region for this workload;
- deletion and organization purge APIs (already proven against a fake
  transport; live-proof is the credential-blocked step above);
- documented availability, backup, recovery, and cost characteristics for
  the selected Zep Cloud plan;
- private credential injection through existing secret management
  (`ACR_CONTEXT_FABRIC_ZEP_API_KEY_FILE`, matching every other ACR secret);
- no Dev Health-owned Python Graphiti sidecar, satisfied by construction
  since there is no Python code in this repository's Zep integration.

Local development may use an explicit insecure loopback/private endpoint
only when the ACR environment permits it (`ACR_CONTEXT_FABRIC_ZEP_ALLOW_INSECURE=true`);
this exists for point-in-time local testing against a caller-run proxy or
mock, not as a path around the Zep Cloud requirement above.

## Deletion, rebuild, and rollback

- Tombstones delete canonical nodes or edges by deterministic identity.
- Organization deletion purges only the server-derived organization graph.
- A rebuild purges the organization graph, resets the ACR checkpoint, and replays canonical projection batches.
- Rollback disables the Zep adapter at ACR composition and preserves the canonical source systems; consumers never depend directly on Zep state.

## Verification

The adapter proves, against a fake transport (`internal/contextfabric/zepgraph/adapter_test.go`, `config_test.go`):

- exact/hybrid subject resolution and authorization filtering, including a
  safe no-match result and a safe ambiguous/clarification result;
- aliases and prior names in the embedded surface, without a later
  relationship/content/episode upsert erasing that canonical entity metadata
  across separate projection batches (`mergedSubjectAttributes` in
  `projection.go`);
- subjectless cohort retrieval;
- temporal triples, evidence closure, and driver candidates;
- idempotent projection replay, tombstones across every kind (relationship,
  document, episode, generic subject) with idempotent re-application, purge,
  and watermark read/not-found boundaries;
- server-derived graph identity and organization isolation, including that
  purging one organization's graph cannot touch another's;
- actual `zep-go` base URL, API-key, bounded-retry (with the pinned-version
  caveat above), cancellation, and error-classification behavior against a
  real `httptest` HTTP server.
- Rebuild (purge + checkpoint reset + replay) is composed from
  `ProjectionBackend.PurgeOrganization` plus the caller's own
  `ProjectionSource`/`ProjectionCheckpointStore` (`internal/contextfabric/ports.go`);
  it is Reset 1 (CHAOS-3753) worker-orchestration scope, not a zepgraph method.

`internal/contextfabric/zepgraph.TestLiveZepContextFabricLifecycle` proves
the same lifecycle end-to-end against a real Zep endpoint — isolated graph
creation, projection of every kind, idempotent replay, retrieval with
temporal/evidence metadata, tombstone, watermark read, purge (including
idempotent re-purge), and cross-organization isolation — but is env-gated
on `ACR_TEST_ZEP_BASE_URL` / `ACR_TEST_ZEP_API_KEY` and has **not been run**
as part of CHAOS-3752: no Zep Cloud account/API key was available in this
environment. Run it with:

```bash
ACR_TEST_ZEP_BASE_URL=https://api.getzep.com/api/v2 \
ACR_TEST_ZEP_API_KEY=<zep-cloud-api-key> \
  go test -count=1 -run TestLiveZep ./internal/contextfabric/zepgraph -v
```

## Consequences

- Context Fabric has one permanent Go-owned graph integration boundary.
- Graphiti/Zep remains replaceable at the ACR port, but replacement is no longer part of Reset 0.
- ACR owns the operational dependency and must expose its readiness and degradation honestly.
- No Python graph runtime is authorized.
- Zep v3 is Zep Cloud only. A Zep Cloud account and API key are an accepted,
  externally-owned operational dependency of Context Fabric, not something
  this repository can provision. Reset 1 must not silently assume the live
  contract test has been run; CI keeps it skipped until credentials exist.
