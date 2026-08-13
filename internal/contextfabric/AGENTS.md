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

Vector and semantic retrieval (CHAOS-3778): `internal/contextfabric/embedprovider` is the OpenAI-compatible embedder behind the ACR-owned `Embedder` port in `ports.go` (`GraphReader` is unchanged -- that is what TRD 19.4's "no port change expected" meant). Embeddings are written at projection time by `falkorgraph`'s `embedProjectionBatch`, over the SAME `search_text` property the lexical index covers, so the two retrieval paths differ only in MECHANISM.

Three constants carry the acceptance bars, and none of them may be moved casually:

- `falkorgraph.vectorRelevanceCeiling` (0.70) sits strictly below `graphrank.CorroboratedFloor` (0.72, the shipped lone-candidate gate). That is what makes AC-3778-3 -- "a vector hit alone never commits a subject" -- true by ARITHMETIC rather than by a rule. Raising it repeals the acceptance bar.
- The configured similarity floor (tau) is the AC-3778-4 honest-no-match guard. A k-NN query always returns k rows; the floor is the only thing between that and a confident wrong subject.
- `graphrank.CorroboratedCeiling` (0.86) stays below the 0.88 top-of-two gate, so two corroborated candidates still clarify.

`embedprovider` verifies the `model` field of every embeddings RESPONSE against the configured model and fails closed on mismatch, including when the field is absent. This is not defensive polish: LM Studio with several embedding models loaded silently ignores the request's `model` and serves another, and the dimension check only catches that when the widths differ -- two same-width models would produce silent mixed-vector corruption stamped with the identity of the model that was asked for. Normalization is exactly trim + ASCII case-fold; anything else is a mismatch (`ExpectResponseModel` retargets the comparison, it cannot weaken it).

FalkorDB's vector score is a cosine DISTANCE (0 = identical), verified live. It must never reach `graphrank.ResultConfidence` -- see the D11-class regression in `graphrank/vector_ladder_regression_test.go`. Embeddings are projection artifacts, so the existing epoch/rebuild machinery covers them; a dimension change disables vector retrieval for that organization until `acr-projector rebuild --org` runs, and a stale-dimension vector is never queried.

Historical time axis (CHAOS-3781): the H6 refusal of every non-current axis
is GONE from all three layers it lived in -- `Engine` (`temporal.go`'s
`validateTimeContext` replaces `requireCurrentTimeAxis`), every
`devhealthfacts` provider (`timebound.go` replaces `checkCurrentTimeOnly`),
and `internal/api/context_fabric_routes.go`. AC-3781-6 required that in one
change: a layer left refusing would either contradict the others or answer
with current data under a historical label.

What replaced it is narrower, not equivalent. `ErrInvalidTimeBound` refuses
only bounds this service will not read -- a time in the future (the axis is
historical, not speculative) and a range wider than 400 days. A time
EARLIER than any retained data is not an error: "we have nothing that far
back" is a real answer.

Three things make a historical answer honest rather than merely possible:

- `devhealthsource` now emits `ValidFrom`/`ValidTo` from each source row's
  own immutable interval columns (`validity.go`), and `falkorgraph` admits
  by that window on the `_ns` int64 properties only (`temporal.go`,
  AC-3781-7). Half-open `[valid_from, valid_to)`; a point-in-time request
  is the degenerate interval `[T, T]`, so one predicate serves both axes.
  An element with NO window is admitted at every requested time AND
  counted, disclosed as the `context-fabric:graph-validity-windows`
  coverage source -- excluding it would empty a pre-rebuild graph;
  admitting it silently would be the H6 defect. This is why the source
  version bumped v3 → v4 and why a rebuild is required (docs/operations.md).
- `devhealthfacts` providers split three ways, and the split is per
  provider, not per table: Tier A rollups bound `day`/`window_end`/
  `computed_at`; Tier B derives state from immutable interval columns (PR
  merged/closed, incident resolved, run finished, item completed) and gates
  existence; Tier C has no recorded history and returns `not_applicable`.
  `observed_time` degrades EVERYWHERE, Tier A included -- `computed_at` is
  a recompute stamp and the entity tables are `ReplacingMergeTree`, so no
  observation history exists to query (drift item D15 as a hard limit).
- `Engine` composes `ContextFabricTemporalLabel` itself, never a
  synthesizer: what time an answer covers is a fact about which reads ran,
  not something a model may assert. Effective time only ever narrows, and
  the result contract refuses a non-current axis carrying no label.

`ReuseKey` gained a fifth dimension, `TimeAxisKey` (migration `0013`).
Without it the same question text at two as-of times shares one key and a
June answer is served for a March question. The current axis maps to a
FIXED literal -- a wall-clock-derived key would make every current-axis key
unique and silently drop the reuse rate to zero while CHAOS-3782's own
tests kept passing. Conditions 1-7 are otherwise unchanged: a historical
answer is NOT safely cacheable for longer, because a backfill rewrites past
days (D15).
