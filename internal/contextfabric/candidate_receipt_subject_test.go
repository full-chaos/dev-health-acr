package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
)

func TestConfirmedCandidateReceiptReachesResolutionAsExactSubjectHint(t *testing.T) {
	t.Parallel()
	for _, allow := range []bool{false, true} {
		h := newNeedTurnHarness(t, nil)
		one := h.turn(needTurnRequest("request_candidate_hint_one", true), candidateOfferingNeedResponse())
		if one.result.StructureNeeds == nil || len(one.result.StructureNeeds.CandidateOptions) != 2 {
			t.Fatalf("fixture defect: turn one must offer two candidates; status=%s", one.result.Status)
		}
		offer := one.result.StructureNeeds.CandidateOptions[1]
		request := needTurnRequest("request_candidate_hint_two", true)
		request.Options.AllowClarification = allow
		request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: offer.ReceiptID}}
		two := h.turn(request, committingNeedResponse())
		call := lastPortCall(t, two)

		var got []SubjectHint
		for _, hint := range call.request.RequestedScope.SubjectHints {
			if hint.Kind == offer.Kind && hint.ID == offer.CanonicalID {
				got = append(got, hint)
			}
		}
		if len(got) != 1 || got[0].Source != string(hintsource.ConfirmedCandidateReceipt) {
			t.Fatalf("allow_clarification=%v: ResolveSubjects hints = %#v, want exactly one exact hint for the redeemed candidate %s from the confirmed candidate source", allow, call.request.RequestedScope.SubjectHints, offer.CanonicalID)
		}
	}
}
