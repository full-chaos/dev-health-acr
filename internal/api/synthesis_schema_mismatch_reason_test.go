package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A synthesis rejection the output-schema check caused reaches the failure line
// under that check's own word, and the 422 body still carries no
// rejection_reason: on the investigation route the reason stays a log field.
func TestSchemaMismatchSynthesisRejectionNamesItsRuleOnTheFailureLineOnly(t *testing.T) {
	rejection := contextfabric.NewSynthesisRejection(
		contextfabric.RejectionReasonOutputSchemaMismatch,
		fmt.Errorf("%w: %w: schema", contextfabric.ErrSynthesisRejected, contextfabric.ErrModelOutput),
	)
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, rejection
	}))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationRequest(t, token))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "rejection_reason") || strings.Contains(response.Body.String(), "output_schema_mismatch") {
		t.Fatalf("the 422 body discloses the rejection reason: %s", response.Body.String())
	}
	entry := decodeFailureLog(t, logs.String())
	if got := entry["rejection_reason"]; got != string(contextfabric.RejectionReasonOutputSchemaMismatch) {
		t.Fatalf("failure line rejection_reason = %v, want %q", got, contextfabric.RejectionReasonOutputSchemaMismatch)
	}
}
