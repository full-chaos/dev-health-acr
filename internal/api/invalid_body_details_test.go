package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestInvestigationRouteInvalidBodyDetails(t *testing.T) {
	valid, err := json.Marshal(investigationRequestBody())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		body      string
		wantCode  int
		wantReas  string
		wantField string
		absent    []string
	}{
		{"malformed_json", `{"schema_version": `, 400, "malformed_json", "", nil},
		{"empty_body", ``, 400, "malformed_json", "", nil},
		{"type_violation_names_field", strings.Replace(string(valid), `"max_drivers":10`, `"max_drivers":"ten"`, 1), 400, "schema_violation", "options.max_drivers", []string{"ten", "int"}},
		{"unknown_field_names_field", strings.Replace(string(valid), `{`, `{"surprise_field":1,`, 1), 400, "unknown_field", "surprise_field", nil},
		{"unknown_field_odd_name_dropped", strings.Replace(string(valid), `{`, `{"secret value here":1,`, 1), 400, "unknown_field", "", []string{"secret value here"}},
		{"trailing_json", string(valid) + `{}`, 400, "trailing_json", "", nil},
		{"validation_failure", strings.Replace(string(valid), `"question":"Most of the work is closed, so why is Ask Dev still not ready to ship?"`, `"question":""`, 1), 400, "schema_violation", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				t.Fatal("investigator must not run")
				return contextfabric.InvestigationResult{}, nil
			}))
			request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader([]byte(tc.body)))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			if response.Code != tc.wantCode {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			var body contractsv1.ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "invalid_request" {
				t.Fatalf("code = %q", body.Error.Code)
			}
			if got, _ := body.Error.Details["reason"].(string); got != tc.wantReas {
				t.Errorf("details.reason = %q, want %q (details=%v)", got, tc.wantReas, body.Error.Details)
			}
			if got, _ := body.Error.Details["field"].(string); got != tc.wantField {
				t.Errorf("details.field = %q, want %q", got, tc.wantField)
			}
			if len(body.Error.Details) > 2 {
				t.Errorf("details has extra keys: %v", body.Error.Details)
			}
			for _, a := range tc.absent {
				if strings.Contains(response.Body.String(), a) {
					t.Errorf("body leaks %q: %s", a, response.Body.String())
				}
			}
			if !contractsv1.IsInvalidRequestReason(tc.wantReas) {
				t.Errorf("reason %q not in the closed vocabulary", tc.wantReas)
			}
		})
	}
}
