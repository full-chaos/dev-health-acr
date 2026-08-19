# Context Fabric: 3896 Slice B — survivors-first presentation

Short design note for CHAOS-3896 Slice B (design brief v6 §6 item 2):
"presentation only. Survivors-first prompt from census-attested
elimination; budgets/degradation live." Builds directly on CHAOS-3898 S3's
bridge functions ("pool∩satisfiers now runs live against the S3 bridge
fns — that was the whole point of S3").

## 0. What Slice A (CHAOS-3899) left unbuilt

CHAOS-3899 shipped the full shadow evidence round -- D-derivation, the
aggregate-first census protocol, the would_commit/would_no_match/
would_clarify decision -- but two things stayed deliberately absent,
matched exactly by design brief v5 §1.3(2)'s own note and by
`devhealthsource.CensusBudget` (defined, unconsumed, since CHAOS-3899):

1. **The non-decisive satisfier-SET fetch** for 2≤count≤CensusBudget. The
   census's decisive protocol only ever fetches ONE row (the witness, at
   count==1) -- there was no code path that named more than one satisfier
   at all.
2. **The source→graph bridge**, consumed. CHAOS-3898 S3 built
   `BridgeSatisfierToCanonicalID`; nothing called it before this slice.

Both were the SAME "defined ahead of its own wiring" pattern this program
uses throughout (`RelationshipFamilyWorkItemHierarchy` in S2b was the same
shape, closed by S3's own fix-forward).

## 1. The satisfier-SET fetch and its closure discipline

`RunCensus` (devhealthsource) now issues a THIRD statement, `SELECT
<identityColumn> ... LIMIT {CensusBudget}+1`, but ONLY for
2≤count≤CensusBudget -- Count==0 and Count>CensusBudget still issue only
the aggregate statement, unchanged.

Chris's ruling on this fetch's own soundness: it must be **attested
against the SAME count the aggregate witnessed** -- the identical "a race
can only demote, never mint" discipline the decisive Count==1 path already
applies (identity-witness comparison), extended to the multi-row case. The
implementation: `len(fetchedRows) == aggregateCount && len(fetchedRows) <=
CensusBudget` is required to trust the set; anything else (a row count
disagreement from a race between the two statements, or the LIMIT `B+1`
overflow sentinel) sets `SatisfierSetClosureMismatch` and discards the
fetched rows entirely -- **never** a partial or best-effort set.

This is a **separate field** from the existing `ClosureMismatch` (the
decisive Count==1 path's own closure check), deliberately: the two must
never be conflated. `ClosureMismatch` can still affect
`RunShadowEvidenceRound`'s own decisive outcome (would_clarify via
`ReasonCensusClosureMismatch`); `SatisfierSetClosureMismatch` is
presentation-only and is read by nothing outside this slice's own reorder
step.

## 2. The bridge boundary: devhealthsource bridges, graphrank never does

`graphrank` cannot import `devhealthsource` (the reverse import already
exists, `chaos3884_identity_universe.go`). Since the bridge
(`BridgeSatisfierToCanonicalID`) lives in `devhealthsource`, bridging must
happen on that side of the boundary -- `devhealthsource.NewCensusFunc`'s
own adapter closure now calls it for both the singular Count==1 witness
and every entry in the new satisfier SET, handing `graphrank.CensusOutcome`
ALREADY-BRIDGED canonical ids (`SatisfierCanonicalID`,
`SatisfierCanonicalIDs`). `graphrank` never sees a raw natural key for this
purpose.

A bridge failure or H6 whole-row omission for one satisfier is silently
dropped from the set rather than propagated as an error or invalidating
the whole set: a natural key too long to bridge could also never have been
minted as a graph node in the first place (the identical omission rule
applies on the projection side), so an unbridgeable satisfier can never
legitimately match a pooled candidate's own canonical id anyway.

## 3. The reorder step: `SurvivorsFirstOrder`

New file, `graphrank/chaos3896_slice_b_presentation.go`. Consumes the
round's `Attestation` (now carrying the bridged ids, still never traced --
see §4) strictly AFTER `ResolveFromMergedCandidatesWithGate`'s own
commit-gate decision is final. The one invariant this function must never
violate: **the returned slice has the exact same membership as its input,
always** -- same elements, same length, order only. It is not a filter and
must never be mistaken for a commit-gate or no_match decision.

