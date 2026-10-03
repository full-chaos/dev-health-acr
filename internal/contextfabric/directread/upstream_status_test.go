package directread_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const canaryText = "SECRET-UPSTREAM-CANARY"

func gqlEnvelope(message, path, code string) string {
	return `{"errors":[{"message":"` + message + `","path":` + path + `,"extensions":{"code":"` + code + `"}}],"data":null}`
}

func TestUpstreamStatusAndGraphQLCodeReachErrorsAndLog(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	hot, _ := cat.Lookup("hotspots")
	vars := opMinimalVariables(t, hot)
	oversized := `{"errors":[{"message":"` + canaryText + strings.Repeat("x", 70*1024) + `","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`
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
			wantErr: directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422, GraphQLCode: directread.GraphQLCodeParseFailed},
			wantLog: map[string]any{"decision": "refused", "refusal_code": "invalid_request", "error_class": "http_status", "upstream_status": 422, "graphql_code": "graphql_parse_failed"},
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
			body:    gqlEnvelope(canaryText, `[]`, canaryText),
			call:    directread.CallUpstreamError,
			wantErr: directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422, GraphQLCode: directread.GraphQLCodeOther},
			wantLog: map[string]any{"decision": "upstream_error", "error_class": "http_status", "upstream_status": 422, "graphql_code": "other"},
		},
		{
			name: "500_resolver_error_code", status: 500,
			body:    gqlEnvelope(canaryText, `[]`, "INTERNAL_SERVER_ERROR"),
			call:    directread.CallUpstreamError,
			wantErr: directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 500, GraphQLCode: directread.GraphQLCodeOther},
			wantLog: map[string]any{"decision": "upstream_error", "error_class": "http_status", "upstream_status": 500, "graphql_code": "other"},
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
		{
			name: "overlong_variable_name_is_dropped", status: 422,
			body: gqlEnvelope(canaryText, `["variable","`+strings.Repeat("a", 65)+`"]`, "GRAPHQL_VALIDATION_FAILED"),
			call: directread.CallRefused, refusal: directread.RefusalInvalidRequest,
			wantErr:  directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422, GraphQLCode: directread.GraphQLCodeValidationFailed},
			wantLog:  map[string]any{"decision": "refused", "refusal_code": "invalid_request", "error_class": "http_status", "upstream_status": 422, "graphql_code": "graphql_validation_failed"},
			absentLn: []string{"variable"},
		},
		{
			name: "non_variable_path_carries_no_name", status: 422,
			body: gqlEnvelope(canaryText, `["query","hotspots"]`, "GRAPHQL_VALIDATION_FAILED"),
			call: directread.CallRefused, refusal: directread.RefusalInvalidRequest,
			wantErr:  directread.OperationError{Class: directread.UpstreamHTTPStatus, Status: 422, GraphQLCode: directread.GraphQLCodeValidationFailed},
			wantLog:  map[string]any{"decision": "refused", "refusal_code": "invalid_request", "error_class": "http_status", "upstream_status": 422, "graphql_code": "graphql_validation_failed"},
			absentLn: []string{"variable"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return tc.status, tc.body }, opHarnessOptions{})
			raw, _ := json.Marshal(vars)
			resp, err := h.runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: hot.Name, Variables: raw})
			if err != nil {
				t.Fatal(err)
			}
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
			line := opLineOf(t, h.logs.String(), directread.OperationReadLogMessage)
			want := map[string]any{"org_id": opOrgA, "operation": "hotspots"}
			for k, v := range tc.wantLog {
				want[k] = v
			}
			opCertify(t, line, want)
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

func TestParseUpstreamStatusVocabularyIsClosed(t *testing.T) {
	vocab := directread.UpstreamGraphQLCodeVocabulary()
	seen := map[directread.UpstreamGraphQLCode]bool{}
	for _, c := range vocab {
		seen[c] = true
	}
	for _, c := range []directread.UpstreamGraphQLCode{directread.GraphQLCodeValidationFailed, directread.GraphQLCodeParseFailed, directread.GraphQLCodeMCPRefused, directread.GraphQLCodeReadBudget, directread.GraphQLCodeOther} {
		if !seen[c] {
			t.Fatalf("%s missing from the vocabulary", c)
		}
	}
}
