package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// D11-CLASS REGRESSION, pinned per the orchestrator's ruling (2026-08-13).
//
// D11 was: an unbounded lexical relevance score written straight into
// CandidateNode.Score, which ResultConfidence's fallback arm then INVERTED as
// 1/score, so higher relevance produced lower confidence. AC-3778-0 fixed that
// by normalizing lexical scores into Relevance.
//
// CHAOS-3778 introduces a second, differently-shaped raw score with the SAME
// failure mode available: FalkorDB's db.idx.vector.queryNodes yields a cosine
// DISTANCE, where 0 means identical. Verified live against graph module 42002:
// an identical vector scored 0.0 and an unrelated one scored 0.699398.
//
// That number falls into ResultConfidence's OTHER arm -- the
// `score >= 0 && score <= 1 -> return score` passthrough -- which would award
// the BEST possible match a confidence of 0.0 and a poor match 0.699. Same
// class of defect, opposite arm, new place.
//
// These tests pin the invariant that closes it: a raw FalkorDB vector distance
// must never be the value ResultConfidence interprets. The adapter converts it
// to a similarity and declares it in Relevance, which ResultConfidence always
// prefers.
func TestD11Class_RawVectorDistanceThroughScoreWouldInvertConfidence(t *testing.T) {
	// This is the BUG, demonstrated -- not the shipped behavior. It exists so
	// that anyone tempted to "simplify" the adapter by passing the vector
	// score straight through can see exactly what that costs.
	identicalDistance := 0.0
	unrelatedDistance := 0.699398159980774

	best := ResultConfidence(nil, &identicalDistance)
	worst := ResultConfidence(nil, &unrelatedDistance)
	if best >= worst {
		t.Fatal("this test no longer demonstrates the hazard it exists to pin; " +
			"ResultConfidence's passthrough arm must still read a raw distance as a confidence")
	}
	if best != 0 {
		t.Fatalf("a perfect vector match passed through Score reads as confidence %v, expected the inverted 0", best)
	}
}

// The shipped path: the adapter declares Relevance, so the perfect match wins.
func TestD11Class_NormalizedVectorRelevanceRestoresConfidenceOrder(t *testing.T) {
	// What falkorgraph's vector search does: distance -> similarity -> band.
	relevanceFor := func(distance float64) *NormalizedRelevance {
		cosine := 1 - distance
		if cosine < 0 {
			cosine = 0
		}
		// The [0.50, 0.70] vector band, with tau = 0.55 (the shipped default).
		const tau = 0.55
		if cosine < tau {
			t.Fatalf("a similarity of %v is below the floor and must be dropped, not scored", cosine)
		}
		return Normalized(0.50 + 0.20*(cosine-tau)/(1-tau))
	}

	best := ResultConfidence(relevanceFor(0.0), nil)  // identical
	good := ResultConfidence(relevanceFor(0.20), nil) // cosine 0.80
	weak := ResultConfidence(relevanceFor(0.42), nil) // cosine 0.58
	if !(best > good && good > weak) {
		t.Fatalf("confidence order inverted or flattened: best=%v good=%v weak=%v", best, good, weak)
	}
	if best > 0.70 || weak < 0.50 {
		t.Fatalf("vector confidences escaped the [0.50, 0.70] band: best=%v weak=%v", best, weak)
	}
}

// vectorArithmeticBandCeiling is a FIXED historical reference (not
// DefaultCommitGatePolicy().LoneFloor, and not corroboration_test.go's
// live-reading loneCommitGate), deliberately decoupled per luna review P2:
// this test exercises pure ResultConfidence/CorroboratedConfidence
// arithmetic, with no resolution decision and no isVectorOnlyCandidate
// guard involved at all, so it cannot know or care what LoneFloor is
// CURRENTLY configured to be. Binding it to the live default would make it
// fail the moment a future sweep intentionally lowers LoneFloor again --
// even though resolution.go's guard would still correctly block the commit
// at the resolution layer, exactly the scenario CHAOS-3857 shipped this
// guard to handle. This constant instead pins the SAME arithmetic fact
// AC-3778-3 relied on before the guard existed (vectorRelevanceCeiling
// strictly below the pre-CHAOS-3857 lone-candidate gate), as a stable,
// intentionally-never-auto-updated regression pin: if this specific
// arithmetic ever needs to move, that is a deliberate, reviewed edit to
// this test, not a side effect of changing production's LoneFloor
// elsewhere. The guard, not this arithmetic, is what a future LoneFloor
// change is checked against at the resolution layer -- see
// TestVectorOnlyGuardBlocksLoneCommitRegardlessOfConfidence
// (resolution_gate_policy_test.go).
const vectorArithmeticBandCeiling = 0.72

// The arithmetic invariant AC-3778-3 relied on before CHAOS-3857's guard
// existed: nothing a vector-only candidate can score, by the confidence
// math alone, reaches vectorArithmeticBandCeiling. This is a NECESSARY
// property of the vector band's own calibration (unrelated to whatever
// LoneFloor production currently uses), not the thing that makes AC-3778-3
// hold today -- resolution.go's isVectorOnlyCandidate guard is what does
// that, structurally, regardless of this arithmetic. See
// TestAC_3778_3_VectorOnlyCandidateCannotReachTheLoneCommitGate
// (corroboration_test.go) for the resolution-level proof.
func TestD11Class_NoVectorOnlyConfidenceCanReachTheCommitGate(t *testing.T) {
	const tau = 0.55
	for cosine := tau; cosine <= 1.0; cosine += 0.001 {
		relevance := Normalized(0.50 + 0.20*(cosine-tau)/(1-tau))
		confidence := ResultConfidence(relevance, nil)
		if confidence >= vectorArithmeticBandCeiling {
			t.Fatalf("a vector-only candidate at cosine %v reached confidence %v, at or past %v",
				cosine, confidence, vectorArithmeticBandCeiling)
		}
		// And corroboration must not be reachable from a single mechanism.
		single := CorroboratedConfidence([]contextfabric.MatchMechanism{contextfabric.MatchVector}, confidence)
		if single >= vectorArithmeticBandCeiling {
			t.Fatalf("a single-mechanism vector candidate reached %v, at or past %v", single, vectorArithmeticBandCeiling)
		}
	}
}

// A vector candidate must never rely on the Score fallback even by accident:
// if an adapter ever sets BOTH, Relevance must win. This is what makes the
// "normalize into Relevance" rule enforceable rather than merely advised.
func TestD11Class_RelevanceAlwaysBeatsAStrayScore(t *testing.T) {
	relevance := Normalized(0.68)
	strayDistance := 0.0 // what the raw FalkorDB score would have been
	if got := ResultConfidence(relevance, &strayDistance); got != relevance.Float() {
		t.Fatalf("ResultConfidence = %v, want the declared relevance %v -- Score must never win", got, relevance.Float())
	}
}
