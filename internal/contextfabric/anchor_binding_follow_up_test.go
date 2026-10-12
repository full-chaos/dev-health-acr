package contextfabric

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// followUpQuestion is a natural follow-up that names no subject of its own.
const followUpQuestion = "And how many teams contribute to it?"

func askFollowUp(r *InvestigationRequest) { r.Question = followUpQuestion }

// followUpTurns runs turn one (proves and serves alpha) and a changed-question
// follow-up with the carry switch on and the follow-up switch as given.
func followUpTurns(t *testing.T, o carryTurnOptions, response needTurnResponse) (*anchorProbeRig, anchorProbeTurn, anchorProbeTurn) {
	t.Helper()
	o.carryOn = true
	mutate := o.mutate
	o.mutate = func(r *InvestigationRequest) {
		askFollowUp(r)
		if mutate != nil {
			mutate(r)
		}
	}
	return bindingCarryTurnsWith(t, o, response)
}

// rememberedAlphaOffer is the offer the follow-up makes for alpha: the
// subject-substitution guard's remembered-offer form, under the label the
// first answer served.
func rememberedAlphaOffer(parentResultID string) SubjectCandidate {
	return SubjectCandidate{
		ReceiptID: subjectSubstitutionReceiptID(parentResultID, probeAlpha), Subject: probeAlpha,
		State: contractsv1.ContextFabricResolutionAmbiguous, MatchedTerms: []string{},
		MatchReasons: []string{"the subject the earlier answer was about"}, Confidence: 0, EvidenceRefIDs: []string{},
	}
}

// TestAFollowUpNamingNoSubjectOffersTheRememberedOneFirst: the follow-up's
// own resolution names nothing. Switched off, the question change refuses
// admission and the turn ends no_match. Switched on, it is admitted as a
// follow-up and ends asking which subject was meant, the remembered alpha
// offered first under the label the first answer served; nothing is
// carried into resolution and nothing is served from memory.
func TestAFollowUpNamingNoSubjectOffersTheRememberedOneFirst(t *testing.T) {
	for _, tc := range []struct {
		name       string
		followUpOn bool
		admission  AnchorBindingAdmission
		carry      BoundAnchorCarry
		status     InvestigationStatus
		offered    bool
	}{
		{"switched off", false, AnchorBindingAdmissionRefusedQuestionChanged, BoundAnchorCarryNotAdmitted, InvestigationNoMatch, false},
		{"switched on", true, AnchorBindingAdmissionAdmittedFollowUp, BoundAnchorCarryFollowUpOffered, InvestigationClarificationRequired, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, one, two := followUpTurns(t, carryTurnOptions{followUpOn: tc.followUpOn}, emptyProbeResponse())
			line := two.transitions[0]
			if line.Admission != tc.admission || line.BoundAnchorCarry != tc.carry || two.result.Status != tc.status {
				t.Fatalf("admission/carry/status = %s/%s/%s, want %s/%s/%s", line.Admission, line.BoundAnchorCarry, two.result.Status, tc.admission, tc.carry, tc.status)
			}
			if hints := carriedHintsOf(rig); len(hints) != 0 {
				t.Fatalf("carried hints = %+v, want none: a follow-up never carries", hints)
			}
			served := two.result.SubjectResolution
			if len(served.Committed) != 0 {
				t.Fatalf("committed = %+v, want none: never a serve on memory", served.Committed)
			}
			if !tc.offered {
				if len(served.Candidates) != 0 {
					t.Fatalf("candidates = %+v, want none", served.Candidates)
				}
				return
			}
			if len(served.Candidates) != 1 || !reflect.DeepEqual(served.Candidates[0], rememberedAlphaOffer(one.result.ResultID)) {
				t.Fatalf("candidates = %+v, want the remembered alpha offer", served.Candidates)
			}
			if served.ClarificationPrompt != followUpClarificationPrompt {
				t.Fatalf("prompt = %q, want the follow-up prompt", served.ClarificationPrompt)
			}
			if held := bindingMember(two.saved); held == nil || held.State != AnchorBindingBound || held.CanonicalID != probeAlpha.CanonicalID || held.OriginResultID != one.result.ResultID {
				t.Fatalf("binding after the follow-up = %+v, want alpha held from turn one", held)
			}
		})
	}
}

// TestAFollowUpOfferIsRedeemedOnTheNextTurn: the caller picks the remembered
// alpha by its receipt; the next turn resolves through that receipt and
// serves alpha.
func TestAFollowUpOfferIsRedeemedOnTheNextTurn(t *testing.T) {
	rig, one, two := followUpTurns(t, carryTurnOptions{followUpOn: true}, emptyProbeResponse())
	receipt := rememberedAlphaOffer(one.result.ResultID).ReceiptID
	request := continuingNeedTurnAgain(needTurnRequest("request_carry_three", true), needTurnRequest("request_carry_two", true), two.result.ResultID)
	askFollowUp(&request)
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: two.result.ResultID, ReceiptID: receipt}}
	three := rig.turn(t, request, needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)})
	hints := rig.graph.calls[len(rig.graph.calls)-1].request.RequestedScope.SubjectHints
	if len(hints) != 1 || hints[0].ID != probeAlpha.CanonicalID || hints[0].Source != string(hintsource.PriorSubjectReceipt) {
		t.Fatalf("turn three hints = %+v, want the redeemed receipt's alpha only", hints)
	}
	if three.result.Status != InvestigationComplete || len(three.result.SubjectResolution.Committed) != 1 || three.result.SubjectResolution.Committed[0].CanonicalID != probeAlpha.CanonicalID {
		t.Fatalf("turn three status/committed = %s/%+v, want complete on alpha", three.result.Status, three.result.SubjectResolution.Committed)
	}
}

