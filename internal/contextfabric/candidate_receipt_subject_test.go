package contextfabric

import (
	"fmt"
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

func TestConfirmedCandidateReceiptIsAChoiceTheSubstitutionGuardServes(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_candidate_choice_one", true), substitutionResponse(substitutionRepoOne, "receipt_candidate_choice_parent"))
	offering := candidateOfferingNeedResponse()
	offering.material.CandidateOptions[0].CanonicalID, offering.material.CandidateOptions[0].Label = substitutionRepoTwo.CanonicalID, substitutionRepoTwo.Label
	twoRequest := continuingNeedTurn(needTurnRequest("request_candidate_choice_two", true), one.result.ResultID)
	twoRequest.Question = "And how does the second one compare over the same period?"
	two := h.turn(twoRequest, offering)
	if two.result.StructureNeeds == nil || len(two.result.StructureNeeds.CandidateOptions) == 0 {
		t.Fatalf("fixture defect: turn two must offer candidates; status=%s", two.result.Status)
	}
	threeRequest := continuingNeedTurnAgain(needTurnRequest("request_candidate_choice_three", true), twoRequest, two.result.ResultID)
	threeRequest.Question = twoRequest.Question
	threeRequest.Options.AllowClarification = false
	threeRequest.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: two.result.StructureNeeds.CandidateOptions[0].ReceiptID}}
	three := h.turn(threeRequest, substitutionResponse(substitutionRepoTwo, "receipt_candidate_choice_served"))
	if three.result.Status == InvestigationNoMatch || three.result.Status == InvestigationClarificationRequired {
		t.Fatalf("status = %q, want the redeemed candidate served; guard=%+v", three.result.Status, lastSubstitution(t, three))
	}
	assertGuard(t, lastSubstitution(t, three), SubjectSubstitutionRedeemedChoice, SubjectSubstitutionOriginResolver, substitutionRepoOne, substitutionRepoTwo)
}

func TestConfirmedCandidateHintKeepsItsSlotAheadOfPriorSubjectHints(t *testing.T) {
	t.Parallel()
	h := newNeedTurnHarness(t, nil)
	one := h.turn(needTurnRequest("request_candidate_budget_one", true), candidateOfferingNeedResponse())
	offer := one.result.StructureNeeds.CandidateOptions[0]
	request := needTurnRequest("request_candidate_budget_two", true)
	for i := 0; i < 49; i++ {
		request.RequestedScope.SubjectHints = append(request.RequestedScope.SubjectHints, SubjectHint{Kind: SubjectRepository, ID: fmt.Sprintf("repository:caller-%02d", i), Label: "caller", Source: "caller"})
	}
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: offer.ReceiptID}}
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: one.result.ResultID, ReceiptID: "subr_need_pool_0002"}}
	two := h.turn(request, committingNeedResponse())
	hints := lastPortCall(t, two).request.RequestedScope.SubjectHints
	if len(hints) != 50 || hints[49].ID != offer.CanonicalID || hints[49].Source != string(hintsource.ConfirmedCandidateReceipt) {
		t.Fatalf("hints = %d entries, last = %#v, want 49 caller hints and the redeemed candidate in the last slot", len(hints), hints[len(hints)-1])
	}
}
