package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestChaos4386MarshalContextFabricResponseIsExportedForTheTrialHarness pins
// the CHAOS-4386 reuse contract: MarshalContextFabricResponse (exported
// wrapper) and the route's own private marshalContextFabricResponse must
// measure identically -- the trial harness (internal/runtime/hosted) calls
// the exported form so its own result_bytes/est_tokens can never drift from
// what this route actually gates on.
func TestChaos4386MarshalContextFabricResponseIsExportedForTheTrialHarness(t *testing.T) {
	result := threeRollupProjectStatusResult("result_4386_export_pin")
	wantEncoded, wantBytes, wantErr := marshalContextFabricResponse(result)
	gotEncoded, gotBytes, gotErr := MarshalContextFabricResponse(result)
	if wantErr != nil || gotErr != nil {
		t.Fatalf("marshal errors: private=%v exported=%v", wantErr, gotErr)
	}
	if gotBytes != wantBytes {
		t.Fatalf("MarshalContextFabricResponse measured %d bytes, want %d (must match the route's own private encoder exactly)", gotBytes, wantBytes)
	}
	if string(gotEncoded) != string(wantEncoded) {
		t.Fatal("MarshalContextFabricResponse produced different bytes than the route's own private encoder")
	}
}

// TestChaos4386HTTPSampleRejectsSyntheticOversizedResult is this ticket's own
// acceptance test for the "(and the HTTP sample must 413 on it)" half of the
// CHAOS-4386 acceptance bullet: a synthetic ~300 KB result, driven through
// httptest and the REAL POST /investigations route (real limits.Manager,
// real ACR_MAX_SERIALIZED_BYTES=262144 production ceiling -- see
// newContextFabricTestAppWithProductionLimits), must 413 and disclose its
// measurement, never a silent trim or an undisclosed 500.
//
// Reuses twelveRollupResult (CHAOS-4355's own adversarial ~275 KB fixture,
// context_fabric_response_bound_test.go) rather than a new fixture
// generator: it is already sized comfortably over ACR_MAX_SERIALIZED_BYTES
// and in the ~300 KB band this ticket's own acceptance bullet names.
func TestChaos4386HTTPSampleRejectsSyntheticOversizedResult(t *testing.T) {
	result := twelveRollupResult("result_4386_http_sample")
	measuredBytes := marshaledSize(t, result)
	t.Logf("synthetic oversized result: %d bytes (~%d KB)", measuredBytes, measuredBytes/1000)
	if measuredBytes <= productionMaxSerializedBytes {
		t.Fatalf("fixture measured %d bytes, want > ACR_MAX_SERIALIZED_BYTES (%d)", measuredBytes, productionMaxSerializedBytes)
	}
	if measuredBytes < 200_000 || measuredBytes > 400_000 {
		t.Fatalf("fixture measured %d bytes, want a result in the ~300 KB band this ticket's acceptance bullet names", measuredBytes)
	}

	app, token := newContextFabricTestAppWithProductionLimits(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}), nil)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("http_sample_status = %d, want 413 -- a %d-byte synthetic result must trip the real ACR_MAX_SERIALIZED_BYTES gate through the real route; body=%s", response.Code, measuredBytes, response.Body.String())
	}
	assertErrorDetailsDiscloseMeasurement(t, response.Body.Bytes(), productionMaxSerializedBytes)

	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	httpSampleBytes, ok := envelope.Error.Details["measured_bytes"].(float64)
	if !ok {
		t.Fatalf("error details = %#v, want a numeric measured_bytes (this is what a run-level http_sample_bytes field would carry)", envelope.Error.Details)
	}
	if int64(httpSampleBytes) != measuredBytes {
		t.Fatalf("http_sample_bytes = %v, want %d (the SAME measurement the fixture itself produced, proving the route's gate and this ticket's own encoder agree)", httpSampleBytes, measuredBytes)
	}
}
