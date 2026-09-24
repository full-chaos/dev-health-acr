package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestCHAOS6558_InvestigateQuestionBudgetRefusalCarriesNarrowingAdvice
// replays the prod follow-up (raw/11-g1b.json args: question +
// parent_result_id + one winr_ receipt) against a hosted fixture that
// answers with the exact 413 budget-refusal envelope acr-api served for it
// (api req_745310015e271972029c854d74886738; values from that request's
// "context fabric plan narrowing" log line). Before CHAOS-6558 the tool
// text was `validation: acr api error: code=invalid_request status=413
// retryable=false message="the request was rejected as invalid"`: the
// caller could not tell an oversized answer from a malformed request, and
// the axis to narrow on was dropped.
func TestCHAOS6558_InvestigateQuestionBudgetRefusalCarriesNarrowingAdvice(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/context-fabric/investigations" {
			writeJSONFixture(t, w, http.StatusRequestEntityTooLarge, contractsv1.ErrorEnvelope{
				SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_745310015e271972029c854d74886738",
				Error: contractsv1.ErrorDetail{
					Code: "invalid_request", Message: "The Context Fabric answer did not fit the response budget",
					HTTPStatus: http.StatusRequestEntityTooLarge,
					Details: map[string]any{
						"overrun": "bytes", "measured_items": 17, "measured_bytes": 122402,
						"max_items": 30, "max_serialized_bytes": 65536,
						"question_family": "discovered_cohort_ranking", "retry_attempted": true,
						"narrower_continuation": map[string]any{"family": "discovered_cohort_ranking", "axis": "result_count"},
					},
				},
			})
			return
		}
		writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
	}))
	t.Cleanup(server.Close)
	cfg := fixtureConfig(t, server)
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
	if err != nil {
		t.Fatal(err)
	}
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolInvestigateQuestion)
	boot := &Bootstrap{Config: cfg, Client: client, Capabilities: caps}

	args, err := json.Marshal(contractsv1.MCPInvestigateQuestionRequest{
		Question:       "Which teams need attention over the last 30 days?",
		ParentResultID: "result_3823e3f3ccab42aa75c067dbf47ccd33",
		PriorWindowReceipts: []contractsv1.ContextFabricBoundSubjectReceipt{
			{ResultID: "result_3823e3f3ccab42aa75c067dbf47ccd33", ReceiptID: "winr_27db92e1ded3a326e44be5b3"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := invokeInvestigateQuestion(context.Background(), boot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: args}})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("IsError = false, want a tool error for the 413")
	}
	text := toolResultText(result)
	for _, fragment := range []string{
		"status=413", "did not fit the response budget", "overrun=bytes",
		"measured_bytes=122402", "max_serialized_bytes=65536", "retry_attempted=true",
		"narrower_continuation=result_count",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("tool text %q lacks %q", text, fragment)
		}
	}
	if strings.Contains(text, "rejected as invalid") {
		t.Errorf("tool text %q still calls an oversized answer an invalid request", text)
	}
}

// The sidecar's closed axis set must equal the engine's registry (minus
// "none", which the hosted API omits rather than sends). A new axis added
// to the registry without the sidecar would silently drop the advice.
func TestCHAOS6558_SidecarContinuationAxesMatchEngineRegistry(t *testing.T) {
	want := map[string]bool{}
	for _, axis := range contextfabric.NarrowingContinuationAxisVocabulary() {
		if axis != contextfabric.NarrowingContinuationNone {
			want[string(axis)] = true
		}
	}
	got := map[string]bool{}
	for _, axis := range sidecar.BudgetContinuationAxes() {
		got[axis] = true
	}
	if len(got) != len(want) {
		t.Fatalf("sidecar axes %v, engine axes %v", got, want)
	}
	for axis := range want {
		if !got[axis] {
			t.Fatalf("engine axis %q missing from the sidecar's closed set %v", axis, got)
		}
	}
}
