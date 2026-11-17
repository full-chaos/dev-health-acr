package directread_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

var operationListenerRefusedLogMsg = directread.OperationListenerRefusedLog

func mcpRefusedBody(reason string) string {
	return `{"errors":[{"message":"SECRET-UPSTREAM","extensions":{"code":"MCP_REFUSED","reason":"` + reason + `"}}]}`
}

// Every MCP carrier refusal reason maps on run_operation as on graphql_query:
// class carrier_refused, call upstream_error, no data, no retry, the reason
// only in the ERROR log.
func TestRunOperationEveryCarrierRefusalReasonIsMapped(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op := cat.Operations(directread.CallerUnrestricted)[0]
	rows := []struct {
		status   int
		reason   string
		precheck bool
	}{
		{401, "authorization_header", false},
		{401, "no_carrier", false},
		{401, "missing_header", false},
		{401, "duplicate_header", false},
		{401, "not_a_boolean", false},
		{401, "invalid_org", false},
		{401, "not_a_principal", true},
		{403, "elevated_claim", false},
		{403, "invalid_org_argument", true},
		{403, "org_mismatch", true},
	}
	for _, tc := range rows {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.reason), func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return tc.status, mcpRefusedBody(tc.reason) }, opHarnessOptions{})
			resp := h.run(t, opUnrestricted(opOrgA), op.Name, opMinimalVariables(t, op))
			if resp.Call != directread.CallUpstreamError || len(resp.Errors) != 1 || resp.Errors[0] != (directread.OperationError{Class: directread.UpstreamCarrierRefused}) || resp.Data != nil || resp.Refusal != nil {
				t.Fatalf("want upstream_error/carrier_refused only, got %+v", resp)
			}
			if n := len(h.upstream.requests()); n != 1 {
				t.Fatalf("retry: %d upstream requests", n)
			}
			raw, _ := json.Marshal(resp)
			if strings.Contains(string(raw), tc.reason) || strings.Contains(string(raw), "SECRET-UPSTREAM") {
				t.Fatalf("upstream text passed through: %s", raw)
			}
			lines := opLogLines(t, h, operationListenerRefusedLogMsg)
			if len(lines) != 1 {
				t.Fatalf("want one refusal line, got %d in %s", len(lines), h.logs.String())
			}
			l := lines[0]
			if l["level"] != "ERROR" || l["listener_reason"] != tc.reason || l["acr_precheck_gap"] != tc.precheck || l["error_class"] != "carrier_refused" || l["operation"] != op.Name || l["status"] != float64(tc.status) {
				t.Fatalf("log line %v", l)
			}
			if strings.Contains(h.logs.String(), "SECRET-UPSTREAM") {
				t.Fatalf("body echoed to the log: %s", h.logs.String())
			}
		})
	}
}

// Parity: anything that is not a carrier reason keeps today's http_status
// answer with the status and no refusal line.
func TestRunOperationNonCarrierRefusalKeepsHTTPStatus(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op := cat.Operations(directread.CallerUnrestricted)[0]
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"no_body", 401, ``},
		{"plain_text", 401, `SECRET-UPSTREAM unauthorized`},
		{"no_reason", 401, `{"errors":[{"message":"m","extensions":{"code":"MCP_REFUSED"}}]}`},
		{"unknown_reason", 401, mcpRefusedBody("a_future_reason")},
		{"carrier_reason_other_code", 401, `{"errors":[{"message":"m","extensions":{"code":"OTHER","reason":"missing_header"}}]}`},
		{"listener_class_reason", 403, mcpRefusedBody("root_field_not_allowed")},
		{"listener_class_reason_400", 400, mcpRefusedBody("depth_limit")},
		{"server_error", 502, `SECRET-UPSTREAM stack trace`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return tc.status, tc.body }, opHarnessOptions{})
			resp := h.run(t, opUnrestricted(opOrgA), op.Name, opMinimalVariables(t, op))
			if resp.Call != directread.CallUpstreamError || len(resp.Errors) != 1 || resp.Errors[0].Class != directread.UpstreamHTTPStatus || resp.Errors[0].Status != tc.status || resp.Data != nil || resp.Refusal != nil {
				t.Fatalf("want upstream_error/http_status %d, got %+v", tc.status, resp)
			}
			if n := len(opLogLines(t, h, operationListenerRefusedLogMsg)); n != 0 {
				t.Fatalf("%d refusal lines for a non-carrier answer", n)
			}
		})
	}
}

// The census: every known listener reason is in exactly one of the two
// tables, so a reason added to the shared map must be classified here.
func TestRunOperationCarrierTableCoversTheSharedMap(t *testing.T) {
	carrier := map[string]bool{"authorization_header": true, "no_carrier": true, "missing_header": true, "duplicate_header": true, "not_a_boolean": true, "invalid_org": true, "not_a_principal": true, "elevated_claim": true, "invalid_org_argument": true, "org_mismatch": true}
	cat, _ := directread.DefaultCatalogue()
	op := cat.Operations(directread.CallerUnrestricted)[0]
	for _, reason := range directread.MCPListenerRefusalReasons() {
		h := newOpHarness(t, func(opRecorded) (int, string) { return 401, mcpRefusedBody(reason) }, opHarnessOptions{})
		resp := h.run(t, opUnrestricted(opOrgA), op.Name, opMinimalVariables(t, op))
		got := resp.Errors[0].Class
		want := directread.UpstreamHTTPStatus
		if carrier[reason] {
			want = directread.UpstreamCarrierRefused
		}
		if got != want {
			t.Errorf("reason %s: class %s, want %s", reason, got, want)
		}
	}
}