Per-candidate classification (`classifyCandidate`) implements design brief
§1.5's pool∩satisfiers rule:

- Kind not in the closed census-kind registry → neutral (never
  eliminated -- the literal acceptance pin).
- Kind censused this round but incomplete, closure-mismatched (either
  discipline), or the satisfier(s) never bridged → neutral. Every
  not-fully-trustworthy case fails toward neutral, never toward
  elimination -- a partially-degraded round can only ever produce FEWER
  eliminations, never a wrong one.
- Count==0 → every candidate of that kind eliminated (census proved zero
  satisfiers exist).
- Count==1 → eliminated iff the candidate's canonical id differs from the
  bridged witness.
- 2≤Count≤CensusBudget → eliminated iff the candidate's canonical id is
  absent from the (closure-verified) bridged set.
- Count>CensusBudget → neutral (no set was ever fetched).

`Attestation.Reason == ReasonBudgetExhausted` short-circuits the whole
function to a verbatim, unreordered copy -- the literal "budget-exhausted
discards preserve ordering" acceptance pin. (`ReasonBudgetExhausted` has no
production assignment site as of this slice -- see §5.)

`resolve.go`'s `ResolveSubjects` now captures the round's Attestation
(previously discarded) and, when the shadow round ran at all (the existing
"stalled" gate, unchanged), reorders `resolution.Candidates` and rebuilds
`resolution.ClarificationPrompt` from the new order if one was built.
`resolution.Status`/`resolution.Committed` and which candidates are IN the
list are untouched -- exactly what `ResolveFromMergedCandidatesWithGate`
already decided.

## 4. Trace isolation (chris's rider)

`CensusOutcome`/`KindAttestation` gained `SatisfierCanonicalID`,
`SatisfierCanonicalIDs`, `SatisfierSetClosureMismatch`. These are
in-process only, consumed solely by `SurvivorsFirstOrder` --
`RunShadowEvidenceRound`'s own `emit` closure (the only place anything
reaches `ResolutionTracer`) does not reference them, and
`TestResolutionTraceEventNeverCarriesSurvivorData` pins this structurally:
it enumerates `ResolutionTraceEvent`'s own fields via reflection and fails
if any field name starts with "Satisfier" -- so a future edit that
accidentally adds one fails loudly, independent of whether any test
exercises its value.

## 5. What this slice deliberately does not do

- **Does not assign `ReasonBudgetExhausted` anywhere.** The design brief's
  vocabulary separates per-kind `census_over_budget`/`census_error`
  (already live, unchanged) from a round-wide `budget_exhausted`
  ("everything from the partial pass discarded"). Building the detection
  for the latter (e.g. the round's own bounded sub-context expiring
  mid-loop, with kinds left untried) is new round-level machinery, not
  "reorder based on what the round already tells you" -- out of this
  slice's presentation-only scope. `SurvivorsFirstOrder` still HONORS the
  reason defensively (returns unchanged if it is ever set), and the
  per-kind neutral-on-incomplete fallback already covers the pin's spirit
  for any partial/interrupted round today.
- **Does not wire `deps.CensusFunc` into any production composition
  root.** Unchanged from Slice A -- `internal/runtime/hosted/open.go`
  still leaves it nil. The only place it is set today is the standing
  replay harness (`internal/runtime/hosted/chaos3884_replay_harness_test.go`),
  gated on the withheld `ACR_TEST_TRIAL_CORPUS`. Flipping that switch is a
  deliberate, separate decision (matching the epoch-flip "instrument,
  measure, then flip" discipline this program uses everywhere else), not
  bundled here.
- **Does not touch `RunShadowEvidenceRound`'s own decisive outcome
  logic.** would_commit/would_no_match/would_clarify are computed exactly
  as CHAOS-3899 shipped them; this slice only reads the round's AFTERMATH.
