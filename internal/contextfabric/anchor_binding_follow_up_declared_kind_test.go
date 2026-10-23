package contextfabric

import (
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// declaredNoneCohortResponse is the live shape of a follow-up: the response
// carries an org-wide cohort of member, and declaredNone makes the model's
// reading declare NO scope anchor kind over a scoped frame of that member kind.
func declaredNoneCohortResponse(member SubjectKind) needTurnResponse {
	graph := emptyGraphContext()
	graph.Cohort = &Cohort{Kind: member, Complete: true, Rationale: "fixture", Members: []CohortMember{
		{Subject: SubjectRef{Kind: member, CanonicalID: string(member) + ":probe-one", Label: "one"}, Rank: 1, InclusionReasons: []string{"fixture"}},
		{Subject: SubjectRef{Kind: member, CanonicalID: string(member) + ":probe-two", Label: "two"}, Rank: 2, InclusionReasons: []string{"fixture"}},
	}}
	response := emptyProbeResponse()
	response.context = &graph
	return response
}

func declaredNone(member SubjectKind) func(*anchorProbeRig) {
	return func(rig *anchorProbeRig) {
		rig.interpreter.read("", false)
		frame := countingFrame(member)
		rig.interpreter.outcome.Frame, rig.interpreter.outcome.FrameObligations = frame, frame.Obligations
	}
}

// TestAFollowUpOfferEndsTheTurnWhenTheModelDeclaredNoAnchorKind: the live
// shape. The follow-up's frame is a scoped set with an anchor term, the model
// declared NO scope anchor kind, and an org-wide cohort of the member kind is
// discoverable. The offer of the remembered subject must end the turn as a
// clarification, before discovery; it must not fall through to cohort
// discovery and be served.
func TestAFollowUpOfferEndsTheTurnWhenTheModelDeclaredNoAnchorKind(t *testing.T) {
	for _, member := range []SubjectKind{SubjectTeam, SubjectProject} {
		t.Run(string(member), func(t *testing.T) {
			rig, one, two := followUpTurns(t, carryTurnOptions{followUpOn: true, before: declaredNone(member)}, declaredNoneCohortResponse(member))
			if line := two.transitions[0]; line.Admission != AnchorBindingAdmissionAdmittedFollowUp || line.BoundAnchorCarry != BoundAnchorCarryFollowUpOffered {
				t.Fatalf("admission/carry = %s/%s, want admitted_follow_up/follow_up_offered", line.Admission, line.BoundAnchorCarry)
			}
			if two.result.Status != InvestigationClarificationRequired {
				t.Fatalf("status = %s, want clarification_required: the offer must not be served as an answer", two.result.Status)
			}
			served := two.result.SubjectResolution
			if len(served.Committed) != 0 {
				t.Fatalf("committed = %+v, want none", served.Committed)
			}
			if len(served.Candidates) == 0 || !reflect.DeepEqual(served.Candidates[0], rememberedAlphaOffer(one.result.ResultID)) {
				t.Fatalf("candidates = %+v, want the remembered alpha offer first", served.Candidates)
			}
			if served.ClarificationPrompt != followUpClarificationPrompt {
				t.Fatalf("prompt = %q, want the follow-up prompt", served.ClarificationPrompt)
			}
			if two.result.Cohort != nil && len(two.result.Cohort.Members) != 0 {
				t.Fatalf("cohort = %+v, want none served", two.result.Cohort)
			}
			// Turn one ran discovery once; the follow-up must add none.
			if rig.graph.discovers != 1 {
				t.Fatalf("DiscoverContext calls = %d, want 1 (turn one only)", rig.graph.discovers)
			}
		})
	}
}

// TestAFollowUpOfferEndsTheTurnAfterTheStructureOfferIsReconciled: the offer
// is reconciled with the turn's structure offer (carry follow_up_offered_reconciled);
// that is still the offer, and still ends the turn.
func TestAFollowUpOfferEndsTheTurnAfterTheStructureOfferIsReconciled(t *testing.T) {
	response := declaredNoneCohortResponse(SubjectTeam)
	response.material.Missing = []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}
	response.material.AnchorOptions = []AnchorOption{anchorOptionFor(probeAlpha), anchorOptionFor(probeBeta)}
	rig, one, two := followUpTurns(t, carryTurnOptions{followUpOn: true, before: declaredNone(SubjectTeam)}, response)
	if line := two.transitions[0]; line.BoundAnchorCarry != BoundAnchorCarryFollowUpOfferedReconciled {
		t.Fatalf("carry = %s, want follow_up_offered_reconciled", line.BoundAnchorCarry)
	}
	if two.result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %s, want clarification_required", two.result.Status)
	}
	if c := two.result.SubjectResolution.Candidates; len(c) == 0 || !reflect.DeepEqual(c[0], rememberedAlphaOffer(one.result.ResultID)) {
		t.Fatalf("candidates = %+v, want the remembered alpha offer first", c)
	}
	if rig.graph.discovers != 1 {
		t.Fatalf("DiscoverContext calls = %d, want 1 (turn one only)", rig.graph.discovers)
	}
}

