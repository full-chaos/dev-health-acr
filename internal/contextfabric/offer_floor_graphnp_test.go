package contextfabric

import "testing"

// TestTheFloorEmptiedArmYieldsToAGraphThatWasNeverProjected: a floor outcome
// on a resolution whose graph was not projected is NOT the subject-not-found
// no_match -- nothing was searched, so it must fall through to the
// graph-not-projected handling instead of claiming a search happened.
func TestTheFloorEmptiedArmYieldsToAGraphThatWasNeverProjected(t *testing.T) {
	t.Parallel()
	resolution := SubjectResolution{
		Candidates: []SubjectCandidate{}, Committed: []SubjectRef{},
		GraphNotProjected: true,
	}
	floor := OfferFloorOutcome{Refused: true, SearchedKinds: []string{"team"}}
	request := InvestigationRequest{Options: InvestigationOptions{AllowClarification: true}}
	_, limitation := resolveTerminalStatus(request, &resolution, nil, false, declaredKindDecision{}, SubjectSubstitutionNotEvaluated, floor)
	if limitation == noMatchLimitationOfferFloorEmptied {
		t.Fatalf("limitation = %q: the floor-emptied arm fired on a graph that was never projected", limitation)
	}
	resolution.GraphNotProjected = false
	if _, control := resolveTerminalStatus(request, &resolution, nil, false, declaredKindDecision{}, SubjectSubstitutionNotEvaluated, floor); control != noMatchLimitationOfferFloorEmptied {
		t.Fatalf("control: limitation = %q, want the floor-emptied marker when the graph was projected", control)
	}
}
