package contextfabric

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// haikuTermlessScopeFrame is the frame "What about the teams it has?" draws:
// children_of_scope over teams with no anchor term for the pronoun.
const haikuTermlessScopeFrame = `{"goals":["assess_state"],"subject_expression":{"kind":"children_of_scope","scoped":{"member_kind":"team"}}}`

func scopeOutcome(base QuestionFamilyOutcome, kind SubjectKind, invariant FrameInvariant) QuestionFamilyOutcome {
	base.Frame = nil
	base.FrameObligations = nil
	base.Gate = FrameGate{Outcome: FrameGateRejectedInvalid, FailedInvariant: invariant}
	base.WinningSample.TermlessScopeMemberKind = kind
	return base
}

func TestTermlessScopeMemberKindOnlyDecidesFromTheModelsOwnFrame(t *testing.T) {
	receipt := ModelExecutionReceipt{}
	for _, tc := range []struct {
		name  string
		frame string
		want  SubjectKind
	}{
		{"the team follow-up frame", haikuTermlessScopeFrame, SubjectTeam},
		{"an anchor term present is valid, not termless", `{"goals":["assess_state"],"subject_expression":{"kind":"children_of_scope","scoped":{"anchor_terms":["alpha"],"member_kind":"team"}}}`, ""},
		{"a blank anchor term is not an empty list", `{"goals":["assess_state"],"subject_expression":{"kind":"children_of_scope","scoped":{"anchor_terms":[" "],"member_kind":"team"}}}`, ""},
		{"a second defect (no member kind) keeps the refusal", `{"goals":["assess_state"],"subject_expression":{"kind":"children_of_scope","scoped":{}}}`, ""},
		{"a second defect (invalid member kind)", `{"goals":["assess_state"],"subject_expression":{"kind":"children_of_scope","scoped":{"member_kind":"galaxy"}}}`, ""},
		{"a second defect (status qualifier on team members)", `{"goals":["assess_state"],"subject_expression":{"kind":"children_of_scope","scoped":{"member_kind":"team","member_qualifier":"status","member_qualifier_value":"open"}}}`, ""},
		{"a second defect (unknown member qualifier)", `{"goals":["assess_state"],"subject_expression":{"kind":"children_of_scope","scoped":{"member_kind":"team","member_qualifier":"colour"}}}`, ""},
		{"a second defect (no goals)", `{"goals":[],"subject_expression":{"kind":"children_of_scope","scoped":{"member_kind":"team"}}}`, ""},
		{"another kind is untouched", haikuTermlessFrame, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := decodeFrame(t, tc.frame)
			result := validateProposedFrame(receipt, frame, ShapeOpen, nil)
			if got := termlessScopeMemberKindOnly(receipt, frame, ShapeOpen, result); got != tc.want {
				t.Fatalf("termlessScopeMemberKindOnly = %q, want %q (outcome %s, failure %s/%s)", got, tc.want, result.Outcome, result.Failure.Invariant, result.Failure.Detail)
			}
		})
	}
}

func TestATermlessScopeFrameStillRefusesAtValidation(t *testing.T) {
	frame := decodeFrame(t, haikuTermlessScopeFrame)
	result := validateProposedFrame(ModelExecutionReceipt{}, frame, ShapeOpen, nil)
	if result.Outcome != FrameValidationOutcomeRefusedInvalid || result.Failure.Invariant != FrameInvariantI5 || result.Failure.Detail != FrameFailureNoAnchorTerms {
		t.Fatalf("validation = %s %s/%s, want refused_invalid i5/no_anchor_terms: the frame itself is never accepted", result.Outcome, result.Failure.Invariant, result.Failure.Detail)
	}
}

