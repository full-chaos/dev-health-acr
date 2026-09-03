package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// subjectScopeRejection produces a REAL subject-scope rejection by running
// the production validator, rather than hand-building the error type. That
// distinction is the point: a hand-built error would prove only that this
// route prints a struct field, while this proves the field an operator
// actually reads is populated by the code path that rejects a live answer.
//
// inScope decides which side of the display/validate line the offending
// subject sits on. A cohort GROUP subject is citable, so the driver is
// admitted; a resolution CANDIDATE is serialized to the model and
// deliberately not citable, so it rejects with subject_in_payload true; a
// subject in neither is invented, and rejects with false.
func subjectScopeRejection(t *testing.T, shown bool) error {
	t.Helper()
	subject := contextfabric.SubjectRef{
		Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_offending", Label: "Offending",
	}
	input := contextfabric.SynthesisInput{}
	// Engine-supplied evidence, so the driver can close to something real and
	// the evidence rules cannot reject before the subject rule is reached.
	input.Graph.EvidenceRefIDs = []string{"evidence_release_1234"}
	if shown {
		input.Graph.Resolution.Candidates = []contextfabric.SubjectCandidate{{
			ReceiptID: "receipt_12345678", Subject: subject,
			MatchReasons: []string{"lexical"}, Confidence: 0.4,
		}}
	}
	draft := contextfabric.SynthesisDraft{
		Status:              contractsv1.ContextFabricInvestigationDegraded,
		DirectJudgment:      "A judgment.",
		DeterministicAnswer: "An answer.",
		Drivers: []contextfabric.DriverJudgment{{
			DriverID: "driver_12345678", Standing: contractsv1.ContextFabricDriverPrincipal,
			Category: "relationship", Title: "A driver",
			Summary: "A driver summary.", AffectedSubjects: []contextfabric.SubjectRef{subject},
			// Structurally valid, so driver.Validate() cannot reject first
			// and mask the subject-scope rule this fixture is about.
			Derivation: contractsv1.ContextFabricDerivationRuleInferred,
			EpistemicStatus: contractsv1.ContextFabricEpistemicInferred,
			Confidence: 0.9, Current: true,
			EvidenceRefIDs: []string{"evidence_release_1234"},
		}},
	}
	err := draft.ValidateAgainst(input)
	if err == nil {
		t.Fatal("fixture drift: ValidateAgainst accepted a draft whose driver names an uncitable subject")
	}
	if got := contextfabric.SynthesisRejectionReasonOf(err); got != contextfabric.RejectionReasonDriverSubjectOutOfScope {
		t.Fatalf("fixture drift: rejection reason = %q (%v), want a subject-scope rejection", got, err)
	}
	// Wrapped by the SAME exported classifier the engine uses, so this test
	// exercises the production error shape rather than an approximation of it.
	return contextfabric.ClassifySynthesisRejection(draft, input, err)
}

func failureEntryFor(t *testing.T, err error) map[string]any {
	t.Helper()
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, err
	}))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationRequest(t, token))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", response.Code)
	}
	return decodeFailureLog(t, logs.String())
}

// TestFailureEventReportsASubjectTheModelInvented is the expected steady
// state: nothing in the payload mentioned the subject, so the rejection is
// the model's and the operator should read it as model variance.
func TestFailureEventReportsASubjectTheModelInvented(t *testing.T) {
	entry := failureEntryFor(t, subjectScopeRejection(t, false))
	if got := entry["subject_in_payload"]; got != false {
		t.Fatalf("subject_in_payload = %v, want false for a subject that appears nowhere in the payload", got)
	}
}

// TestFailureEventReportsASubjectWeShowedAndThenRefused is the alarm this
// field exists to raise. A true here says ACR serialized a subject into the
// synthesis payload and then rejected the model for citing it -- an ACR
// defect, not a model one. That is exactly the shape the grouped family hit,
// where a cohort's group entity was in every payload and admitted by
// nothing, and it is why this reads as a boolean an operator can alert on
// rather than as one more rejection reason among many.
func TestFailureEventReportsASubjectWeShowedAndThenRefused(t *testing.T) {
	entry := failureEntryFor(t, subjectScopeRejection(t, true))
	if got := entry["subject_in_payload"]; got != true {
		t.Fatalf("subject_in_payload = %v, want true for a subject the payload showed the model", got)
	}
}

// TestFailureEventOmitsSubjectMembershipForANonSubjectRejection keeps the
// field honest at the boundary: a rejection it does not describe must not
// carry a false-looking default.
func TestFailureEventOmitsSubjectMembershipForANonSubjectRejection(t *testing.T) {
	draft := contextfabric.SynthesisDraft{Status: "not_a_status"}
	err := draft.ValidateAgainst(contextfabric.SynthesisInput{})
	if got := contextfabric.SynthesisRejectionReasonOf(err); got != contextfabric.RejectionReasonStatusInvalid {
		t.Fatalf("fixture drift: rejection reason = %q, want a non-subject rejection", got)
	}
	entry := failureEntryFor(t, contextfabric.ClassifySynthesisRejection(draft, contextfabric.SynthesisInput{}, err))
	if _, present := entry["subject_in_payload"]; present {
		t.Fatalf("subject_in_payload = %v is present on a rejection that is not subject-scope", entry["subject_in_payload"])
	}
}
