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
`resolveTimeContext` replaces `requireCurrentTimeAxis`), every
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

Codex round 1 on CHAOS-3781 changed four things worth knowing before
touching this area again:

- **Grain is per PROVIDER, not per answer.** `FactProviderResult.Grain`
  carries the precision each provider actually answered at; the registry
  keeps the COARSEST among providers that CONTRIBUTED facts
  (`coarsestGrain`), and only that reaches the label. The daily rollups
  report day; everything deriving from an immutable event timestamp
  reports instant. Assuming one grain for the whole answer under-reported
  Tier B precision -- a pull request merged at 14:00Z read as midnight.
- **A field with no history is DROPPED, not carried.** Historical incident
  facts omit `severity` entirely: it is revised in place, so the row holds
  only its current value, and a reason string on the answer does not undo
  a wrong VALUE on a specific fact. Status stays, because
  `started_at`/`resolved_at` are immutable.
- **Referenced stubs carry NO validity window.** A stub used to inherit
  the window of whatever relationship or episode mentioned it, so an
  unrelated record's interval excluded a real subject -- and projection
  ORDER decided which interval won. Only the authoritative entity write
  states validity; stubs assert identity and nothing canonical, matching
  CHAOS-3785's stub discipline.
- **Reuse keys from the WIRE request on BOTH sides.** `FindReusable` always
  did (tryReuse runs before Interpret); `Save` now does too, via an
  explicit parameter. Keying Save from the interpretation meant an
  interpreter that read a current-axis request as historical saved under a
  key no identical request could produce, so that whole class of question
  silently reused nothing. Interpretation identity is covered separately
  by condition 6's re-resolution.

Two smaller ones: a tolerated future instant is CLAMPED to now, not merely
accepted (it used to reach the predicates and the label), and a bounded
historical query returning zero rows reports `no_data` with an
out-of-retention reason rather than a clean `available` -- "nothing
happened then" and "we retain nothing that far back" are different answers.

Codex round 2 added one structural change worth knowing: the production
ClickHouse column snapshot now lives in ONE place,
`internal/contextfabric/devhealthschema`, and every parity guard and
fixture in both `devhealthsource` and `devhealthfacts` renders from it.

That consolidation is not tidiness. The UInt32 scan defect CHAOS-3789
fixed in `devhealthsource` survived in `devhealthfacts` reading the SAME
column, because each package hand-wrote fixtures that agreed with its own
reader's mistake -- a fixture cannot catch a fixture's own error. Two
artifacts rendered from one declaration cannot disagree. The declaration
also carries each table's production ENGINE, because the readers query the
ReplacingMergeTree tables with FINAL and FINAL against a plain MergeTree is
a query error, and column POSITION order, so a fixture is a positional
replica of the real table.

Extending that guard immediately found a second live instance the review
had not reported: `source_health` scanned `items_synced` (UInt32) and
`duration_ms` (UInt64) into `*int64`, which the driver also rejects. Both
are now converted in SQL with `toInt64(...)`, the convention every other
numeric projection in the package already used.

Two smaller round-2 rules: reuse keys from the PRE-CLAMP wire context on
both the lookup and the save side (keying the lookup off the clamped value
made it drift with `now` and disagree with what was saved), and temporal
grain is contributed by any provider whose FACTS WERE RETAINED, not only
by `SourceAvailable` ones -- both derived from `factsRetained` so the
retention and grain decisions cannot diverge again.

Round 3 corrected the round-1 reuse-key ruling. Both sides key on the
CLAMPED EFFECTIVE time context, not the wire one.

Round 1 keyed on the wire request, reasoning that identical wire requests
should key identically whatever their arrival time. That premise is false
once clamping is time-dependent: a request for as_of 12:00:30 arriving at
12:00:00 is clamped and answered for 12:00:00, while the SAME wire request
arriving at 12:00:45 is answered for 12:00:30 -- a different question. Wire
keying served the second the first's answer. The key now means what the
answer means, and round-2 F2's symmetry survives because BOTH sides moved
together.