func TestATermlessTeamFollowUpOffersTheRememberedRepositoryWithoutSearching(t *testing.T) {
	callsBefore := -1
	before := func(rig *anchorProbeRig) {
		rig.interpreter.outcome = scopeOutcome(rig.interpreter.outcome, SubjectTeam, FrameInvariantI5)
		callsBefore = len(rig.graph.calls)
	}
	rig, one, two := followUpTurns(t, carryTurnOptions{before: before}, emptyProbeResponse())
	if callsBefore < 0 || len(rig.graph.calls) != callsBefore {
		t.Fatalf("graph calls %d -> %d: a term-less follow-up must not run retrieval", callsBefore, len(rig.graph.calls))
	}
	line := two.transitions[0]
	if line.Admission != AnchorBindingAdmissionAdmittedFollowUp || line.BoundAnchorCarry != BoundAnchorCarryFollowUpOffered || two.result.Status != InvestigationClarificationRequired {
		t.Fatalf("admission/carry/status = %s/%s/%s, want admitted_follow_up/follow_up_offered/clarification_required", line.Admission, line.BoundAnchorCarry, two.result.Status)
	}
	served := two.result.SubjectResolution
	if len(served.Committed) != 0 {
		t.Fatalf("committed = %+v, want none: never a serve on memory", served.Committed)
	}
	if len(served.Candidates) != 1 || !reflect.DeepEqual(served.Candidates[0], rememberedAlphaOffer(one.result.ResultID)) {
		t.Fatalf("candidates = %+v, want the remembered alpha offer", served.Candidates)
	}
}

func TestATermlessScopeFollowUpOffersNothingOutsideItsExactConditions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before func(*anchorProbeRig)
		mutate func(*InvestigationRequest)
	}{
		{"the model stated the held kind as the member kind", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = scopeOutcome(rig.interpreter.outcome, SubjectRepository, FrameInvariantI5)
		}, nil},
		{"no member kind was recorded", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = scopeOutcome(rig.interpreter.outcome, "", FrameInvariantI5)
		}, nil},
		{"another invariant refused the frame", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = scopeOutcome(rig.interpreter.outcome, SubjectTeam, FrameInvariantI4)
		}, nil},
		{"the request states no continuation", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = scopeOutcome(rig.interpreter.outcome, SubjectTeam, FrameInvariantI5)
		}, func(r *InvestigationRequest) { r.ParentResultID = "" }},
		{"the held subject can no longer be read", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = scopeOutcome(rig.interpreter.outcome, SubjectTeam, FrameInvariantI5)
			rig.engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string) (bool, CandidateVerificationReason) {
				return false, CandidateVerificationClaimLost
			}
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, two := followUpTurns(t, carryTurnOptions{before: tc.before, mutate: tc.mutate}, emptyProbeResponse())
			served := two.result.SubjectResolution
			if len(served.Candidates) != 0 || len(served.Committed) != 0 {
				t.Fatalf("candidates/committed = %+v/%+v, want none", served.Candidates, served.Committed)
			}
			if two.transitions[0].BoundAnchorCarry == BoundAnchorCarryFollowUpOffered {
				t.Fatalf("carry = follow_up_offered, want no offer")
			}
		})
	}
}

