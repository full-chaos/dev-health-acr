package contextfabric

import "testing"

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
