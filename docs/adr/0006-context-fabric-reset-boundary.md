# ADR 0006: Rebuild Context Fabric in ACR behind backend-neutral Go interfaces

- Status: Accepted
- Date: 2026-08-11
- Linear: CHAOS-3742, CHAOS-3744, CHAOS-3745

## Context

Context Fabric began as the ACR knowledge and evidence runtime. A later Python discovery implementation proved that Dev Health needs graph-assisted subject resolution, subjectless cohorts, relationship and lineage discovery, temporal context, driver discovery, and evidence closure. Productionizing that prototype inside Dev Health Ops created a second Context Fabric boundary and did not reliably answer the product's default questions.

The product requirement is already established. The remaining decision is where the permanent capability lives and how the graph/index backend is isolated.

## Decision

Context Fabric is rebuilt in `dev-health-acr` as one Go-owned, consumer-neutral open investigation runtime.

ACR owns:

- investigation and answer contracts;
- interpretation of open-ended natural-language engineering questions;
- graph/index projection and lifecycle;
- entity, alias, cohort, relationship, lineage, temporal, document, and episode discovery;
- dynamic selection of canonical facts required for each investigation;
- evidence-closed answer-capable results;
- shared API, Workbench, and MCP projections.

Dev Health Ops remains authoritative for canonical measurable facts and domain rules. ACR consumes those facts through typed read-only adapters and does not duplicate formulas.

The selected graph/index platform is hidden behind Go interfaces. Graphiti, FalkorDB, Zep, or another backend may satisfy those interfaces, but no vendor vocabulary is part of the consumer contract. No permanent Python graph sidecar is introduced.

## Open questions versus bounded facts

The runtime does not implement a supported-question registry. Reusable analytical capabilities may optimize interpretation and fact selection, but a question does not become unsupported merely because its wording or combination of facts was not pre-authored.

Facts, authorization, evidence, traversal, resource budgets, and assertion semantics are bounded. Natural-language questions are interpreted.

## Initial scaffold

Reset 0 introduces:

- internal investigation, result, projection, cohort, path, driver, coverage, and fact models;
- interfaces for interpretation, graph retrieval, canonical facts, synthesis, result snapshots, projection backends, projection sources, and checkpoints;
- a protected HTTP endpoint seam reserved at `POST /api/v1/context-fabric/investigations`;
- a checkpoint-safe projection worker skeleton.

The HTTP route is not registered until the versioned Go/OpenAPI/JSON-Schema contract bundle is accepted. This preserves the repository's contract-first rule.

## Consequences

Positive:

- one permanent Context Fabric implementation serves Workbench, API, MCP, and later Ask Dev;
- backend selection remains replaceable;
- existing ACR authentication, authorization, limits, audit, evidence, and runtime composition are reused;
- the Python prototype becomes historical product evidence rather than production architecture.

Costs:

- the selected backend and canonical Ops adapters must be implemented in Go;
- the repository's previous SVS-era prohibition on graph storage is superseded for this Reset program;
- API, MCP, and Workbench consumers must wait for the shared ACR result rather than creating local graph pipelines.
