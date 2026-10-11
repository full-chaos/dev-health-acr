package mcp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

// A 429 that acr-api answers from its per-organization request budget is not
// an authentication attempt limit: the caller's credential was never judged.
func TestHTTPRequestBudgetRefusalIsNotNamedAnAuthenticationDenial(t *testing.T) {
	hosted := newHostedAPI(t)
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	e := newEndpoint(t, hosted)
	hosted.requestBudget.Store(true)
	resp := postMCP(t, e, http.MethodPost, rawToolsList(), withRequestID(bearerHeader(valid.token), "req-budget"))
	hosted.requestBudget.Store(false)
	var body struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "9" {
		t.Fatalf("status %d Retry-After %q, want 429 and 9", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if strings.Contains(strings.ToLower(body.ErrorDescription), "authentication") {
		t.Fatalf("budget refusal described as %q (error %q)", body.ErrorDescription, body.Error)
	}
	res, err := certify.Certify(parseLog(t, e), certify.Assertion{Event: eventspec.MCPHTTPRequest, Want: map[string]any{"request_id": "req-budget"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Line["auth_outcome"] != "request_budget_exceeded" {
		t.Fatalf("auth_outcome %v", res.Line["auth_outcome"])
	}
	if got := res.Line["result_class"]; got == "auth_denied" {
		t.Fatalf("budget refusal logged as result_class %v, auth_outcome %v", got, res.Line["auth_outcome"])
	}
}
