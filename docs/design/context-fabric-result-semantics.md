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
| **Canonical observation** | A value ACR read directly from an authoritative Dev Health fact provider (`internal/contextfabric/devhealthfacts`, `FactCapabilityRegistry`) and the model restated verbatim. | `ContextFabricClaimedFact` entries (`result.claimed_facts`), referenced by `driver.claimed_fact_ids` / `finding.claimed_fact_ids`. | **Deterministic, code-only, value-level.** `SynthesisDraft.ValidateAgainst` (`internal/contextfabric/model_runtime.go`) deep-equals every claimed `(kind, subject, field, value)` against the canonical fact bundle the synthesizer actually received. A mismatch, or a claim for a field/kind never observed, rejects the whole draft (`ErrModelOutput` + `ErrSynthesisRejected`, CHAOS-3784). A draft written by the service's own model call is then served as a degraded answer (§2.6); a caller-supplied draft is refused with HTTP 422 `synthesis_rejected`. See §2. |
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

   **Derived claims.** A claim may state a value computed from served
   facts: `derived_from` (two or more `{fact_id, field}` inputs, at most
   eight) with `operation` `sum`, `difference` or `ratio` (the last two take
   exactly two inputs; first minus second, first over second). It is
   grounded only when every input is a numeric field of a served fact of the
   claim's own subject and `value` equals the operation over them (integer
   sums and differences exactly, any ratio or float within an absolute
   1e-9; a ratio over zero is ungrounded). It excludes `fact_id` and
   attaches no rows (no single source fact). A derived claim whose inputs do
   not resolve is rejected as `claim_derivation_input_unknown`, one whose
   value is not reproduced as `claim_derivation_value_mismatch`. Each
   rejected re-draw names its rule in `draw_rejected_reasons` (`output_schema_mismatch` for a draft the schema check refused; an `error_*` class when no rule judged the draw). The same word is the `rejection_reason` of the terminal rejection when the last draw is refused by the schema check alone (never `unclassified`); a draw this package's own rule also refuses keeps that rule.

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
5. `DeterministicAnswer` is **required if and only if the result is
   supported** (`ContextFabricResultSupported`,
   `internal/contracts/v1/context_fabric_deterministic_answer.go`). A result
   is supported when its completeness block counts at least one claimed fact
   (`completeness.claimed_facts_count >= 1`, the producer's own count) AND it
   carries at least one citable evidence ref (`evidence_ref_ids` non-empty).
   Both are structured fields; nothing reads prose to decide whether prose
   is allowed. On a result that is NOT supported the answer sentence may be
   empty: nothing was read behind it, and the document's disclosure
   (`limitations`, `warnings`, `coverage`, the completeness block,
   `refusal_basis`, the clarification prompt) is what it carries instead.
   `complete` and `partial` results keep requiring it at every support
   level, and a stored row with no completeness block keeps the rule it was
   written under. The published schemas state the same rule as an
   `allOf` / `if` / `then` on the result object.

   | status | may be supported? | `deterministic_answer` |
   |---|---|---|
   | `complete`, `partial` | yes | required |
   | `degraded`, `clarification_required`, `no_match` | yes | required iff supported; may be empty otherwise |

   This is a WIDENING only: every result that validated before still
   validates. The server does not yet emit the empty form; it will only
   after every consumer accepts it -- the ask-dev contract pin, and the
   acr-mcp sidecar binaries already released, whose embedded validator
   revalidates every result they fetch and still requires the sentence.

6. **A failed model call is served as a degraded answer, with the canonical
   values still on it.** When the service's own model call fails in a way the
   runtime will not retry -- unusable output, every permitted draft broke the
   service's bounds, or the call ran but its receipt was not recorded -- the
   answer is `degraded`, not an error. A caller gone, a rate limit, an
   unavailable provider or a deadline stay errors. The coverage carries one
   `synthesis_model_failed` detail (source `context-fabric:synthesis`) with a
   closed `failure_class` and a `retry_hint` derived from it, and the two
   fields appear together or not at all:

   | `failure_class` | `retry_hint` | meaning |
   |---|---|---|
   | `model_output_invalid` | `retry_may_succeed` | the provider's output could not be used |
   | `model_receipt_unrecorded` | `retry_may_succeed` | the call ran; its receipt was not recorded, so no text from it is served |
   | `synthesis_rejected` | `retry_unlikely_to_help` | every draft broke the service's bounds over these facts |
   | `synthesis_deadline` | `retry_may_succeed` | no call failed: the first written answer was over the response budget and the time budget left no room for a second synthesis, so the facts are served |
   | `synthesis_not_narrowable` | `retry_unlikely_to_help` | no call failed: the first written answer was over the response budget and the cohort was already at its smallest, so the facts are served |
   | `synthesis_rows_over_budget` | `retry_unlikely_to_help` | no call failed: the first written answer was over the response budget because of the rows it carried, and a smaller cohort would not have made it fit, so the facts are served |
   | `synthesis_retry_over_budget` | `retry_unlikely_to_help` | no call failed: a narrower second synthesis ran and still did not fit the response budget, so the facts of the first pass are served |

   No model-authored field is served. Instead each scalar canonical field that
   was read is minted as a claimed fact (claim id `server:fact:<n>`), grounded
   by construction in the fact it was read from, with the evidence those facts
   cite, bounded by the claimed-fact and evidence ceilings. `current_state`
   names them as "Canonical values read", not as current. The answer carries
   the limitation "Synthesis failed; the facts shown are the canonical values
   read, with no judgment applied." and, when facts were left out for a bound,
   "Synthesis failed and more canonical values were read than one answer can
   carry; the facts shown are a part of them." With no value to carry it says
   "Synthesis failed and no canonical values could be shown in its place."
   Such an answer is never a reuse source.

## 3. The honest residual limitation

Free-text fields (`direct_judgment`, `current_state`, `limitations`,
`qualification`, `warnings`) cannot be proven closed by pure code. A model
could still write "the project looks release-ready" inside `current_state`
prose without an accompanying `ClaimedFact` -- catching that is an
NLP/entailment problem (does this sentence assert something a value
contradicts?), not a deterministic one, and is out of scope for
`ValidateAgainst`.

One slice of it is closed deterministically: free text may not name a
calendar day the investigation input does not hold. Every ISO date or
datetime and every month-name date in the draft's free-text fields is checked
against the days found in the synthesis input and its evidence window, plus
one day either side of each instant the input carries (a local zone can shift
the day); a day with no match
rejects the draft with reason `free_text_instant_ungrounded`. A bare month or
year names no day and is not checked. This catches an invented date, not a
wrong claim about a real one.

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

## 5. Served words: the vocabulary label map

Rule (CHAOS-4690 item 4, ruled for this surface on CHAOS-12413): a closed
vocabulary gets a contract-carried label, and raw tokens stay in the Details
fold. Consumer phrasing tables are ruled out, so a new value needs no change in
ask-dev or web.

**The field.** `vocabulary_labels` is an optional root map on the result and on
the answer projection: `"<vocabulary>:<value>" -> label`, shaped like
`evidence_ref_labels`, holding only the values in that result. A consumer looks
a word up, and a value with no entry takes its generic path. A lookup is not a
table. The plural of a subject kind is served under `subject_kind_plural:<value>`,
so no consumer pluralizes.

**The tables** are in `internal/contracts/v1/context_fabric_display_labels.go`,
beside the vocabularies, and the fact field table is in
`internal/contracts/v1/context_fabric_fact_field_labels.go`. Totality runs both
ways (`chaos12415_vocabulary_labels_test.go`), sourced from the production
vocabulary or the published schema: a new value cannot ship unlabeled, and a
label cannot outlive its value. The fact field table is sourced from the
declarations themselves (`devhealthfacts/chaos12418_fact_field_labels_test.go`),
under the catalogue-truth test (CHAOS-7073), so what a provider emits, what it
declares and what the table labels are one set.

| Vocabulary | Field |
|---|---|
| `answer_status` | `status`, `completeness.terminal_status`, `status_correction.replaced_status` (by the rule below) |
| `requirement_outcome` | `completeness.outcomes[].outcome`, `status_correction.deciding_outcome` |
| `candidate_state` | `subject_resolution.candidates[].state` |
| `prior_receipt_disposition` | `prior_subject_receipt_dispositions[].disposition` |
| `structure_disposition` | `confirmed_structure[].disposition` |
| `subject_kind`, `subject_kind_plural` | `cohort.kind`, `coverage.details[].kind` |
| `member_outcome` | `cohort.members[].outcome` |
| `ranking_signal` | `cohort.members[].drivers[].signal`, `.missing_signals[]` (the prose noun) |
| `threshold_label` | `cohort.members[].drivers[].threshold_labels[]` |
| `driver_window` | `cohort.members[].drivers[].window` |
| `driver_standing` | `drivers[].standing` |
| `fact_kind` | `remaining_work[].kind`, `readiness_gaps[].kind`, `conflicts[].kind`, `claimed_facts[].kind` (the registry's word) |
| `fact_field` | `claimed_facts[].field`, the keys of `rows[].fields` and of `time_series_rows[].fields` (one row per declared name, with its unit) |
| `driver_source_state` | `drivers[].source_state` (the registry's word, as on a coverage row) |
| `cohort_score_meaning` | `cohort.score_meaning` (what the score measures) |
| `window_relative_id`, `window_provenance` | `effective_evidence_window.relative_id`, `.provenance` |

**Notes.** A field a reader sees as one line has one word for this result, under
`<field>:note` (CHAOS-12487). Like the correction note, a note is no table row. It
is served only when its field says so, so a false flag, a zero and an absent field
all show nothing and imply nothing. The raw field stays in the fold.

| Key | Served when | Word |
|---|---|---|
| `limitations_displaced:note` | `limitations_displaced` above 0, canonical document only | The count, in one sentence |
| `generated_at:note` | `generated_at` is set | The lead word. The consumer formats the date |
| `reused:note` | `reused` is true | This is a stored answer |
| `retrieval_degraded:note` | `subject_resolution.retrieval_degraded` is true | Subject matching ran in a reduced way |
| `summary_corrected:note` | A driver's `summary_corrected` is true | The service corrected the source state, so the summary may not reflect it. The summary itself is never edited |
| `judgment_mismatch:note` | `cohort.judgment_mismatch` is true | The ranking does not support the judgment asked for |
| `population:note` | `cohort.population` above 0 | The size of the list. "At least" when `population_lower_bound` is set, never an exact count from a lower bound |

A projection has no `limitations_displaced` field. Its `limitations_omitted` also
counts what the projection cut, so `limitations_displaced:note` is dropped there:
the canonical count would understate what the reader is missing.

**Not served, on purpose.** `completeness.outcomes[].requirement` is not a
vocabulary: it is a composite coordinate, `obligation/role/subject`, so it takes
the generic path. `obligation`, the cause and refinement fields, plan narrowing,
and driver category, epistemic status and derivation stay in the fold. A derived
claim's `field` is free text, even when it matches a declared name, so it takes
the generic path.
`completeness.state` and `data_completeness` are never shown, and `outcomes[].impact` goes to the fold (CHAOS-12245,
CHAOS-12413): their tables are removed.

**The answer status word.** One function (`ContextFabricAnswerStatusLabel`) gives
every `answer_status:<status>`, and the note below reads it, so the two cannot
disagree. The word for `complete` also reads the result's completeness state, so
a value keeps one key and its word follows this result (CHAOS-12245):

| Status | Completeness state | Word |
|---|---|---|
| `complete` | `complete` | Full answer |
| `complete` | `not_derived`, or none | Answer |
| `complete` | `partial`, `degraded` | Answer, not fully covered |
| `partial` | any | Partial answer |
| `degraded` | any | Degraded answer |
| `clarification_required` | any | Answer needs your confirmation |
| `no_match` | any | No answer |

`no_match` says "No answer", not web's "No answer: no matching subject", because
the stop of ruling B also ends in `no_match`. These words are in sentence case
because a reader sees each as a whole label, and the note quotes them.

**The correction note.** `status_correction:note` is one sentence the service
composes for one result, so consumers hold no table for it (CHAOS-12413 ruling 2).
It is the only key that is not a table row, and it is present when the result or
`completeness` carries a status correction:

> The service lowered this answer from Answer to Partial answer: health was read
> only in part.

Each status reads the function above, so the note never holds a raw status word.
A direction reads "lowered", or "raised" for `degraded_to_partial`, the only raise.
The clause names the obligation (the first part of `deciding_requirement`) and the
outcome. An obligation or outcome with no word drops the clause, and a direction
outside the vocabulary reads "changed". With no word for a status there is no note:
a raw token is never the fallback. A correction a budget lever's row decided has no
deciding pair (CHAOS-12460), so its note has no clause: that row names no
requirement on purpose, and the correction stands without it.

**Source state words.** Each state keeps its own word, because unknown, stale,
no data, not applicable and zero are different statements (North Star check 12).
`no_data` stays "no data": "zero rows" is true of only one of its three paths. A stored result reads
today's words with no migration: `coverage.sources[].label` and `.state_label` are
stamped from the structured source and state on the served copy (ruling 3, which
narrows CHAOS-4690 design §7.3 for these two fields). `degraded_reasons[]` does not
change, and no string is parsed.

**Bound.** Each label is 1 to 160 characters, a key at most 128, and the map at
most 8,192 entries, the bound of `evidence_ref_labels`. That is not the working
limit. The map holds only the values in one result, so the whole registry is its
ceiling: 125 entries and 5,999 bytes, 9.2% of the 65,536-byte answer budget, plus
the notes. The fact field table has one row per declared name, so it stands
apart: 357 entries and 20,924 bytes, 31.9% of the budget, which no answer carries.
One result holds only its own fields: a claim adds its field and the columns of
its tables, and every name of the widest kind is 77 entries and 4,577 bytes, 7.0%.

**Where it is composed.** `contextfabric.ServeVocabularyLabels` is the only writer
(`TestOnlyTheServeFunctionSetsTheMap`). Two doors call it, after the document's last
writer: `serveContextFabricInvestigationResult`, for every engine exit, fresh,
reused or fitted, on the JSON response and the event stream; and
`serveStoredInvestigationResultOn`, for the read by id, the replay of a done
submission and the attach terminal. The engine saves its result before either door,
so the stored document never carries a word
(`TestEveryDoorThatEncodesAnAnswerComposesItsWords`). A read of a stored row
composes the map again, so a change of the registry reaches old answers.

**Budget.** The map counts against the byte budget it is served under, and a word
never costs a row or an answer. The engine fits the document with no map. Each door
then adds the words within its bound, which is the tighter of the service and caller
bounds, less the synthesis input on a client turn. The shed is tiered, so the served
document is the richest one within the bound:

1. The full map.
2. Only `answer_status:*`, `status_correction:note` and `judgment_mismatch:note`
   (`status_only`).
3. No map (`none`).
4. No stamped source words.

The status words go last because they say the most for the fewest bytes.
`judgment_mismatch:note` stays with them because nothing else says it: with no word,
a ranking reads as if it supports the judgment asked for. With no map
a partial answer reads a neutral pill and loses its note, so the pill says less than
the limitation below it. The input document stands if nothing fits, and the door
refuses or projects it as before. A document over the caller's bound goes to the
projection, which sheds by the same tiers before any count is halved, and whose
last tier projects the answer again with no stamped words. A consumer takes its
generic path for the words that are gone. The line
`context fabric vocabulary labels shed` states the `tier` reached.

**The projection's status words.** The projection cuts the document and re-derives
its completeness state, so the map it copies can overstate it: a `complete` status
that read "Full answer" on the canonical document reads "Answer, not fully covered"
on a projection that left the full result out. `Project` and `MarkFullResultOmitted`
therefore restate each `answer_status:*` word and the note from the projection's own
state, by the same function. Every other word is copied as served.

**Rollout.** The result root is `additionalProperties:false`, and ask-dev treats a
validation failure as an upstream failure. So the field is additive and optional
but NOT safe on an old pin: emitting it early fails every answer. An existing
deploy order cannot protect `main`, because acr and web roll separately. So
`ACR_CONTEXT_FABRIC_VOCABULARY_LABELS_ENABLED` (default off) gates the map, and a
follow-up ticket removes the flag once every consumer's pin is deployed. Off serves
no map. The stamped source words are served in both states, because those fields
are on every pin. So is the legend of a driver series (CHAOS-12473): `render_shapes`
is saved with the result, so a stored row's `series.label` would keep the signal
token, and `ServeVocabularyLabels` restates it from the `ranking_signal` word at
every door. A fresh result carries the same word from `SelectRenderShapes`. The
saved document stays as it was. See `docs/operations.md`.

**MCP.** The map is carried on MCP too. The API serves the same
`answerprojection.Project` output when an answer is over a caller's byte option
(`projectToCallerOption`, `internal/api/context_fabric_routes.go:420`), and web
accepts that projection, so the projection must carry the map. The MCP tool
calls the same function (`internal/mcp/investigate_question.go:141`), so leaving
it out would take a second code path. Both untrusted-content lists name
`structured.vocabulary_labels{}`.
