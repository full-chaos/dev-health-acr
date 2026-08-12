# Context Fabric Reset 0–1 scaffold

This document maps the initial code scaffold to CHAOS-3744 and CHAOS-3745. It is intentionally structural: no graph vendor is selected, no public route is registered, and no question strings are hard-coded.

## Package map

```text
internal/contextfabric/
  model.go       internal draft shapes for requests, subjects, cohorts, paths,
                 drivers, facts, results, and projection batches
  ports.go       backend-neutral investigation, graph, canonical fact,
                 synthesis, result-store, projection, source, and checkpoint ports
  engine.go      consumer-neutral open-investigation orchestration
  projector.go   checkpoint-safe incremental projection worker

internal/api/
  context_fabric_routes.go
                 protected endpoint seam reserved for the Reset 1 engine;
                 intentionally unregistered until public contracts are accepted
```

## Planned public endpoint

```http
POST /api/v1/context-fabric/investigations
```

The endpoint will:

1. authenticate and derive the principal from ACR credentials or an accepted web assertion;
2. enforce the existing Context Fabric entitlement, read scope, client compatibility, request limits, timeout, and audit controls;
3. decode a strict versioned investigation request;
4. invoke the shared `contextfabric.Investigator`;
5. return a bounded, evidence-closed investigation result;
6. remain the source for later Workbench and MCP projections.

The scaffold creates the fully protected handler factory but does not add the route to `App.Handler`. Reset 0 must first publish:

- canonical Go DTOs under `internal/contracts/v1`;
- JSON Schemas;
- canonical OpenAPI JSON and generated YAML;
- golden examples and malformed fixtures;
- parity and compatibility tests;
- MCP projections where applicable.

## Reset 0 work remaining

### Contract bundle

Promote the accepted internal shapes into a versioned external contract bundle. Keep request and result consumer-neutral; do not encode Ask Dev UI concepts or a finite intent grammar.

### Canonical Ops adapters

Define typed, read-only operations for identity/membership, status/completion, work/blockers, PR/review/CI/deployment/incident evidence, metrics/comparisons, health/workload/investment/readiness/deficiencies, source health, and evidence expansion.

The engine selects these capabilities dynamically from the interpreted question. The adapter remains authoritative for values and formulas.

### Backend selection

Implement a bounded Go proof for the candidate backends against the same `ProjectionBackend` and `GraphReader` behavior:

- idempotent canonical projection;
- exact/alias/lexical/semantic resolution;
- bounded traversal and path reconstruction;
- subjectless cohort discovery;
- organization isolation and purge;
- temporal metadata, tombstones, rebuild, watermarks, latency, and failure behavior.

Select one backend without changing the public contract.

## Reset 1 implementation slices

### Projection and lifecycle

Implement a real `ProjectionSource`, `ProjectionBackend`, and `ProjectionCheckpointStore`. Hosting composition may run `ProjectionWorker` in a dedicated `acr-projector` binary or as an independently controlled runtime component. It must support cancellation, retries outside the domain worker, rebuild, source watermarks, and organization purge.

### Open investigation

Implement:

- `QuestionInterpreter` without exact-question matching;
- `GraphReader` for exact, alias, provider key, lexical, semantic, observation-to-entity, prior-result, cohort, lineage, temporal, document, and episode discovery;
- safe ambiguity and clarification behavior.

### Canonical fact planning and synthesis

Implement a `CanonicalFactReader` over bounded Dev Health services and an `AnswerSynthesizer` that produces direct judgments, pressures, driver standing, remaining work, readiness gaps, conflicts, limitations, and canonical evidence.

### Persistence and serving

Implement immutable result snapshots, register the investigation endpoint, add readiness/observability, and expose the same engine through API/MCP in the next Reset phase.

## First vertical slice

The bootstrap investigation is:

> What is the actual status of the Ask Dev project, and what are the current drivers?

It must prove the full real path, but it is not a question allowlist. Held-out paraphrases and novel combinations must run through the same engine without new question-text branches.

## Explicit non-goals of this scaffold

- choosing or importing a graph vendor;
- copying Python graph implementation code;
- registering an undocumented public endpoint;
- implementing Ask Dev UI integration;
- adding a fixed intent/plan registry;
- claiming the knowledge graph is operational before a real backend and canonical adapters exist.
