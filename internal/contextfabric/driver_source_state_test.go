package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func suppliedDriverSourceState(t *testing.T, factState SourceState, claimed SourceState) SourceState {
	t.Helper()
	wb := newWriteBackRig(t, func(r *clientRig) {
		for index := range r.facts {
			if r.facts[index].Kind == FactReadiness {
				r.facts[index].SourceState = factState
			}
		}
	})
	_, bundle := wb.firstCall()
	wb.parse.draft.Drivers[0].SourceState = claimed
	result, _, err := wb.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("write-back: %v", err)
	}
	if len(result.Drivers) != 1 {
		t.Fatalf("drivers = %d, want 1", len(result.Drivers))
	}
	return result.Drivers[0].SourceState
}

func TestSuppliedDraftCannotClaimACompleteStateForATruncatedFact(t *testing.T) {
	t.Parallel()
	if got := suppliedDriverSourceState(t, SourceTruncated, ""); got != SourceTruncated {
		t.Errorf("served driver source_state = %q, want truncated from the grounded fact", got)
	}
}

func TestSuppliedDraftCannotClaimTruncationForAnAvailableFact(t *testing.T) {
	t.Parallel()
	if got := suppliedDriverSourceState(t, SourceAvailable, SourceTruncated); got != "" {
		t.Errorf("served driver source_state = %q, want empty for an available fact", got)
	}
}

func suppliedDerivedDriverSourceState(t *testing.T, workState SourceState, claimed SourceState, withClaim bool) SourceState {
	t.Helper()
	wb := newWriteBackRig(t, func(r *clientRig) {
		r.facts = append(r.facts,
			CanonicalFact{Kind: FactReadiness, Subject: r.project, Fields: map[string]FactValue{"open_items": IntegerFactValue(7)}, EvidenceRefIDs: []string{"evidence_readiness_0001"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"},
			CanonicalFact{Kind: FactWork, Subject: r.project, Fields: map[string]FactValue{"closed_items": IntegerFactValue(3)}, EvidenceRefIDs: []string{"evidence_readiness_0001"}, SourceState: workState, Source: "ops", SourceVersion: "v1"},
		)
		AssignFactIDs(r.facts)
	})
	_, bundle := wb.firstCall()
	var open, closed string
	for _, fact := range wb.facts {
		switch fact.Kind {
		case FactReadiness:
			if _, ok := fact.Fields["open_items"]; ok {
				open = fact.ID
			}
		case FactWork:
			closed = fact.ID
		}
	}
	draft := &wb.parse.draft
	draft.ClaimedFacts = append(draft.ClaimedFacts, ClaimedFact{
		ClaimID: "claim_derived_total", Kind: FactReadiness, Subject: wb.project, Field: "total_items",
		Value:       ScalarValue{Integer: func() *int64 { v := int64(10); return &v }()},
		DerivedFrom: []contractsv1.ContextFabricDerivationInput{{FactID: open, Field: "open_items"}, {FactID: closed, Field: "closed_items"}},
		Operation:   contractsv1.ContextFabricDerivationSum,
	})
	if withClaim {
		draft.Drivers[0].ClaimedFactIDs = []string{"claim_derived_total"}
	} else {
		draft.Drivers[0].ClaimedFactIDs = nil
		draft.Drivers[0].Category = "narrative"
	}
	draft.Drivers[0].SourceState = claimed
	result, _, err := wb.investigate(writeBackRequest(bundle, nil))
	if err != nil {
		t.Fatalf("write-back: %v", err)
	}
	return result.Drivers[0].SourceState
}

func TestSuppliedDerivedClaimOverATruncatedInputServesTheTruncatedState(t *testing.T) {
	t.Parallel()
	if got := suppliedDerivedDriverSourceState(t, SourceTruncated, "", true); got != SourceTruncated {
		t.Errorf("served driver source_state = %q, want truncated from a derivation input", got)
	}
}

func TestSuppliedDriverWithoutClaimsCannotAssertASourceState(t *testing.T) {
	t.Parallel()
	if got := suppliedDerivedDriverSourceState(t, SourceTruncated, SourceTruncated, false); got != "" {
		t.Errorf("served driver source_state = %q, want cleared for a driver with no claim ids", got)
	}
}