// TestAFollowUpOfferPutsTheRememberedOneBeforeTheResolversOwn: the resolver
// offers candidates; the remembered alpha is offered first, once, in the
// remembered form, whether or not the resolver offered it.
func TestAFollowUpOfferPutsTheRememberedOneBeforeTheResolversOwn(t *testing.T) {
	alpha, beta := probeOffer("receipt_follow_alpha", probeAlpha), probeOffer("receipt_follow_beta", probeBeta)
	for _, tc := range []struct {
		name     string
		response needTurnResponse
	}{
		{"the resolver offers alpha second", offeringResponse(beta, alpha)},
		{"the resolver offers only beta", offeringResponse(beta)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, one, two := followUpTurns(t, carryTurnOptions{followUpOn: true}, tc.response)
			served := two.result.SubjectResolution
			if len(served.Candidates) != 2 || !reflect.DeepEqual(served.Candidates[0], func() SubjectCandidate {
				c := rememberedAlphaOffer(one.result.ResultID)
				c.Provider = providerOfSubject(tc.response.resolution.Candidates, probeAlpha)
				return c
			}()) || served.Candidates[1].ReceiptID != beta.ReceiptID {
				t.Fatalf("candidates = %+v, want the remembered alpha first, then beta", served.Candidates)
			}
			if served.ClarificationPrompt != followUpClarificationPrompt || two.transitions[0].BoundAnchorCarry != BoundAnchorCarryFollowUpOffered {
				t.Fatalf("prompt/carry = %q/%s, want the follow-up prompt/follow_up_offered", served.ClarificationPrompt, two.transitions[0].BoundAnchorCarry)
			}
		})
	}
}

// TestAFollowUpThatNamesItsOwnSubjectIsUnchanged: the follow-up's own text
// commits a subject. The same subject serves; another one meets the
// subject-substitution guard. Nothing is offered by the follow-up either way.
func TestAFollowUpThatNamesItsOwnSubjectIsUnchanged(t *testing.T) {
	sameAlpha := needTurnResponse{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha)}
	for _, tc := range []struct {
		name     string
		response needTurnResponse
		status   InvestigationStatus
		guard    SubjectSubstitutionOutcome
	}{
		{"the same subject", sameAlpha, InvestigationComplete, SubjectSubstitutionSameSubject},
		{"another subject", identityProvenResponse(probeBeta), InvestigationClarificationRequired, SubjectSubstitutionClarified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, two := followUpTurns(t, carryTurnOptions{followUpOn: true}, tc.response)
			line := two.transitions[0]
			if two.result.Status != tc.status || line.SubstitutionGuard != tc.guard || line.BoundAnchorCarry != BoundAnchorCarryFollowUpCommitted {
				t.Fatalf("status/guard/carry = %s/%s/%s, want %s/%s/follow_up_committed", two.result.Status, line.SubstitutionGuard, line.BoundAnchorCarry, tc.status, tc.guard)
			}
			if two.result.SubjectResolution.ClarificationPrompt == followUpClarificationPrompt {
				t.Fatal("the follow-up offer fired on a turn whose own text named a subject")
			}
		})
	}
}

// TestAFollowUpOfferNeedsAFrameThatTakesTheKind: a follow-up whose frame does
// not take the binding's kind as its scope anchor offers nothing.
func TestAFollowUpOfferNeedsAFrameThatTakesTheKind(t *testing.T) {
	unscoped := func(rig *anchorProbeRig) { rig.interpreter.outcome.Frame = nil }
	_, _, two := followUpTurns(t, carryTurnOptions{followUpOn: true, before: unscoped}, emptyProbeResponse())
	if line := two.transitions[0]; line.Admission != AnchorBindingAdmissionAdmittedFollowUp || line.BoundAnchorCarry != BoundAnchorCarryFollowUpFrameRefused {
		t.Fatalf("admission/carry = %s/%s, want admitted_follow_up/follow_up_frame_refused", line.Admission, line.BoundAnchorCarry)
	}
	if len(two.result.SubjectResolution.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", two.result.SubjectResolution.Candidates)
	}
}

