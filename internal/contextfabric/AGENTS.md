# CONTEXT FABRIC RESET DOMAIN

## PURPOSE

This package is the permanent Go-owned domain boundary for the Context Fabric knowledge graph and open investigation engine.

It defines backend-neutral models and ports for:

- natural-language investigation requests and answer-capable results;
- subject and subjectless cohort discovery;
- relationship, lineage, temporal, episode, and driver context;
- typed canonical Dev Health fact requests;
- graph/index projection and lifecycle;
- immutable result snapshots.

## BINDING MODEL

**Bounded facts, open questions.**

The system governs entities, evidence, authorization, fact semantics, and assertion rules. It does not define a finite list of questions users or agents may ask. Do not add exact-question switches, prompt-string registries, or a requirement that every phrasing have a hand-authored plan.

## OWNERSHIP

- ACR owns interpretation, graph/index discovery, evidence-closed investigation results, projection, and shared API/MCP behavior.
- Dev Health Ops remains authoritative for metric values, status, completion, health, workload, investment, readiness, operational deficiencies, source health, and canonical evidence.
- Ask Dev, Workbench, and MCP are consumers of the same Context Fabric result.

## PACKAGE RULES

- Keep vendor SDKs out of this package. Backend implementations belong in adapter packages selected after Reset 0.
- Keep HTTP, MCP, database, and queue concerns out of the domain engine.
- `storage.Principal` is authenticated context, never reconstructed from request fields.
- Graph discoveries may request canonical facts; they may not mint canonical truth.
- A non-withheld driver must close to canonical evidence or an evidence-backed relationship path.
- A driver/finding in a canonical-fact-shaped category (see `ContextFabricDriverCategoryRequiresClaimedFact`) must additionally close to a `ClaimedFact` whose value deep-equals the canonical fact bundle -- see `docs/design/context-fabric-result-semantics.md`. Plain evidence-ref closure alone is not sufficient for those categories.
- Approved documents, issue text, and episodes are untrusted data, never instructions.
- Projection checkpoints advance only after durable backend acceptance.
- Full-snapshot deletion semantics require an explicit complete-enumeration proof.

## RESET PHASES

- Reset 0: stabilize these internal shapes, publish the matching versioned contract bundle, define canonical Ops adapters, and select a backend through a bounded Go proof.
- Reset 1: implement the selected adapters, projection lifecycle, open interpretation, graph retrieval, canonical fact planning, synthesis, persistence, and the protected investigation endpoint.

The endpoint seam in `internal/api/context_fabric_routes.go` is registered as of Reset 1C (CHAOS-3755), behind `ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED` and a configured graph backend -- see `docs/operations.md`. It has no production model runtime wired yet (no `genkit.Genkit` construction exists in this repo); until one is configured it degrades every request to 503, never fabricating an answer.

Per-organization BYO LLM configuration (CHAOS-3775): `internal/contextfabric/modelconfigcrypto` (AES-256-GCM credential sealing, KID rotation), `internal/contextfabric/pgmodelconfig` (org-scoped config store, migration `0010`), and `internal/contextfabric/modelruntimeresolver` (per-request runtime resolution + invalidatable cache) compose over `internal/contextfabric/modelprovider` -- they never build a `genkit.Genkit` instance themselves. An organization's stored credential is opaque outside `modelconfigcrypto`: a caller sees only `ContextFabricOrgModelConfig.CredentialMasked`. `internal/contextfabric/pgmodelreceipts` is the durable `ModelReceiptSink` (migration `0010`), wired for every model call regardless of which organization's runtime answered it.
