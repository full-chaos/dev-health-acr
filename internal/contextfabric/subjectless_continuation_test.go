package contextfabric

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// haikuTermlessFrame is the frame shape the Anthropic strict wire decodes a
// follow-up into when the question names no subject: a named_subject with no
// terms, goal explain_drivers.
const haikuTermlessFrame = `{"goals":["explain_drivers"],"subject_expression":{"kind":"named_subject","named":{}}}`

func decodeFrame(t *testing.T, raw string) QuestionFrame {
	t.Helper()
	var frame QuestionFrame
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return frame
}

func TestTermlessNamedSubjectOnlyDecidesFromTheModelsOwnFrame(t *testing.T) {
	receipt := ModelExecutionReceipt{}
	for _, tc := range []struct {
		name  string
		frame string
		want  bool
	}{
		{"the haiku follow-up frame", haikuTermlessFrame, true},
		{"a named subject with a term is valid, not termless", `{"goals":["explain_drivers"],"subject_expression":{"kind":"named_subject","named":{"terms":["alpha"]}}}`, false},
		{"a blank term is a different failure", `{"goals":["explain_drivers"],"subject_expression":{"kind":"named_subject","named":{"terms":[""]}}}`, false},
		{"another kind is untouched", `{"goals":["explain_drivers"],"subject_expression":{"kind":"explicit_set","explicit":{"operands":[]}}}`, false},
		{"another failure on the same frame (no goals)", `{"goals":[],"subject_expression":{"kind":"named_subject","named":{}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := decodeFrame(t, tc.frame)
			result := validateProposedFrame(receipt, frame, ShapeOpen, nil)
			if got := termlessNamedSubjectOnly(receipt, frame, ShapeOpen, result); got != tc.want {
				t.Fatalf("termlessNamedSubjectOnly = %v, want %v (outcome %s, failure %s/%s)", got, tc.want, result.Outcome, result.Failure.Invariant, result.Failure.Detail)
			}
		})
	}
}

func TestATermlessFrameStillRefusesAtValidation(t *testing.T) {
	frame := decodeFrame(t, haikuTermlessFrame)
	result := validateProposedFrame(ModelExecutionReceipt{}, frame, ShapeOpen, nil)
	if result.Outcome != FrameValidationOutcomeRefusedInvalid || result.Failure.Invariant != FrameInvariantI3 || result.Failure.Detail != FrameFailureNoTerms {
		t.Fatalf("validation = %s %s/%s, want refused_invalid i3/no_terms: the frame itself is never accepted", result.Outcome, result.Failure.Invariant, result.Failure.Detail)
	}
}

func termlessOutcome(base QuestionFamilyOutcome, termless bool, invariant FrameInvariant) QuestionFamilyOutcome {
	base.Frame = nil
	base.FrameObligations = nil
	base.Gate = FrameGate{Outcome: FrameGateRejectedInvalid, FailedInvariant: invariant}
	base.WinningSample.TermlessNamedSubject = termless
	return base
}

func TestATermlessFollowUpOffersTheRememberedSubjectWithoutSearching(t *testing.T) {
	callsBefore := -1
	before := func(rig *anchorProbeRig) {
		rig.interpreter.outcome = termlessOutcome(rig.interpreter.outcome, true, FrameInvariantI3)
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

func TestATermlessFollowUpOffersNothingOutsideItsExactConditions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before func(*anchorProbeRig)
		mutate func(*InvestigationRequest)
	}{
		{"the frame was not term-less", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = termlessOutcome(rig.interpreter.outcome, false, FrameInvariantI3)
		}, nil},
		{"another invariant refused the frame", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = termlessOutcome(rig.interpreter.outcome, true, FrameInvariantI4)
		}, nil},
		{"the request states no continuation", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = termlessOutcome(rig.interpreter.outcome, true, FrameInvariantI3)
		}, func(r *InvestigationRequest) { r.ParentResultID = "" }},
		{"the held subject can no longer be read", func(rig *anchorProbeRig) {
			rig.interpreter.outcome = termlessOutcome(rig.interpreter.outcome, true, FrameInvariantI3)
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
			if two.result.Status == InvestigationClarificationRequired && len(served.Candidates) == 0 && two.transitions[0].BoundAnchorCarry == BoundAnchorCarryFollowUpOffered {
				t.Fatalf("carry = follow_up_offered, want no offer")
			}
		})
	}
}

func TestTermlessContinuationNeedsAnExplicitRequestSideContinuation(t *testing.T) {
	outcome := termlessOutcome(QuestionFamilyOutcome{}, true, FrameInvariantI3)
	for _, tc := range []struct {
		name    string
		request InvestigationRequest
		want    bool
	}{
		{"a named parent result", InvestigationRequest{ParentResultID: "result_parent"}, true},
		{"no parent and no referenced result", InvestigationRequest{}, false},
		{"a blank parent", InvestigationRequest{ParentResultID: "  "}, false},
	} {
		if got := termlessContinuation(outcome, tc.request); got != tc.want {
			t.Fatalf("%s: termlessContinuation = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAReceiptWithoutTheTermlessFlagSerializesUnchanged(t *testing.T) {
	raw, err := json.Marshal(ModelExecutionReceipt{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "termless_named_subject") {
		t.Fatalf("receipt json = %s, want no termless_named_subject key unless set", raw)
	}
}
