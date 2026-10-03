package directread_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func TestGraphQLQueryUpstreamStatusAndCodeReachErrorsAndLog(t *testing.T) {
	oversized := `{"errors":[{"message":"` + canaryText + strings.Repeat("x", 70*1024) + `","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`
	const query = `{ hotspots(input: {sinceUtc: "2026-09-21T00:00:00Z", untilUtc: "2026-09-28T00:00:00Z"}) { rows { filePath } } }`
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		call     directread.CallStatus
		refusal  directread.RefusalCode
		wantErr  directread.OperationError
		wantLog  map[string]any
		absentLn []string
	}{
		{
			name: "validation_422_required_variable", status: 422,
			body: gqlEnvelope(canaryText, `["variable","orgId"]`, "GRAPHQL_VALIDATION_FAILED"),
			call: directread.CallRefused, refusal: directread.RefusalInvalidRequest,
			wantErr: directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422, GraphQLCode: directread.GraphQLCodeValidationFailed, Variable: "orgId"},
			wantLog: map[string]any{"decision": "refused", "refusal_code": "invalid_request", "error_class": "http_status", "upstream_status": 422, "graphql_code": "graphql_validation_failed", "variable": "orgId"},
		},
		{
			name: "parse_422", status: 422,
			body: gqlEnvelope(canaryText, `[]`, "GRAPHQL_PARSE_FAILED"),
			call: directread.CallRefused, refusal: directread.RefusalInvalidRequest,
			wantErr:  directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422, GraphQLCode: directread.GraphQLCodeParseFailed},
			wantLog:  map[string]any{"decision": "refused", "refusal_code": "invalid_request", "error_class": "http_status", "upstream_status": 422, "graphql_code": "graphql_parse_failed"},
			absentLn: []string{"variable"},
		},
		{
			name: "validation_code_on_a_500_is_not_the_callers_fault", status: 500,
			body:    gqlEnvelope(canaryText, `["variable","orgId"]`, "GRAPHQL_VALIDATION_FAILED"),
			call:    directread.CallUpstreamError,
			wantErr: directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 500, GraphQLCode: directread.GraphQLCodeValidationFailed, Variable: "orgId"},
			wantLog: map[string]any{"decision": "upstream_error", "error_class": "http_status", "upstream_status": 500, "graphql_code": "graphql_validation_failed", "variable": "orgId"},
		},
		{
			name: "unknown_422_code_is_other_and_stays_upstream_error", status: 422,
			body:     gqlEnvelope(canaryText, `[]`, canaryText),
			call:     directread.CallUpstreamError,
			wantErr:  directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422, GraphQLCode: directread.GraphQLCodeOther},
			wantLog:  map[string]any{"decision": "upstream_error", "error_class": "http_status", "upstream_status": 422, "graphql_code": "other"},
			absentLn: []string{"variable"},
		},
		{
			name: "502_not_json", status: 502, body: canaryText + " bad gateway",
			call:     directread.CallUpstreamError,
			wantErr:  directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 502},
			wantLog:  map[string]any{"decision": "upstream_error", "error_class": "http_status", "upstream_status": 502},
			absentLn: []string{"graphql_code", "variable"},
		},
		{
			name: "oversized_body_keeps_the_status_only", status: 422, body: oversized,
			call:     directread.CallUpstreamError,
			wantErr:  directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422},
			wantLog:  map[string]any{"decision": "upstream_error", "error_class": "http_status", "upstream_status": 422},
			absentLn: []string{"graphql_code", "variable"},
		},
		{
			name: "hostile_variable_name_is_dropped", status: 422,
			body: gqlEnvelope(canaryText, `["variable","org Id\"; DROP"]`, "GRAPHQL_VALIDATION_FAILED"),
			call: directread.CallRefused, refusal: directread.RefusalInvalidRequest,
			wantErr:  directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422, GraphQLCode: directread.GraphQLCodeValidationFailed},
			wantLog:  map[string]any{"decision": "refused", "refusal_code": "invalid_request", "error_class": "http_status", "upstream_status": 422, "graphql_code": "graphql_validation_failed"},
			absentLn: []string{"variable"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGQLHarness(t, gqlHarnessOptions{fake: func(cfg *fakeMCPConfig) {
				cfg.Status = func() (int, string) { return tc.status, tc.body }
			}})
			resp := h.run(t, opUnrestricted(opOrgA), query, nil)
			out, _ := json.Marshal(resp)
			if resp.Call != tc.call || resp.Data != nil || len(resp.Errors) != 1 || resp.Errors[0] != tc.wantErr {
				t.Fatalf("got %s, want call %s error %+v", out, tc.call, tc.wantErr)
			}
			if tc.refusal != "" && (resp.Refusal == nil || resp.Refusal.Code != tc.refusal || resp.Refusal.Reason != directread.RefusalReasonUpstreamRejectedRequest) {
				t.Fatalf("refusal: %s", out)
			}
			if tc.refusal == "" && resp.Refusal != nil {
				t.Fatalf("unexpected refusal: %s", out)
			}
			if strings.Contains(string(out), canaryText) || strings.Contains(h.logs.String(), canaryText) {
				t.Fatalf("upstream text reached the response or the log:\n%s\n%s", out, h.logs.String())
			}
			line := opLineOf(t, h.logs.String(), directread.GraphQLQueryLogMessage)
			want := map[string]any{"org_id": opOrgA}
			for k, v := range tc.wantLog {
				want[k] = v
			}
			gqlCertify(t, line, want)
			var parsed map[string]any
			if err := json.Unmarshal(line, &parsed); err != nil {
				t.Fatal(err)
			}
			for _, key := range tc.absentLn {
				if _, present := parsed[key]; present {
					t.Fatalf("log line carries %s: %s", key, line)
				}
			}
		})
	}
}
