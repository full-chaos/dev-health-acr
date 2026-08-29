# Context Fabric investigation result semantics (CHAOS-3755, Reset 1C)

Short reference note. Covers: the four kinds of grounding an investigation
result can carry, how each is checked (or isn't), where each lives in the
`context_fabric_investigation_result.v1` wire shape, and the honest limits
of the value-level evidence closure this ticket added.

## 1. The four-way distinction

Every claim inside an `InvestigationResult` falls into exactly one of these
categories. A driver, finding, or piece of narrative prose that blurs them
-- presenting an inference as if it were observed, or a graph association as
if it were a canonical measurement -- is the failure mode this document
exists to prevent.

| Kind | What it means | Where it lives on the wire | How closure is enforced |
| --- | --- | --- | --- |
| **Canonical observation** | A value ACR read directly from an authoritative Dev Health fact provider (`internal/contextfabric/devhealthfacts`, `FactCapabilityRegistry`) and the model restated verbatim. | `ContextFabricClaimedFact` entries (`result.claimed_facts`), referenced by `driver.claimed_fact_ids` / `finding.claimed_fact_ids`. | **Deterministic, code-only, value-level.** `SynthesisDraft.ValidateAgainst` (`internal/contextfabric/model_runtime.go`) deep-equals every claimed `(kind, subject, field, value)` against the canonical fact bundle the synthesizer actually received. A mismatch, or a claim for a field/kind never observed, rejects the whole draft (`ErrModelOutput` + `ErrSynthesisRejected` -> HTTP 422 `synthesis_rejected`, CHAOS-3784). See §2. |
| **Graph association** | A relationship the graph backend discovered between subjects (an edge, a path) -- evidence that two things are connected, not a measurement of either one. | `ContextFabricRelationshipPath` entries (`result.paths`), referenced by `driver.path_ids`. | **Structural only.** `ValidateAgainst` checks a cited `path_id` exists in the supplied graph context. It does not, and cannot, verify the association is semantically correct -- that is `internal/contextfabric/falkorgraph`'s and CHAOS-3754's responsibility, proved by their own test suites. |
| **Source assertion** | A document, episode, or other free-text artifact says something, without ACR having independently measured it. | `evidence_ref_ids` pointing at a document/episode-backed `EvidenceRef` (resolved via the existing evidence boundary, not a Context Fabric-specific type). | **Structural only.** `ValidateAgainst` checks a cited evidence ref ID exists in the allow-set built from the supplied graph paths, candidates, canonical facts, and (CHAOS-4522) the cohort's own per-member `EvidenceRefIDs`. It does not verify the underlying document says what the model claims it says. |
| **Inference** | The model's own reasoning connecting the above into a judgment, with no further external grounding possible. | Free-text prose: `direct_judgment`, `current_state`, `strongest_pressures`, `limitations`, `qualification`. | **Not independently checkable.** See §3. |

`ContextFabricDriverJudgment`/`ContextFabricFinding` also carry
`EpistemicStatus` (`observed` / `source_asserted` / `inferred` / `disputed`
/ `superseded` / `unknown`) and `Derivation` (`canonical_structured` /
`deterministic_projection` / `graph_associated` / `model_extracted` /
`rule_inferred`) -- these are the model's own self-classification along
roughly the same four-way split, useful for a consumer that wants to filter
or weight by grounding kind, but they are not independently verified by
`ValidateAgainst` the way `claimed_facts` are. Don't confuse a driver
*saying* it is `canonical_structured` with the closure guarantee that only
`ClaimedFacts` actually provides.

## 2. Value-level closure: what it catches and how

This is the mechanism the Reset 0 adversarial review named as a must-do: "a
synthesis draft claiming 'release-ready' against canonical
`release_ready=false` currently passes every Reset 0 validator." Structural
closure (does a citation exist) doesn't catch that; a contradicted value
still cites something real.

The fix, concretely:

1. `ContextFabricDriverCategory` is a closed vocabulary
   (`internal/contracts/v1/context_fabric_types.go`) mapped 1:1 to the
   `FactKind`s a canonical-fact-shaped judgment could be about (`status`,
   `readiness`, `blockers`, `health`, ...). `ContextFabricDriverCategoryRequiresClaimedFact`
   is a plain map lookup -- **never** a substring/keyword match over
   `Category`, `Title`, or `Summary` text, so wording can't dodge or
   falsely trigger the requirement.
2. A driver or finding whose `Category`/`Kind` is in that table must cite
   at least one `ClaimedFactID` resolving to a claim of the matching
   `FactKind`. Enforced twice: structurally at
   `ContextFabricDriverJudgment.Validate`/`ContextFabricFinding.Validate`
   (presence only), and with full cross-reference (does the ID resolve,
   does its `Kind` match) at `ContextFabricInvestigationResult.Validate`
   via `validateClaimedFactReferences`.
3. Every `ClaimedFact` the model produces is checked, before a result is
   ever built, against the real `CanonicalFactBundle` the synthesizer
   received: some `CanonicalFact` with the same `Kind` and `Subject` must
   carry `Fields[claim.Field]`, and its value must deep-equal the claim's
   value exactly (`factValueEqualsScalar`). This is struct equality, not
   text similarity -- no false positives from rewording, no false negatives
   from a value that merely looks similar.

   **"Some", not "the first" (CHAOS-4522).** `groundClaim`
   (`model_runtime.go`) closes over EVERY fact sharing the claim's
   `(Kind, Subject)`. A `ClaimedFact` addresses a fact by
   `(Kind, Subject, Field)` alone -- `CanonicalFact` carries no identifier
   -- so when a producer legitimately emits several facts under one
   `(kind, subject)`, no member of that group is addressable in preference
   to another, and picking one by slice order is an accident, not a rule.
   That accident was a live defect: a `discovered_cohort` answer carries
   17 `readiness|team:CHAOS` facts (one per work scope/day), the first of
   which observes no `estimate_coverage_ratio`, so every claim about the
   readiness coverage gap was rejected as "not canonically observed" while
   the value sat in the next fact of the group -- HTTP 422, 6 attempts of
   6. `cohort_ranking.go`'s `findFact` already documented that
   readiness/workload/deficiency producers "can legitimately emit
   several"; the ranking closed over the group and this validator did not.
   The guarantee is unchanged in strength either way: a claim is admitted
   iff some canonical fact the model was actually shown observed that
   field with exactly that value.

   `attachCanonicalRows` resolves a claim's table through the SAME
   `groundClaim` (via `claimSourceFact`), so a claim's scalar `Value` and
   its rendered `Rows` always describe one observation rather than two
   members of the group.

   **Residual, disclosed rather than fixed:** two claims CAN now each be
   grounded in a different member of one group (e.g.
   `estimate_coverage_ratio` 0 and 1 for one team, from two work scopes)
   and both are individually true, but a reader cannot tell them apart
   because the claim carries no fact identity. Closing that needs
   `CanonicalFact.id` + `ClaimedFact.fact_id` -- a contract widening plus
   a prompt change -- tracked separately.

   **The closure the model is shown must equal the closure it is judged
   against (CHAOS-4522).** `synthesisSubjects` and the `allowedEvidence`
   allow-set are rebuilt inside `ValidateAgainst` from `SynthesisInput`,
   while `synthesisInputFromDomain` decides what the model actually sees.
   When those two drift, ACR rejects the model for citing what ACR itself
   supplied. That is not hypothetical: `Cohort.Members[].Subject` was
   admitted by `synthesisSubjects` from CHAOS-4398 onward, but the same
   members' `EvidenceRefIDs` -- serialized into the prompt as part of the
   Cohort -- were absent from `allowedEvidence`, so a cohort member with
   no canonical fact of its own could be cited only by an evidence ref
   nothing in the allow-set could ever supply. Any future field added to
   the model-facing payload must be added to both sides together.
4. `DeterministicAnswer` is **server-composed**, not model-authored
   (`composeDeterministicAnswer` in `model_runtime.go`), from the
   already-validated `Status` + principal `Drivers` + `ClaimedFacts` only.
   The model's own `deterministic_answer` output is discarded entirely.
   This is the only reading of "deterministic" that can't itself
   reintroduce an unchecked claim: every value it renders was already
   proven equal to the canonical bundle by step 3.

## 3. The honest residual limitation

Free-text fields (`direct_judgment`, `current_state`, `limitations`,
`qualification`, `warnings`) cannot be proven closed by pure code. A model
could still write "the project looks release-ready" inside `current_state`
prose without an accompanying `ClaimedFact` -- catching that is an
NLP/entailment problem (does this sentence assert something a value
contradicts?), not a deterministic one, and is out of scope for
`ValidateAgainst`.

This is deliberately accepted, not hidden:

- The synthesis prompt (`internal/contextfabric/genkitruntime/prompts.go`)
  instructs the model to route every canonical-fact-shaped judgment through
  `claimed_facts` and to distinguish the four kinds explicitly, but a
  prompt is guidance, not a guarantee.
- `DeterministicAnswer` -- the one field the ticket calls out as needing to
  be genuinely deterministic, and plausibly the one most likely to get
  quoted verbatim by a downstream consumer -- is immune to this gap by
  construction (§2.4), because it is never model prose at all.
- `direct_judgment`/`current_state` remain model prose. A future evaluator
  pass (`ModelReceiptSink`'s reserved `EvaluatorVersion` seam, ADR 0008,
  still unwired) is the natural place to add an asynchronous,
  out-of-band entailment check against these fields -- deliberately not
  built synchronously into the request path, where a false-positive
  rejection would degrade availability for a check that itself may be
  imperfect.

## 4. Persistence and replay

Every field above -- claims, paths, evidence refs, prose -- persists
verbatim in `acr.context_fabric_investigation_results` (migration `0009`,
`internal/contextfabric/pginvestigation`) as the immutable JSON payload of
the full `ContextFabricInvestigationResult`. A follow-up turn's
`PriorSubjectReceipts` binds back to a `SubjectCandidate.ReceiptID` inside
that same persisted record (`Engine.resolvePriorSubjectHints`); there is no
separate "receipt" record or schema. See `internal/contextfabric/ports.go`'s
`InvestigationResultStore` doc comment for the organization-scoping binding
precondition every implementation (Postgres, memory, or otherwise) must
enforce.

See also: [`context-fabric-regime-a-offers.md`](context-fabric-regime-a-offers.md)
(CHAOS-4234) for what a turn-1 result carries when the class-default window
gate fires -- kind/handle/candidate offers beside the window offer, with no
committed material.