// TestAFollowUpOfferNeedsTheParentToHaveServedTheIdentity: a binding whose
// identity the parent did not serve has no label the caller already saw;
// nothing is offered.
func TestAFollowUpOfferNeedsTheParentToHaveServedTheIdentity(t *testing.T) {
	holdBeta := mutateStoredBindings(func(s *PersistedSemanticState) {
		editBindingMember(s, func(b *AnchorBinding) {
			b.Kind, b.CanonicalID, b.Witness = probeBeta.Kind, probeBeta.CanonicalID, []string{string(probeBeta.Kind) + ":" + probeBeta.CanonicalID}
		})
	})
	_, _, two := followUpTurns(t, carryTurnOptions{followUpOn: true, edit: holdBeta}, emptyProbeResponse())
	if line := two.transitions[0]; line.Admission != AnchorBindingAdmissionAdmittedFollowUp || line.BoundAnchorCarry != BoundAnchorCarryFollowUpParentMismatch {
		t.Fatalf("admission/carry = %s/%s, want admitted_follow_up/follow_up_parent_mismatch", line.Admission, line.BoundAnchorCarry)
	}
	if len(two.result.SubjectResolution.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", two.result.SubjectResolution.Candidates)
	}
}

// TestAFollowUpIsNotAdmittedFromAPendingOrExpiredBinding: a pending binding
// is not admitted for a changed question, and a follow-up still ages out.
func TestAFollowUpIsNotAdmittedFromAPendingOrExpiredBinding(t *testing.T) {
	toPending := mutateStoredBindings(func(s *PersistedSemanticState) {
		editBindingMember(s, func(b *AnchorBinding) { b.State = AnchorBindingPendingWindowConfirmation })
	})
	for _, tc := range []struct {
		name string
		edit func(*staticResultStore)
		want AnchorBindingAdmission
	}{
		{"pending", toPending, AnchorBindingAdmissionRefusedQuestionChanged},
		{"expired", expiredBinding, AnchorBindingAdmissionRefusedExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, two := followUpTurns(t, carryTurnOptions{followUpOn: true, pendingOn: true, edit: tc.edit}, emptyProbeResponse())
			if line := two.transitions[0]; line.Admission != tc.want || two.result.Status != InvestigationNoMatch || len(two.result.SubjectResolution.Candidates) != 0 {
				t.Fatalf("admission/status/candidates = %s/%s/%d, want %s/no_match/0", line.Admission, two.result.Status, len(two.result.SubjectResolution.Candidates), tc.want)
			}
		})
	}
}

// TestAnAdmittedFollowUpNeverFeedsTheHintOrTheKind: with the count switch on
// and the follow-up read under another anchor kind, an admitted follow-up
// leaves the model's kind on the reading (the count kind reads admitted
// only) and carries no hint.
func TestAnAdmittedFollowUpNeverFeedsTheHintOrTheKind(t *testing.T) {
	projectKind := func(rig *anchorProbeRig) { rig.interpreter.read(SubjectProject, false) }
	rig, _, two := followUpTurns(t, carryTurnOptions{followUpOn: true, countOn: true, before: projectKind}, emptyProbeResponse())
	line := two.transitions[0]
	if line.Admission != AnchorBindingAdmissionAdmittedFollowUp || line.BoundAnchorKind != BoundAnchorKindNone {
		t.Fatalf("admission/bound kind = %s/%s, want admitted_follow_up/none", line.Admission, line.BoundAnchorKind)
	}
	if hints := carriedHintsOf(rig); len(hints) != 0 {
		t.Fatalf("carried hints = %+v, want none", hints)
	}
}

// TestNewEngineRefusesTheFollowUpWithoutTheCarry: the follow-up widens what
// the carry admits, so it alone is a configuration error.
func TestNewEngineRefusesTheFollowUpWithoutTheCarry(t *testing.T) {
	deps := EngineDependencies{
		Interpreter: &anchorProbeInterpreter{},
		Graph:       &needTurnGraph{},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return validInvestigationResult(), nil
		}),
	}
	options := EngineOptions{ServiceVersion: "follow-up", NewResultID: resultIDSequence(), AnchorBindingCarryEnabled: true, AnchorBindingFollowUpEnabled: true}
	if _, err := NewEngine(deps, options); err != nil {
		t.Fatalf("premise: the follow-up beside the carry must construct, got %v", err)
	}
	options.AnchorBindingCarryEnabled = false
	if _, err := NewEngine(deps, options); err == nil || !strings.Contains(err.Error(), "follow-up needs the anchor binding carry") {
		t.Fatalf("NewEngine() error = %v, want the follow-up-needs-carry refusal", err)
	}
}

// TestAFollowUpThatEndsBeforeResolutionReadsNotEvaluated: an admitted
// follow-up whose turn ends at the frame-gate refusal, before the offer is
// decided, reads not_evaluated, never not_admitted.
func TestAFollowUpThatEndsBeforeResolutionReadsNotEvaluated(t *testing.T) {
	refuseFrame := func(rig *anchorProbeRig) {
		rig.interpreter.outcome.Gate = FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectTeam}
	}
	_, _, two := followUpTurns(t, carryTurnOptions{followUpOn: true, before: refuseFrame}, emptyProbeResponse())
	if line := two.transitions[0]; line.Admission != AnchorBindingAdmissionAdmittedFollowUp || line.BoundAnchorCarry != BoundAnchorCarryNotEvaluated {
		t.Fatalf("admission/carry = %s/%s, want admitted_follow_up/not_evaluated", line.Admission, line.BoundAnchorCarry)
	}
}
