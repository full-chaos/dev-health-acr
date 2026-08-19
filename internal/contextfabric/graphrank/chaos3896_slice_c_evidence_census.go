package graphrank

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// evidenceStrength implements design brief v6 §1.4's own description of the
// evidence_census commit path's confidence arithmetic: "the ratified
// corroborated-band arithmetic computed locally over the raw base plus one
// attested source witness (0.50 -> 0.755 >= LoneFloor); no wire mechanism,
// no curve change, no candidate mutation, raw-bases re-entry,
// MaxMatchMechanisms frozen at 6 by assertion."
//
// Concretely: CorroboratedConfidence's OWN formula
// (mechanism.go) --
//
//	strength   := 0.5*base + 0.5*(distinct-2)/(MaxMatchMechanisms-2)
//	confidence := CorroboratedFloor + (Ceiling-Floor)*strength
//
// -- evaluated with distinct FIXED at exactly 2: the census's own keyed,
// graph-existence-confirmed witness stands in for a second, independent
// mechanism, but ONLY for this local computation. Two things this
// deliberately does NOT do, matching the brief's own "no wire mechanism...
// no candidate mutation" text:
//
//  1. It never mints a contextfabric.MatchMechanism value for the witness.
//     MaxMatchMechanisms stays the closed, 6-member enum
//     (contractsv1.ValidContextFabricSubjectMatchMechanism) it always was --
//     the mechanism-weight term collapses to exactly 0 at distinct==2
//     (nothing "extra" beyond the two-mechanism floor), so the result is
//     driven by base alone: strength = 0.5*base.
//  2. It never writes its result back onto the candidate. The
//     evidence_census rescue (resolution.go) commits the candidate at
//     whatever Confidence it already carries -- exactly like the CHAOS-3829
//     vector_margin_rescue immediately above it, which also never mutates
//     Confidence at the point of commit. evidenceStrength is a GATE
//     function, not a scoring function.
//
// "raw-bases re-entry": base is the candidate's OWN Confidence as it stands
// when this rescue runs -- ResolveFromMergedCandidatesWithGate's phase-2.5
// pass already applied CorroboratedConfidence(mechanisms, base) to every
// candidate once, and for every candidate this rescue can ever reach that
// pass returned base UNCHANGED (design brief §0's own measured ground:
// every current stall is single-mechanism, sitting in [0.50, ~0.755) --
// a candidate that already cleared CorroboratedFloor via a real second
// mechanism would have committed at lone_floor before ever reaching this
// rescue). So reading candidate.Confidence here IS reading the raw,
// pre-corroboration base, without this function needing its own separate
// plumbing to recover it.
func evidenceStrength(base float64) float64 {
	base = Clamp(base)
	if base >= 1 {
		return 1
	}
	// mechanismWeight term: (distinct-2)/(MaxMatchMechanisms-2) with
	// distinct forced to 2 -- always exactly 0. Spelled out (rather than
	// simplified away) so a future MaxMatchMechanisms change is visibly
	// still safe: the witness always contributes the MINIMUM corroborated
	// case, never scales with the enum's size.
	const distinct = 2
	mechanismWeight := Clamp(float64(distinct-2) / float64(MaxMatchMechanisms-2))
	strength := Clamp(0.5*base + 0.5*mechanismWeight)
	corroborated := CorroboratedFloor + (CorroboratedCeiling-CorroboratedFloor)*strength
	if corroborated < base {
		return base
	}
	return corroborated
}

// indexBySubjectKey returns the index of the candidate whose SubjectKey
// equals key ("attestedSubject match" -- design brief §1.4's own case
// pseudocode names this as one of the evidence_census conjuncts). O(n) over
// an already-small, per-resolution candidate slice -- no index is worth
// building for a rescue that fires at most once per stalled resolution.
func indexBySubjectKey(candidates []contextfabric.SubjectCandidate, key string) (int, bool) {
	for index, candidate := range candidates {
		if SubjectKey(candidate.Subject) == key {
			return index, true
		}
	}
	return 0, false
}

// attestedSatisfier reports the ONE census kind/canonical-id pair a round's
// Attestation named decisively, per design brief §1.4: "censusComplete &&
// |satisfiers| == 1 names one source row S." Mirrors
// RunShadowEvidenceRound's own would-commit predicate (satisfierKinds==1,
// no mismatch, no non-censused-kind survivor) rather than trusting
// attestation.Outcome alone, so a caller that (incorrectly) invokes this
// against a stale or hand-built Attestation still gets the SAME structural
// guarantee the round itself enforces -- ok is false whenever the round's
// own conjunction does not hold, or the one qualifying kind's satisfier
// could not be bridged to a graph canonical id (CHAOS-3898's bridge
// omitted it, or errored -- KindAttestation.SatisfierCanonicalID stays ""
// in exactly that case, chaos3899_census_adapter.go's own NewCensusFunc).
func attestedSatisfier(attestation Attestation) (kind contextfabric.SubjectKind, canonicalID string, ok bool) {
	if attestation.Outcome != ShadowWouldCommit {
		return "", "", false
	}
	satisfierKinds := 0
	for _, ka := range attestation.Kinds {
		if !ka.Complete || ka.ClosureMismatch {
			continue
		}
		if ka.Count == 1 {
			satisfierKinds++
			if ka.SatisfierCanonicalID != "" {
				kind, canonicalID = ka.Kind, ka.SatisfierCanonicalID
			}
		} else if ka.Count > 1 {
			// A second, multi-satisfier kind alongside the decisive one --
			// should be structurally impossible whenever
			// Outcome==ShadowWouldCommit (RunShadowEvidenceRound's own
			// switch requires multiSatisfierKinds==0 for that outcome), but
			// checked here anyway rather than trusted, since this function's
			// whole point is not trusting Outcome alone.
			return "", "", false
		}
	}
	if satisfierKinds != 1 || canonicalID == "" {
		return "", "", false
	}
	return kind, canonicalID, true
}
