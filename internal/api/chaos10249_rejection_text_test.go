package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A rejected model output read as "the question exceeds a limit".
func TestInterpretationRejectionTextSaysTheModelOutputWasRejected(t *testing.T) {
	app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, contextfabric.ErrInterpretationRejected
	}))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationRequest(t, token))
	var body contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	message := body.Error.Message
	if !strings.Contains(message, "model's output") || !strings.Contains(message, "rejected") || !strings.Contains(message, "not over a limit") {
		t.Fatalf("message = %q, want it to say the model output was rejected and not a question limit", message)
	}
}
