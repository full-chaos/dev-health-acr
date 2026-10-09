package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func suppliedInvestigationHTTPRequest(t *testing.T, token string, supplied contractsv1.ContextFabricSuppliedInterpretation) *http.Request {
	t.Helper()
	body := investigationRequestBody()
	body.SuppliedInterpretation = &supplied
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func validSuppliedInterpretation() contractsv1.ContextFabricSuppliedInterpretation {
	return contractsv1.ContextFabricSuppliedInterpretation{
		Output:             json.RawMessage(`{"shape":"open","requested_judgment":"status","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":false}`),
		ModelOutputVersion: "context-fabric-model-output.v8", PromptVersion: "context-fabric-interpretation.v23",
		ClientModel: "claude-test",
	}
}

func TestSuppliedInterpretationContractMismatchIsServedAsATypedRefusal(t *testing.T) {
	refusal := contractsv1.ContextFabricInterpretationContractRefusal{
		Mismatch: []string{contractsv1.ContextFabricInterpretationContractFieldPromptVersion},
		Current: contractsv1.ContextFabricInterpretationContract{
			ModelOutputVersion: "context-fabric-model-output.v8", PromptVersion: "context-fabric-interpretation.v23",
			SystemSHA256: strings.Repeat("a", 64),
		},
	}
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, &contextfabric.StageError{
			Stage: contextfabric.StageInterpretation,
			Err:   &contextfabric.SuppliedInterpretationContractMismatch{Refusal: refusal},
		}
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, suppliedInvestigationHTTPRequest(t, token, validSuppliedInterpretation()))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
			Details   struct {
				Contract contractsv1.ContextFabricInterpretationContractRefusal `json:"interpretation_contract"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, response.Body.String())
	}
	if envelope.Error.Code != "invalid_request" || envelope.Error.Retryable {
		t.Fatalf("error = %#v, want a non-retryable invalid_request", envelope.Error)
	}
	if !reflect.DeepEqual(envelope.Error.Details.Contract, refusal) {
		t.Fatalf("details.interpretation_contract = %#v, want %#v", envelope.Error.Details.Contract, refusal)
	}
	if err := envelope.Error.Details.Contract.Validate(); err != nil {
		t.Fatalf("served refusal does not validate: %v", err)
	}
	entry := decodeFailureLog(t, logs.String())
	if got := entry["failure_classification"]; got != "interpretation_contract_mismatch" {
		t.Fatalf("failure_classification = %v, want interpretation_contract_mismatch", got)
	}
	if got := entry["failure_stage"]; got != "interpretation" {
		t.Fatalf("failure_stage = %v, want interpretation", got)
	}
}

func TestSuppliedInterpretationReachesTheInvestigatorIntact(t *testing.T) {
	supplied := validSuppliedInterpretation()
	var received *contractsv1.ContextFabricSuppliedInterpretation
	app, token := newContextFabricTestApp(t, investigatorFunc(func(_ context.Context, _ storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		received = request.SuppliedInterpretation
		return validContextFabricInvestigationResult(), nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, suppliedInvestigationHTTPRequest(t, token, supplied))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", response.Code, response.Body.String())
	}
	if received == nil || !reflect.DeepEqual(*received, supplied) {
		t.Fatalf("investigator received %#v, want %#v", received, supplied)
	}
}

func TestSuppliedInterpretationOutsideItsBoundsIsRefusedBeforeTheInvestigator(t *testing.T) {
	oversize := json.RawMessage(`{"requested_judgment":"` + strings.Repeat("a", contractsv1.ContextFabricSuppliedInterpretationMaxBytes) + `"}`)
	cases := []struct {
		name   string
		mutate func(*contractsv1.ContextFabricSuppliedInterpretation)
	}{
		{"output over the byte bound", func(s *contractsv1.ContextFabricSuppliedInterpretation) { s.Output = oversize }},
		{"output is not an object", func(s *contractsv1.ContextFabricSuppliedInterpretation) { s.Output = json.RawMessage(`["open"]`) }},
		{"model output version is blank", func(s *contractsv1.ContextFabricSuppliedInterpretation) { s.ModelOutputVersion = "  " }},
		{"prompt version is over its length bound", func(s *contractsv1.ContextFabricSuppliedInterpretation) { s.PromptVersion = strings.Repeat("v", 257) }},
		{"system sha256 is not 64 hex", func(s *contractsv1.ContextFabricSuppliedInterpretation) { s.SystemSHA256 = "ABC" }},
		{"client model outside its character class", func(s *contractsv1.ContextFabricSuppliedInterpretation) { s.ClientModel = "model name\nwith a line" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				t.Fatal("the investigator ran for a request that violates the supplied interpretation bounds")
				return contextfabric.InvestigationResult{}, nil
			}))
			supplied := validSuppliedInterpretation()
			tc.mutate(&supplied)
			response := httptest.NewRecorder()

			app.Handler().ServeHTTP(response, suppliedInvestigationHTTPRequest(t, token, supplied))

			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"invalid_request"`) {
				t.Fatalf("status = %d body=%s, want a 400 invalid_request", response.Code, response.Body.String())
			}
		})
	}
}
