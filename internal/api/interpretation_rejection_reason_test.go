package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestContextFabricInterpretationRejectionNamesItsRuleInTheFailureEvent is
// the API-boundary half of the interpretation rejection-reason work, and it
// exists because an adversarial review round found this layer still silent
// after the model decision line had been fixed.
//
// The failure event already carried rejection_reason for a SYNTHESIS
// rejection. For an interpretation rejection it said only
// failure_classification=interpretation_rejected — the exact complaint that
// produced this ticket, one layer further out.
//
// It matters most for the two fact-registry producers used here: those
// rejections never pass through genkitruntime at all, so the model decision
// line where the reason otherwise appears is never emitted for them. This
// event is their ONLY telemetry surface, and without this fix they reach an
// operator unnamed.
func TestContextFabricInterpretationRejectionNamesItsRuleInTheFailureEvent(t *testing.T) {
	want := contractsv1.ContextFabricInterpretationRejectionFactCapabilityParameterNotAllowed
	rejection := contextfabric.NewInterpretationRejection(
		want,
		fmt.Errorf("%w: fact capability status: parameter %q is not allowed", contextfabric.ErrInterpretationRejected, "sql"),
	)

	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, rejection
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 -- wrapping the reason must not change the classification", response.Code)
	}
	entry := decodeFailureLog(t, logs.String())
	if got := entry["failure_classification"]; got != "interpretation_rejected" {
		t.Fatalf("failure_classification = %v, want the pre-existing value to be preserved", got)
	}
	if got := entry["rejection_reason"]; got != string(want) {
		t.Fatalf("rejection_reason = %v, want %q -- an interpretation rejection reaching the API failure event must name its rule, exactly as a synthesis rejection already does", got, want)
	}
}

// TestContextFabricFailureEventOmitsRejectionReasonForAnUnrelatedFailure
// pins the other half: the field is emitted only for a rejection, so an
// unrelated failure never carries a meaningless "unclassified".
func TestContextFabricFailureEventOmitsRejectionReasonForAnUnrelatedFailure(t *testing.T) {
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, fmt.Errorf("resolve subjects: %w", contextfabric.ErrUnavailable)
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	entry := decodeFailureLog(t, logs.String())
	if _, present := entry["rejection_reason"]; present {
		t.Fatalf("rejection_reason = %v is present on a non-rejection failure", entry["rejection_reason"])
	}
}
