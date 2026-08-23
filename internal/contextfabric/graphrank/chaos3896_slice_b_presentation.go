package graphrank

import (
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// This file is CHAOS-3896 Slice B: "survivors-first prompt construction
// from census-attested elimination; budgets/degradation live" (design
// brief v6 §6 item 2). It is PRESENTATION ONLY -- see SurvivorsFirstOrder's
// own doc comment for the exact invariant. RunShadowEvidenceRound's own
// would_commit/would_no_match/would_clarify decision (CHAOS-3899) is
// untouched; this file consumes its Attestation strictly AFTER that
// decision has already been made, and never feeds anything back into it.

// candidateSurvivorVerdict is this file's own closed, in-process
// classification for one pooled candidate against one census kind's own
// attestation -- never traced, never part of any contract type.
type candidateSurvivorVerdict int

const (
	// verdictNeutral is both "no opinion" AND the fail-safe default: every
	// case this file cannot POSITIVELY prove eliminated -- a non-censused
	// kind, an incomplete/mismatched/over-budget census, an unbridged
	// satisfier id -- lands here. A neutral candidate is never demoted;
	// only a positively eliminated one is.
	verdictNeutral candidateSurvivorVerdict = iota
	// verdictEliminated is design brief §1.5's own "pool \ satisfiers":
	// this kind's census completed cleanly, named its FULL satisfier set
	// (a single witness at Count==1, or the closure-checked SET at
	// 2<=Count<=CensusBudget), and this candidate's own canonical id is
	// NOT in it.
	verdictEliminated
)

// classifyCandidate is design brief §1.5's pool∩satisfiers rule, applied to
// ONE pooled candidate: "pool ∩ satisfiers = survivors; pool \ satisfiers =
// eliminated... Pooled hypotheses of NON-censused kinds are never
// eliminated." byKind is Attestation.Kinds indexed by its own Kind field
// (the caller builds this once per call, not once per candidate).
//
// Every branch below that cannot POSITIVELY establish "this candidate's id
// is absent from a TRUSTWORTHY, COMPLETE satisfier set" returns
// verdictNeutral -- fail-safe by construction, matching chris's own ruling
// on the 2<=Count<=CensusBudget closure discipline ("closure mismatch ->
// the kind goes neutral, never eliminated") extended to every other
// not-fully-trustworthy case this function can observe (a kind this round
// never censused at all, an incomplete/errored kind, the existing
// decisive-path ClosureMismatch, an over-budget kind with no fetched set,
// or a satisfier that failed to bridge to a canonical id at all).
func classifyCandidate(candidate contextfabric.SubjectCandidate, byKind map[CensusKind]KindAttestation) candidateSurvivorVerdict {
	if !IsCensusKindRegistered(candidate.Subject.Kind) {
		return verdictNeutral
	}
	ka, ok := byKind[candidate.Subject.Kind]
	if !ok || !ka.Complete || ka.ClosureMismatch {
		return verdictNeutral
	}
	switch {
	case ka.Count == 0:
		// The census proved ZERO satisfiers exist for this kind under D --
		// every pooled candidate of this kind is eliminated, unconditionally.
		return verdictEliminated
	case ka.Count == 1:
		if ka.SatisfierCanonicalID == "" {
			// Bridge failed/omitted (or CensusFunc never populated it) --
			// cannot compare, so cannot positively eliminate.
			return verdictNeutral
		}
		if candidate.Subject.CanonicalID == ka.SatisfierCanonicalID {
			return verdictNeutral // survivor
		}
		return verdictEliminated
	case ka.Count >= 2 && ka.Count <= CensusSatisfierSetBudget:
		if ka.SatisfierSetClosureMismatch || len(ka.SatisfierCanonicalIDs) == 0 {
			return verdictNeutral
		}
		for _, id := range ka.SatisfierCanonicalIDs {
			if id == candidate.Subject.CanonicalID {
				return verdictNeutral // survivor
			}
		}
		return verdictEliminated
	default:
		// ka.Count > CensusSatisfierSetBudget: over the enrichment budget,
		// no set was ever fetched (design brief's own cost-contract
		// discipline) -- nothing to compare against.
		return verdictNeutral
	}
}

// CensusSatisfierSetBudget mirrors devhealthsource.CensusBudget's value
// (999) -- duplicated as a plain constant rather than imported, matching
// this package's own established "graphrank cannot import devhealthsource"
// boundary (CensusOutcome's doc comment). A dedicated cross-package test
// (TestCensusSatisfierSetBudgetMatchesDevhealthsource, devhealthsource
// package, which CAN import graphrank) pins the two against each other so
// a future edit to either constant that forgets its mirror fails loudly
// instead of silently drifting -- the same discipline
// KindHasAnchorFK/censusKindRegistryEntries.anchorColumns already use for
// an identical cross-boundary duplication.
const CensusSatisfierSetBudget = 999

// SurvivorsFirstOrder is CHAOS-3896 Slice B's own entry point: given
// candidates (the ALREADY tiered, truncated, final list
// ResolveFromMergedCandidatesWithGate produced -- CHAOS-3899's own decisive
// pipeline, completely unchanged by this slice) and the SAME resolution's
// shadow evidence round Attestation, returns a REORDERED COPY: every
// census-eliminated candidate moved after every survivor/neutral one,
// stable within each group (a budget-exhausted or otherwise-untrustworthy
// round returns candidates unchanged, byte-identical).
//
// THE INVARIANT THIS FUNCTION MUST NEVER VIOLATE (design brief v6 §6 Slice
// B row: "presentation only... must NOT move behavior"): the RETURNED
// SLICE HAS THE EXACT SAME MEMBERSHIP AS candidates, ALWAYS -- same
// elements, same length, only order changes. This function never adds,
// drops, or otherwise interprets a candidate; it is not a filter and must
// never be mistaken for a commit-gate or no_match decision (those stay
// CHAOS-3899's/a future Slice C's alone). Callers MUST NOT use this
// function's output to decide resolution.Status, resolution.Committed, or
// which candidates belong in the result at all -- only their DISPLAY
// order, and (when the resolution is ambiguous) the clarification prompt
// text built from that order.
//
// Attestation.Reason == ReasonBudgetExhausted (design brief §4:
// "budget_exhausted -- everything from the partial pass discarded --
// decisions AND prompt ordering") short-circuits to a verbatim copy of
// candidates, unreordered -- the literal acceptance pin. Every OTHER
// degradation this function can observe (a kind this round never censused,
// an incomplete/errored kind, an existing-path ClosureMismatch, an
// unbridged satisfier, an over-budget kind) already fails toward
// verdictNeutral per classifyCandidate's own doc comment, so this function
// needs no separate handling for them -- a partially-degraded round can
// only ever produce FEWER eliminations, never a wrong one.
//
// tracer/requestID (CHAOS-4088, trace-only -- team-lead ruling on this
// ticket): when tracer is non-nil, EVERY candidate this call actually
// classifies emits one slice_b_survivor_verdict event (verdictNeutral
// included -- see ResolutionTraceEvent.SurvivorVerdict's own doc comment
// for why silence must mean "never reached", not "everything neutral").
// A nil tracer (every existing caller before this ticket, and every
// caller that does not thread one) skips this with zero extra cost, the
// same convention every other optional ResolveDeps-sourced tracer call in
// this package already uses. This is READ-ONLY of the verdicts this
// function was already computing for its own reordering -- it changes
// nothing about which candidate ends up where.
func SurvivorsFirstOrder(candidates []contextfabric.SubjectCandidate, attestation Attestation, tracer ResolutionTracer, requestID string) []contextfabric.SubjectCandidate {
	ordered := make([]contextfabric.SubjectCandidate, len(candidates))
	copy(ordered, candidates)
	if attestation.Reason == ReasonBudgetExhausted {
		return ordered
	}
	byKind := make(map[CensusKind]KindAttestation, len(attestation.Kinds))
	for _, ka := range attestation.Kinds {
		byKind[ka.Kind] = ka
	}
	verdicts := make([]candidateSurvivorVerdict, len(ordered))
	anyEliminated := false
	for i, candidate := range ordered {
		verdicts[i] = classifyCandidate(candidate, byKind)
		if verdicts[i] == verdictEliminated {
			anyEliminated = true
		}
		if tracer != nil {
			verdictName := "neutral"
			if verdicts[i] == verdictEliminated {
				verdictName = "eliminated"
			}
			tracer.Trace(ResolutionTraceEvent{
				RequestID: requestID, Stage: "slice_b_survivor_verdict",
				Subject: candidate.Subject, SurvivorVerdict: verdictName,
			})
		}
	}
	if !anyEliminated {
		// Nothing to reorder -- also keeps this the common, no-op case
		// (design brief §9/chris's own expectation-setting: most rounds
		// will have nothing census-eliminated at all) cheap and allocation-
		// free beyond the defensive copy above.
		return ordered
	}
	index := make([]int, len(ordered))
	for i := range index {
		index[i] = i
	}
	sort.SliceStable(index, func(i, j int) bool {
		return verdicts[index[i]] < verdicts[index[j]] // verdictNeutral(0) before verdictEliminated(1)
	})
	result := make([]contextfabric.SubjectCandidate, len(ordered))
	for i, idx := range index {
		result[i] = ordered[idx]
	}
	return result
}

// ReorderingWasReachable reports whether AT LEAST ONE census kind in this
// Attestation reached the non-decisive 2<=Count<=CensusBudget satisfier-SET
// enrichment WITH a trustworthy (closure-agreeing, non-empty) result --
// i.e. whether SurvivorsFirstOrder had genuine multi-candidate elimination
// evidence available for at least one kind, as opposed to every censused
// kind landing on Count==0, Count==1, over-budget, or a closure mismatch.
// This is NOT a production signal (not traced, not telemetry) -- it exists
// SOLELY for the harness measurement chris asked for: "report the count of
// rounds where reordering was REACHABLE... that forecasts how much surface
// Slice C's gate actually gets."
func ReorderingWasReachable(attestation Attestation) bool {
	for _, ka := range attestation.Kinds {
		if ka.Complete && !ka.ClosureMismatch && ka.Count >= 2 && ka.Count <= CensusSatisfierSetBudget &&
			!ka.SatisfierSetClosureMismatch && len(ka.SatisfierCanonicalIDs) > 0 {
			return true
		}
	}
	return false
}
