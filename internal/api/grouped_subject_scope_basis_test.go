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
// shown picks which basis the rejection carries: a resolution candidate is
// serialized to the model and deliberately uncitable, so it rejects
// "shown_uncitable_by_policy"; a subject in no payload source at all is
// invented, and rejects "absent".
func subjectScopeRejection(t *testing.T, shown bool, alarm bool) error {
	t.Helper()
	subject := contextfabric.SubjectRef{
		Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_offending", Label: "Offending",
	}
	input := contextfabric.SynthesisInput{}
	if alarm {
		// A payload path the census sees by shape and no allow-set admits.
		input.Interpretation.FactRequirements = []contextfabric.FactRequirement{{
			Kind: contractsv1.ContextFabricFactReadiness, Subjects: []contextfabric.SubjectRef{subject},
		}}
	}
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
			Derivation:      contractsv1.ContextFabricDerivationRuleInferred,
			EpistemicStatus: contractsv1.ContextFabricEpistemicInferred,
			Confidence:      0.9, Current: true,
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
	entry := failureEntryFor(t, subjectScopeRejection(t, false, false))
	if got := entry["subject_scope_basis"]; got != "absent" {
		t.Fatalf("subject_scope_basis = %v, want \"absent\" for a subject that appears nowhere in the payload", got)
	}
}

// TestFailureEventReportsAShownSubjectAsUncitableByPolicy is the case that
// replaced this file's original "we showed it and refused it" assertion.
// A resolution candidate IS shown to the model, so the boolean this field
// replaced reported true -- and true was documented as an ACR defect, so an
// alert written to that documentation would have fired on ordinary model
// misuse. The operator must be able to tell the two apart on the line
// itself, which is why the alarm value is its own constant.
func TestFailureEventReportsAShownSubjectAsUncitableByPolicy(t *testing.T) {
	entry := failureEntryFor(t, subjectScopeRejection(t, true, false))
	if got := entry["subject_scope_basis"]; got != "shown_uncitable_by_policy" {
		t.Fatalf("subject_scope_basis = %v, want \"shown_uncitable_by_policy\" -- a candidate is shown on purpose and uncitable on purpose", got)
	}
	if got := entry["subject_scope_basis"]; got == "shown_should_be_citable" {
		t.Fatal("a policy exclusion must never render as the ACR-defect alarm value")
	}
}

// TestFailureEventOmitsScopeBasisForANonSubjectRejection keeps the field
// honest at the boundary: a rejection it does not describe must not carry a
// default-looking value.
func TestFailureEventOmitsScopeBasisForANonSubjectRejection(t *testing.T) {
	draft := contextfabric.SynthesisDraft{Status: "not_a_status"}
	err := draft.ValidateAgainst(contextfabric.SynthesisInput{})
	if got := contextfabric.SynthesisRejectionReasonOf(err); got != contextfabric.RejectionReasonStatusInvalid {
		t.Fatalf("fixture drift: rejection reason = %q, want a non-subject rejection", got)
	}
	entry := failureEntryFor(t, contextfabric.ClassifySynthesisRejection(draft, contextfabric.SynthesisInput{}, err))
	if _, present := entry["subject_scope_basis"]; present {
		t.Fatalf("subject_scope_basis = %v is present on a rejection that is not subject-scope", entry["subject_scope_basis"])
	}
}

// TestFailureEventReportsTheAlarmOnTheWire is the route half of the alarm.
// The validator-level tests prove the basis is COMPUTED; this proves an
// operator can actually see it on the failure line, which is the only place
// the value does any work. Round 2 found the route covered `absent` and the
// policy exclusion and never the one value that means the defect is ours.
func TestFailureEventReportsTheAlarmOnTheWire(t *testing.T) {
	entry := failureEntryFor(t, subjectScopeRejection(t, false, true))
	if got := entry["subject_scope_basis"]; got != "shown_should_be_citable" {
		t.Fatalf("subject_scope_basis = %v, want the alarm value on a subject the payload carries and no list admits", got)
	}
}