The accepted cost is stated at `TimeAxisKeyFor`: a future-dated request
inside the skew tolerance keys per arrival, so that class never reuses and
each request writes its own row. It is near-empty in practice -- "now"
questions use axis=current, whose key is a fixed literal, and real
historical questions carry a past as_of that never clamps -- and those rows
are ordinary saved answers under normal retention and invalidation, so the
growth is bounded rather than accumulating.

## ANSWER SURFACE (CHAOS-3746)

`internal/contextfabric/answerprojection` derives the bounded consumer
projection (`context_fabric_answer_projection.v1`) of an investigation
result. It is the SINGLE choke point every bounded consumer sees an answer
through: the hosted API and the MCP sidecar both call `Project`, so neither
can grow its own summariser and drift.

- The package is import-pure by constraint: standard library and
  `internal/contracts/v1` only, no HTTP, MCP, storage, or database imports.
  `TestPackageImportsStayPure` enforces it rather than leaving it to review.
- The projection SELECTS and DROPS. It never rewrites, re-ranks, re-judges,
  or re-words. `direct_judgment`, `current_state`, and every retained driver
  standing and category are copied verbatim.
- Driver selection follows the engine's own standing field; canonical order
  is preserved within a standing. Withheld drivers never reach a consumer as
  part of the answer, and their absence is counted, not hidden.
- Every drop is declared in `projection_budget`, and `truncated` is true if
  and only if something was dropped. Silent truncation is a defect.
- A retained driver may never cite a claimed fact the projection dropped, so
  value-level evidence stays checkable by whoever received it.

`context_fabric_answer_projection.v1` is deliberately a self-contained
schema rather than a composition of `context_fabric_common.v1`, so an
agent-facing tool schema does not carry graph-projection vocabulary no
answer consumer uses. The resulting enum-drift risk is closed mechanically
by `TestAnswerProjectionVocabulariesMatchTheCanonicalOnes`.

The projection carries CHAOS-3781's temporal label verbatim, and never
budgets it away. It is the ONE canonical shape the projection reuses whole
rather than narrowing (`TemporalLabel` and `TimeContext` are embedded
byte-identically, which `TestAnswerProjectionReusedShapesMatchTheCanonicalOnes`
enforces). The reason is not symmetry: the projection carries no
interpretation, so before this a bounded consumer could not tell a March
answer from today's -- same drivers, same `current_state`, same judgment,
and the sidecar rendering printing a "Current state" heading over all of
it. That is the H6 defect AC-3781-2 closed on the canonical result,
reappearing on the only surface an agent reads. The result contract's
converse rule (a non-current axis REQUIRES a label) cannot be restated at
the projection, which has no axis to test against; it is closed
structurally instead, by `Project` copying from an already-valid result --
see `TestEveryHistoricalResultProjectsItsTemporalLabel`.

The retrieval-degradation limitation is DISPLACING, on both sides. At the
contract's limitation cap the engine drops the last model-authored caveat
rather than dropping the disclosure (a degraded answer would read as a
clean one) or failing the result (which is what a plain append did: one
entry over the cap, `ErrInvalidResult`, no answer at all -- and a degraded
retrieval is exactly the run that produces a long limitation list). The
projection does the same on the read side, because the engine appends the
disclosure last and the projection keeps a prefix: without a retention
priority, a legacy row written when the cap was 250 loses precisely that
entry, and only the bounded consumer is misled -- the canonical view still
carries it. Every displacement is counted -- on the RESULT, as
`limitations_displaced`, which the projection then adds into
`limitations_omitted`. The count cannot be re-derived downstream: a
displaced list and a list that simply had room are the same length and both
end with the disclosure, so the engine has to say so. The
strings and the two-spelling predicate live in `contracts/v1`
(`context_fabric_limitations.go`) because `answerprojection` may not import
the engine.

Result retrieval (`GET /api/v1/context-fabric/investigations/{result_id}`)
reads the same immutable store the engine writes through, surfaced as
`api.RuntimeDependencies.InvestigationResults`. Not-found is classified
through the port sentinel `ErrInvestigationResultNotFound`, which both store
adapters wrap; unknown, malformed, and cross-organization IDs are
indistinguishable on the wire so `result_id` can never act as a cross-tenant
existence oracle.
