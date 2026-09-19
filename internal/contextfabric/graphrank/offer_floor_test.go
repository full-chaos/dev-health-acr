package graphrank

import (
	"math"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// allMatchMechanisms is the closed mechanism vocabulary the enumeration ranges
// over.
var allMatchMechanisms = []contextfabric.MatchMechanism{
	contextfabric.MatchExact, contextfabric.MatchAlias, contextfabric.MatchProviderKey,
	contextfabric.MatchLexical, contextfabric.MatchVector, contextfabric.MatchTraversalParent,
}

// mechanismSubsets returns every non-empty subset of allMatchMechanisms.
func mechanismSubsets() [][]contextfabric.MatchMechanism {
	var out [][]contextfabric.MatchMechanism
	for mask := 1; mask < 1<<len(allMatchMechanisms); mask++ {
		var subset []contextfabric.MatchMechanism
		for index, mechanism := range allMatchMechanisms {
			if mask&(1<<index) != 0 {
				subset = append(subset, mechanism)
			}
		}
		out = append(out, subset)
	}
	return out
}

// wantOfferAdmission is the decision table written independently of
// ClassifyOffer: state committed admits; vector alone refuses; an identity
// mechanism admits; anything else admits only strictly above the floor.
func wantOfferAdmission(committed bool, mechanisms []contextfabric.MatchMechanism, confidence float64) OfferAdmission {
	if committed {
		return OfferAdmittedCommitted
	}
	if len(mechanisms) == 1 && mechanisms[0] == contextfabric.MatchVector {
		return OfferRefusedVectorOnly
	}
	for _, mechanism := range mechanisms {
		if mechanism == contextfabric.MatchExact || mechanism == contextfabric.MatchAlias || mechanism == contextfabric.MatchProviderKey {
			return OfferAdmittedIdentity
		}
	}
	if confidence > OfferSimilarityFloor {
		return OfferAdmittedAboveFloor
	}
	return OfferRefusedAtOrBelowFloor
}

// TestOfferFloorEnumeration walks every mechanism subset x every score band
// (the floor and one representable step either side, the band ends, zero, one)
// x committed/proposed x every offerable subject kind, through the real
// classifier, and requires (a) the independent table to agree cell for cell,
// (b) every admission member to be reached, and (c) an offer to be admitted
// only when the candidate is committed, identity-proven, or a
// non-vector-only similarity hit strictly above the floor.
func TestOfferFloorEnumeration(t *testing.T) {
	scores := []float64{
		0, LexicalBandFloor, math.Nextafter(OfferSimilarityFloor, 0), OfferSimilarityFloor,
		math.Nextafter(OfferSimilarityFloor, 1), LexicalBandCeiling, CorroboratedFloor, 1,
	}
	reached := map[OfferAdmission]bool{}
	cells := 0
	for _, kind := range sortedKinds(structureOfferKinds) {
		for _, mechanisms := range mechanismSubsets() {
			for _, score := range scores {
				for _, committed := range []bool{false, true} {
					state := contextfabric.ResolutionProposed
					if committed {
						state = contextfabric.ResolutionCommitted
					}
					candidate := contextfabric.SubjectCandidate{
						Subject:         contextfabric.SubjectRef{Kind: kind, CanonicalID: "id"},
						State:           state,
						Confidence:      score,
						MatchMechanisms: mechanisms,
					}
					got := ClassifyOffer(candidate)
					want := wantOfferAdmission(committed, mechanisms, score)
					if got != want {
						t.Fatalf("kind=%s mechanisms=%v score=%v committed=%v: got %s, want %s", kind, mechanisms, score, committed, got, want)
					}
					reached[got] = true
					cells++
					identity := false
					for _, mechanism := range mechanisms {
						identity = identity || mechanism == contextfabric.MatchExact || mechanism == contextfabric.MatchAlias || mechanism == contextfabric.MatchProviderKey
					}
					if got.Admitted() && !committed && !identity && !(score > OfferSimilarityFloor) {
						t.Fatalf("admitted a similarity-only candidate at or below the floor: %v %v", mechanisms, score)
					}
				}
			}
		}
	}
	for _, admission := range OfferAdmissions {
		if !reached[admission] {
			t.Errorf("admission %q never reached by the enumeration", admission)
		}
	}
	if len(reached) != len(OfferAdmissions) {
		t.Errorf("enumeration reached %d admissions, vocabulary has %d", len(reached), len(OfferAdmissions))
	}
	t.Logf("enumerated %d cells over %d kinds", cells, len(structureOfferKinds))
}

// TestOfferSimilarityFloorIsDerivedFromTheLexicalBand pins the floor to the
// band and the coverage share it comes from: it is the confidence a hit
// covering half of the query's terms scores, so a hit at exactly half is
// refused and one more term is admitted.
func TestOfferSimilarityFloorIsDerivedFromTheLexicalBand(t *testing.T) {
	scoreFor := func(matched, terms float64) float64 {
		return LexicalBandFloor + (LexicalBandCeiling-LexicalBandFloor)*(matched/terms)
	}
	if OfferSimilarityFloor != scoreFor(1, 2) {
		t.Fatalf("floor = %v, want the score of a hit covering half the terms %v", OfferSimilarityFloor, scoreFor(1, 2))
	}
	lexical := []contextfabric.MatchMechanism{contextfabric.MatchLexical}
	for _, tc := range []struct {
		matched, terms float64
		want           OfferAdmission
	}{
		{0, 4, OfferRefusedAtOrBelowFloor},
		{1, 4, OfferRefusedAtOrBelowFloor},
		{2, 4, OfferRefusedAtOrBelowFloor},
		{1, 2, OfferRefusedAtOrBelowFloor},
		{3, 4, OfferAdmittedAboveFloor},
		{2, 3, OfferAdmittedAboveFloor},
		{1, 1, OfferAdmittedAboveFloor},
	} {
		got := ClassifyOffer(contextfabric.SubjectCandidate{State: contextfabric.ResolutionProposed, Confidence: scoreFor(tc.matched, tc.terms), MatchMechanisms: lexical})
		if got != tc.want {
			t.Errorf("%v of %v terms: got %s, want %s", tc.matched, tc.terms, got, tc.want)
		}
	}
}
