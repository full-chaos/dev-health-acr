package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// waitBudgetBootstrap serves an investigation endpoint that stands in for the
// hosted contract: with the wait budget header it answers at once (a stored
// answer with a result_id); without it, it holds the call for hostedWork.
// seenBudget records the header the hosted side received.
func waitBudgetBootstrap(t *testing.T, callTimeout, wait, hostedWork time.Duration, seenBudget *atomic.Value) *Bootstrap {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/context-fabric/investigations" {
			budget := r.Header.Get(contractsv1.ContextFabricWaitBudgetHeader)
			seenBudget.Store(budget)
			if budget == "" {
				select {
				case <-time.After(hostedWork):
				case <-r.Context().Done():
				}
				return
			}
			writeJSONFixture(t, w, http.StatusOK, parityResult())
			return
		}
		writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
	}))
	t.Cleanup(server.Close)
	cfg := fixtureConfig(t, server)
	cfg.Timeout = callTimeout
	cfg.InvestigationWait = wait
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
	if err != nil {
		t.Fatal(err)
	}
	caps := validCapabilitiesFixture()
	caps.EnabledTools = append(caps.EnabledTools, toolInvestigateQuestion, toolInvestigationResult)
	return &Bootstrap{Config: cfg, Client: client, Capabilities: caps}
}

func callInvestigationWithDefaultQuestion(t *testing.T, boot *Bootstrap) *mcpsdk.CallToolResult {
	t.Helper()
	args, err := json.Marshal(contractsv1.MCPInvestigateQuestionRequest{Question: "Why is the payments project slipping over the last 90 days?"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := invokeInvestigateQuestion(context.Background(), boot, &mcpsdk.CallToolRequest{
		Params: &mcpsdk.CallToolParamsRaw{Arguments: args},
	})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	return result
}

// Past the per-call deadline with no wait budget the caller receives only a
// bare timeout: no result_id, nothing to read later.
func TestInvestigateQuestionWithoutAWaitBudgetPastTheCallDeadlineReturnsOnlyATimeout(t *testing.T) {
	var seen atomic.Value
	boot := waitBudgetBootstrap(t, time.Second, 0, 4*time.Second, &seen)
	result := callInvestigationWithDefaultQuestion(t, boot)
	text := toolResultText(result)
	if !result.IsError || !strings.Contains(text, "timeout") || strings.Contains(text, "result_") {
		t.Fatalf("is_error=%v text=%q, want a bare timeout error", result.IsError, text)
	}
	if got, _ := seen.Load().(string); got != "" {
		t.Fatalf("wait budget header sent as %q with the budget off", got)
	}
}

// With a wait budget configured the sidecar sends it, in whole seconds, and
// the answer it receives back reaches the caller with its result_id.
func TestInvestigateQuestionSendsTheConfiguredWaitBudgetAndServesTheAnswer(t *testing.T) {
	var seen atomic.Value
	boot := waitBudgetBootstrap(t, 20*time.Second, 15*time.Second, 4*time.Second, &seen)
	result := callInvestigationWithDefaultQuestion(t, boot)
	if result.IsError {
		t.Fatalf("tool error: %s", toolResultText(result))
	}
	if got, _ := seen.Load().(string); got != "15" {
		t.Fatalf("wait budget header = %q, want 15", got)
	}
	var response contractsv1.MCPInvestigateQuestionResponse
	if err := json.Unmarshal(result.StructuredContent.(json.RawMessage), &response); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	if response.Structured.ResultID != parityResult().ResultID {
		t.Fatalf("answer result_id = %q, want %q", response.Structured.ResultID, parityResult().ResultID)
	}
}