func TestTermlessContinuationReadsTheScopedVariantFromItsOwnFields(t *testing.T) {
	request := InvestigationRequest{ParentResultID: "result_parent"}
	for _, tc := range []struct {
		name    string
		outcome QuestionFamilyOutcome
		request InvestigationRequest
		want    termlessOffer
	}{
		{"scoped, team", scopeOutcome(QuestionFamilyOutcome{}, SubjectTeam, FrameInvariantI5), request, termlessOffer{Active: true, MemberKind: SubjectTeam}},
		{"scoped flag at the named invariant", scopeOutcome(QuestionFamilyOutcome{}, SubjectTeam, FrameInvariantI3), request, termlessOffer{}},
		{"named flag at the scoped invariant", termlessOutcome(QuestionFamilyOutcome{}, true, FrameInvariantI5), request, termlessOffer{}},
		{"scoped without a continuation", scopeOutcome(QuestionFamilyOutcome{}, SubjectTeam, FrameInvariantI5), InvestigationRequest{}, termlessOffer{}},
	} {
		if got := termlessContinuation(tc.outcome, tc.request); got != tc.want {
			t.Fatalf("%s: termlessContinuation = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	held := SubjectRepository
	if (termlessOffer{Active: true, MemberKind: SubjectRepository}).admits(held) || !(termlessOffer{Active: true, MemberKind: SubjectTeam}).admits(held) || !(termlessOffer{Active: true}).admits(held) || (termlessOffer{MemberKind: SubjectTeam}).admits(held) {
		t.Fatal("termlessOffer.admits does not keep the kind-difference rule")
	}
}

func TestTheProductionPathCarriesTheTermlessScopeKindIntoTheOffer(t *testing.T) {
	frame := decodeFrame(t, haikuTermlessScopeFrame)
	receipt := &ModelExecutionReceipt{QuestionFrame: &frame}
	RuntimeQuestionInterpreter{}.resolveFrame(context.Background(), storage.Principal{}, receipt, ShapeOpen, nil)
	if receipt.TermlessScopeMemberKind != SubjectTeam || receipt.TermlessNamedSubject || receipt.FrameGateOutcome != FrameGateRejectedInvalid || receipt.FrameFailedInvariant != FrameInvariantI5 {
		t.Fatalf("receipt scope kind/named/gate/invariant = %q/%v/%s/%s, want team/false/rejected_invalid/i5", receipt.TermlessScopeMemberKind, receipt.TermlessNamedSubject, receipt.FrameGateOutcome, receipt.FrameFailedInvariant)
	}
	sample := familySampleFrom(InterpretedQuestion{}, *receipt)
	if sample.TermlessScopeMemberKind != SubjectTeam {
		t.Fatal("familySampleFrom dropped the receipt's TermlessScopeMemberKind")
	}
	before := func(rig *anchorProbeRig) {
		outcome := scopeOutcome(rig.interpreter.outcome, "", FrameInvariantI5)
		outcome.WinningSample = sample
		outcome.Gate = FrameGate{Outcome: receipt.FrameGateOutcome, FailedInvariant: receipt.FrameFailedInvariant}
		rig.interpreter.outcome = outcome
	}
	_, one, two := followUpTurns(t, carryTurnOptions{before: before}, emptyProbeResponse())
	served := two.result.SubjectResolution
	if two.transitions[0].BoundAnchorCarry != BoundAnchorCarryFollowUpOffered || len(served.Candidates) != 1 || !reflect.DeepEqual(served.Candidates[0], rememberedAlphaOffer(one.result.ResultID)) {
		t.Fatalf("carry/candidates = %s/%+v, want the remembered alpha offer", two.transitions[0].BoundAnchorCarry, served.Candidates)
	}
}

// existingReceiptJSON was captured from main 565b3246, before the scope field
// existed; a receipt without the new fields must serialize to exactly it.
const existingReceiptJSON = `{"operation":"interpret","provider":"p","model":"m","model_version":"v","prompt_version":"","schema_version":"","evaluator_version":"","started_at":"2026-10-23T01:02:03Z","completed_at":"2026-10-23T01:02:04Z","attempts":0,"input_digest":"","usage":{},"fallback_used":false,"outcome":"success","request_id":"req_1","frame_outcome":"refused_invalid","frame_failed_invariant":"i3","frame_gate_outcome":"rejected_invalid"}`

func TestAnExistingReceiptSerializesByteIdentical(t *testing.T) {
	receipt := ModelExecutionReceipt{
		Operation: ModelOperationInterpret, Provider: "p", Model: "m", ModelVersion: "v", Outcome: "success",
		StartedAt: time.Date(2026, 10, 23, 1, 2, 3, 0, time.UTC), CompletedAt: time.Date(2026, 10, 23, 1, 2, 4, 0, time.UTC),
		FrameOutcome: FrameValidationOutcomeRefusedInvalid, FrameFailedInvariant: FrameInvariantI3, FrameGateOutcome: FrameGateRejectedInvalid,
		RequestID: "req_1",
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != existingReceiptJSON {
		t.Fatalf("receipt json changed:\n got %s\nwant %s", raw, existingReceiptJSON)
	}
}