// TestAFollowUpOfferThatEndsTheTurnReportsTheCallersReceipts: a follow-up
// that carries prior subject receipts has them reported on the clarification.
func TestAFollowUpOfferThatEndsTheTurnReportsTheCallersReceipts(t *testing.T) {
	withReceipt := func(r *InvestigationRequest) {
		r.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: r.ParentResultID, ReceiptID: subjectSubstitutionReceiptID(r.ParentResultID, probeAlpha)}}
	}
	_, _, two := followUpTurns(t, carryTurnOptions{followUpOn: true, before: declaredNone(SubjectTeam), mutate: withReceipt}, declaredNoneCohortResponse(SubjectTeam))
	if two.result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %s, want clarification_required", two.result.Status)
	}
	if len(two.result.SubjectResolution.PriorSubjectReceiptDispositions) != 1 {
		t.Fatalf("receipt dispositions = %+v, want one entry for the carried receipt", two.result.SubjectResolution.PriorSubjectReceiptDispositions)
	}
}

// TestAFollowUpOfferForACallerThatCannotClarifyServesNoCohort: a caller that
// declined clarification is never served the org-wide cohort in place of the
// offer.
func TestAFollowUpOfferForACallerThatCannotClarifyServesNoCohort(t *testing.T) {
	noClarify := func(r *InvestigationRequest) { r.Options.AllowClarification = false }
	rig, _, two := followUpTurns(t, carryTurnOptions{followUpOn: true, before: declaredNone(SubjectTeam), mutate: noClarify}, declaredNoneCohortResponse(SubjectTeam))
	if two.result.Status == InvestigationComplete || two.result.Status == InvestigationPartial || two.result.Status == InvestigationClarificationRequired {
		t.Fatalf("status = %s, want a refusal that serves no answer and asks no question", two.result.Status)
	}
	if len(two.result.SubjectResolution.Committed) != 0 || rig.graph.discovers != 1 {
		t.Fatalf("committed/discovers = %+v/%d, want none/1", two.result.SubjectResolution.Committed, rig.graph.discovers)
	}
}

// TestAFollowUpThatOffersNothingStillReadsTheCohort: the offer ends the turn
// only when it was made. A follow-up the offer refuses (frame takes no anchor,
// or the parent mismatches the binding) keeps today's path, discovery included.
func TestAFollowUpThatOffersNothingStillReadsTheCohort(t *testing.T) {
	noAnchorTerms := func(rig *anchorProbeRig) {
		declaredNone(SubjectTeam)(rig)
		rig.interpreter.outcome.Frame.SubjectExpression.Scoped.AnchorTerms = nil
	}
	holdBeta := mutateStoredBindings(func(s *PersistedSemanticState) {
		editBindingMember(s, func(b *AnchorBinding) {
			b.Kind, b.CanonicalID, b.Witness = probeBeta.Kind, probeBeta.CanonicalID, []string{string(probeBeta.Kind) + ":" + probeBeta.CanonicalID}
		})
	})
	for _, tc := range []struct {
		name  string
		carry BoundAnchorCarry
		opts  carryTurnOptions
	}{
		{"frame refused", BoundAnchorCarryFollowUpFrameRefused, carryTurnOptions{followUpOn: true, before: noAnchorTerms}},
		{"parent mismatch", BoundAnchorCarryFollowUpParentMismatch, carryTurnOptions{followUpOn: true, before: declaredNone(SubjectTeam), edit: holdBeta}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, _, two := followUpTurns(t, tc.opts, declaredNoneCohortResponse(SubjectTeam))
			if line := two.transitions[0]; line.BoundAnchorCarry != tc.carry {
				t.Fatalf("carry = %s, want %s", line.BoundAnchorCarry, tc.carry)
			}
			if rig.graph.discovers != 2 || two.result.Status == InvestigationClarificationRequired {
				t.Fatalf("discovers/status = %d/%s, want 2/an answer: no offer was made", rig.graph.discovers, two.result.Status)
			}
		})
	}
}
