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

- The SDK uses an injected HTTP client with a bounded request timeout.
- SDK retry attempts are bounded and permitted because projected writes use deterministic identities.
- Context cancellation propagates to the SDK.
- 404, authorization, and rate-limit failures are classified into bounded ACR errors; dependency response bodies are not exposed.
- Telemetry records operation, duration, status class, backend version, and watermarks without credentials, raw source bodies, or unrestricted graph payloads.

## Deployment topology

Zep is a service dependency of ACR. The deployment may be managed or in the FullChaos-controlled environment, but the chosen environment must provide:

- organization isolation and data residency appropriate to the deployment;
- deletion and organization purge APIs;
- documented availability, backup, recovery, and cost characteristics;
- private credential injection through existing secret management;
- Compose/Kubernetes configuration without a Dev Health-owned Python Graphiti sidecar.

The exact service URL and API key are runtime configuration. Local development may use an explicit insecure loopback/private endpoint only when the ACR environment permits it.

## Deletion, rebuild, and rollback

- Tombstones delete canonical nodes or edges by deterministic identity.
- Organization deletion purges only the server-derived organization graph.
- A rebuild purges the organization graph, resets the ACR checkpoint, and replays canonical projection batches.
- Rollback disables the Zep adapter at ACR composition and preserves the canonical source systems; consumers never depend directly on Zep state.

## Verification

The adapter must prove:

- exact/hybrid subject resolution and authorization filtering;
- aliases and prior names in the embedded surface;
- subjectless cohort retrieval;
- temporal triples, lineage, evidence closure, and driver candidates;
- idempotent projection, tombstones, purge, watermark, and rebuild boundaries;
- actual `zep-go` base URL, API-key, cancellation, and error-classification behavior;
- optional live-service projection and purge when test credentials are configured.

## Consequences

- Context Fabric has one permanent Go-owned graph integration boundary.
- Graphiti/Zep remains replaceable at the ACR port, but replacement is no longer part of Reset 0.
- ACR owns the operational dependency and must expose its readiness and degradation honestly.
- No Python graph runtime is authorized.
