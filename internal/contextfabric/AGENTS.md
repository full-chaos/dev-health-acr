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

Canonical fact planning (CHAOS-3783): `planFactReads` (`fact_planner.go`) runs inside `FactCapabilityRegistry.ReadFacts`, after interpretation and before any provider is touched. It prunes a capability when NO resolved subject has a kind that capability declares in `SupportedSubjectKinds`, and narrows the subject list when only some do. The rule is a proof, not a heuristic -- such a capability could not have produced one admissible fact, which is why the measurement harness asserts the FACTS come out identical (coverage deliberately differs -- the pruned observations are the point -- and that delta is asserted separately, exactly). The mapping is provider-declared code; the interpretation contributes only a closed-enum fact kind, so a model cannot prune a provider by phrasing. Every decision is recorded in `Coverage` as a `SourcePruned` observation with a closed reason code; `pruned` rejects facts, is NOT a degradation (`factStateDegrades` returns false, so `Coverage.Partial` stays clean), and is not a provider-returnable state. This replaced a whole-bundle failure: `buildFactQuery` used to error on the first unsupported subject kind, which made "which projects are behind" unanswerable. There is deliberately no kill switch and no model-selected judgment-category gate -- see `docs/design/context-fabric-fact-planning.md` for both rejections.

Answer reuse (CHAOS-3782): `Engine.tryReuse` (`answer_reuse.go`) runs before `QuestionInterpreter.Interpret`, so a reuse hit costs zero model calls. It enforces TRD §19.7.3's six fail-closed conditions -- question hash/org/contract/projection/model-identity match and the staleness/rebuild-invalidation window are proved by `AnswerReuseGate.FindReusable` (`pginvestigation.Store`, migration `0011`); current authorization for every subject and evidence reference is rechecked separately, using only the existing `GraphReader.ResolveSubjects`/`DiscoverContext` -- no new port. `ReuseInvalidator` is the write-side hook `projectionrun.Coordinator` calls when a rebuild completes, and (CHAOS-3786) that `internal/api/context_fabric_model_config_routes.go`'s PUT/DELETE handlers now also call on every successful write, unconditionally. Reuse is disabled by default: an operator opts in by setting `ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE` (see that variable's D15 hazard note in `internal/config/config.go` and `docs/operations.md`).

Model-identity chain (CHAOS-3786): the recommended deployment configures a fallback model (`ACR_CONTEXT_FABRIC_MODEL_FALLBACK` / an org's BYO `FallbackModel`) that answers a large share of requests, not a rare edge case. `ReuseKey.ModelIdentities` therefore carries the org's CURRENT effective CHAIN (primary, then fallback if configured), and `FindReusable` matches chain MEMBERSHIP (`model_identity = ANY(...)`) rather than equality against a single value -- a saved result's `Versions.ModelIdentity` names the ONE model that actually produced it (`genkitruntime.mergeFallbackReceipt` carries the fallback leg's own identity on a successful fallback call), and it stays reusable only while that identity is still in the chain. Migration `0012` is a one-time cutover quarantining every organization's pre-CHAOS-3786 rows (saved under the primary's identity regardless of which model actually answered) via the same epoch mechanism a rebuild uses.
