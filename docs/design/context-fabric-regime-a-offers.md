# Context Fabric turn-1 window gate: offers beside the window offer (CHAOS-4234)

Short reference note. Covers: what the class-default window gate ("regime
A") returns on turn 1 after CHAOS-4234, the offers-only resolution that
feeds it, what that pass skips and why, and the trace/report fields a
trial artifact carries for it. Design ruling and safety argument live in
`internal/contextfabric/chaos4234_offers_only.go`'s own doc comment;
sibling note: [`context-fabric-result-semantics.md`](context-fabric-result-semantics.md).

## 1. Why

Before CHAOS-4234 the class-default window gate (`engine.go`,
`WindowCanonicalizationGatedClassDefault`) returned before `ResolveSubjects`
ever ran, so `windowConfirmationRequiredResult` could only compose a
window-only disclosure (CHAOS-4118 option (a)). On the two kiac replicates
of record that made regime A the whole "ranking-budget wall": 45/50 and
43/48 positive-arm `expected_kind` offer misses and 14/25, 13/24
`subject_handle` misses were rows with no candidate pool at all, and a case
that spent turn 1 on the window and turn 2 on the kind never answered inside
the two-turn harness.

Ruling (team-lead, 2026-08-24): run `ResolveSubjects` under the gate in an
offers-only mode, keep its `StructureOfferMaterial`, discard every
commit-bearing output, compose kind/handle/candidate offers beside the
window offer. Ratified with it: offers minted under an inferred window are
non-decisive disclosure, the same class as the window offer itself. The
CHAOS-4040 bar (no committed material under an inferred window, gate order
unchanged, `window_commit_count` stays 0) is untouched; CHAOS-4118(a) is
extended, not reversed; CHAOS-4039's noninterference proof is untouched.

## 2. Turn-1 gated path

```mermaid
flowchart TD
    Q[Turn-1 request<br/>no stated window] --> I[Interpret]
    I --> EW[composeEffectiveWindow<br/>window.go]
    EW -- "provenance != inferred_default" --> D[decisive path<br/>ResolveSubjects -> DiscoverContext -> facts -> synthesis]
    EW -- "inferred_default (regime A)" --> G[gatedOfferMaterial<br/>chaos4234_offers_only.go]
    G -- "AllowClarification=false" --> RF[refused no_match<br/>no graph read]
    G -- "RegimeAOffersDisabled" --> WO[window-only StructureNeeds<br/>CHAOS-4118 shape]
    G --> RS["graph.ResolveSubjects(WithOffersOnlyResolution(ctx))"]
    RS -- "error" --> WO
    RS -- "resolution, bases, digests" --> X[DISCARDED]
    RS -- "StructureOfferMaterial" --> P[consultPriorStructureOffers]
    P --> C["composeGatedStructureNeeds<br/>Missing = [window, +kind/handle/candidate]<br/>WindowOptions + KindOptions + HandleOptions + CandidateOptions<br/>receipts minted by composeStructureNeeds"]
    WO --> R
    C --> R[windowConfirmationRequiredResult<br/>status clarification_required<br/>SubjectResolution EMPTY<br/>persisted with StructureNeeds]
    R --> T2["Turn 2: PriorWindowReceipts + PriorKindReceipts/Handle/Candidate<br/>in ONE request (engine.go canonicalizes both before the gate)"]

    subgraph OFFERS_ONLY["offers-only mode inside graphrank.resolveSubjects"]
        S1["Search / SearchQuestion / AliasLookup / coverage floor<br/>(unchanged)"]
        S2["ResolveFromMergedCandidatesWithGateAndBasis<br/>corroboration -> sort -> decision -> ranked_cut trace -> cut"]
        S3["SKIPPED: shadow evidence round / census<br/>CHAOS-4154 confirmed-kind scope pass<br/>SurvivorsFirstOrder"]
        S4["offer builders on kindOfferCandidates<br/>kind_offer trace: OfferedUnderWindowGate=true"]
        S1 --> S2 --> S4
        S2 -. "skipped" .-> S3
    end
    RS --- OFFERS_ONLY
```

The pass is window-agnostic by construction: the inferred window lives in
`effectiveWindow`, never in `interpretation.TimeContext`, and the graph
request handed to `ResolveSubjects` is the same `graphRequest` the decisive
path would use. Cost per gated request equals a regime-B turn 1 (per term:
one fulltext, one embedding, one KNN, plus alias lookup, question search,
and the coverage floor) minus the census round.

## 3. Reversibility

`EngineOptions.RegimeAOffersDisabled` (zero value = enabled) restores the
window-only disclosure and skips the graph read entirely. The engine-side
discard is the load-bearing safety layer; the ctx mark
(`contextfabric.WithOffersOnlyResolution`) is a cost optimisation a
`GraphReader` may ignore without producing a wrong outcome.

## 4. Telemetry (same change)

| Where | Field | Meaning |
| --- | --- | --- |
| `EngineTelemetry.RecordGatedOfferResolution` | `composed` / `empty` / `failed` / `disabled` / `refused` | once per class-default gated request |
| `kind_offer` trace event | `OfferedUnderWindowGate` | this resolution ran in offers-only mode |
| `ranked_cut` trace stage (`resolution.go`) | `Subject`, `Rank`, `Survived` | one event per candidate, in rank order, before the `MaxSubjectCandidates` cut; `Rank==1` opens a batch, readers keep the last batch |
| `ranked_cut` companion (`resolve.go`) | `CoverageBypass=true`, `Rank 0` | a coverage-floor find the cut dropped but `unionCandidatesForOffer` still hands to the offer builders |
| two-turn report row (schema v28) | `turn1_offer_composed_under_window_gate`, `expected_subject_in_pool`, `expected_subject_rank`, `expected_subject_at_offer_boundary`, `turn2_window_receipt_attached` | subject-level twins of `expected_in_pool` / `expected_kind_at_offer_boundary`; harness semantics flag |
| two-turn report (schema v28) | `regime_a_offer_composed_count`, `regime_a_turn2_answered_count` | summed by `cmd/acr-trial-merge-two-turn`, no bar |

Harness semantics change (schema v28): on a regime-A case the positive
arm's turn 2 carries the oracle's window receipt beside the member receipt.
`offer_miss_count` stays an engine-only aggregate across the bump; turn-2
aggregates carry the harness change as part of the lever.

## 5. Measurement

Kiac replicate pair at tip against the schema-27 pair of record
(`gen-trial-chaos3742_twoturn-parallel-20260824T163241Z` /
`...T175417Z`). Must move: `offer_miss_count.expected_kind` (50/48),
`offer_miss_count.subject_handle` (25/24), `regime_a_turn2_answered_count`
up from 8/46, 8/41 window rows. Must not move: `wrong_commit_count` 0,
`false_no_match_count` 0, `window_commit_count` 0, `window_gated_count` 65,
`anti_vacuity_valid` true, `controls_witnessed` 27/27,
`inferred_unjustified_count` 0, `structure_and_window_disclosure_absent_count`
0. Aggregates only; per-case noise is 22-34%.
