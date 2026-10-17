package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/full-chaos/dev-health-go/llm"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A caller that sends a wait budget gets a stored degraded answer inside the
// budget even when the request timeout would let a stalled model call run far
// longer. Without the header the same call runs to the request timeout minus
// the reserve.
func TestAWaitBudgetEndsAStalledModelCallInsideTheBudgetAndServesADegradedAnswer(t *testing.T) {
	logs := &bytes.Buffer{}
	runtime, err := modelprovider.New(context.Background(), modelprovider.Config{
		Provider: modelprovider.DefaultProvider, BaseURL: stalledModelProvider(t), Model: modelprovider.DefaultModel,
		APIKey: llm.NewSecret("sk-configured"), Timeout: 2 * time.Minute, MaxAttempts: 1, MaxTransportRetries: 0, AllowInsecureBaseURL: true,
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := teamSynthesisApp(t, runtime, nil, teamExpander{targets: 3, complete: true, workItemsOnly: true}, logs)
	app.config.RequestTimeout = 60 * time.Second

	started := time.Now()
	response := postTeamQuestionWithHeader(t, app, token, contractsv1.ContextFabricWaitBudgetHeader, "10")
	elapsed := time.Since(started)

	if elapsed > 14*time.Second {
		t.Fatalf("answered after %s, want inside the 10 s wait budget; status=%d", elapsed, response.Code)
	}
	result := decodeTeamAnswer(t, response, logs)
	if result.Status != cf.InvestigationDegraded || result.ResultID == "" {
		t.Fatalf("status = %q result_id = %q, want a stored degraded answer", result.Status, result.ResultID)
	}
	entries := logLines(t, logs.String(), modelFailureWarn)
	if len(entries) != 1 || entries[0]["composition_reserve_fired"] != true {
		t.Fatalf("warn lines = %v, want one that says the composition reserve ended the call", entries)
	}
	budgetLines := logLines(t, logs.String(), "investigation wait budget")
	if len(budgetLines) != 1 || budgetLines[0]["narrowed"] != true || budgetLines[0]["budget_seconds"] != float64(10) {
		t.Fatalf("budget lines = %v, want one narrowed 10 s line", budgetLines)
	}
}

func TestRequestBudgetNarrowsOnlyTheInvestigationRouteAndOnlyBelowTheTimeout(t *testing.T) {
	app := &App{config: AppConfig{RequestTimeout: 150 * time.Second}, logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	cases := []struct {
		name   string
		method string
		path   string
		header string
		want   time.Duration
	}{
		{"narrowed", http.MethodPost, ContextFabricInvestigationsPath, "45", 45 * time.Second},
		{"at minimum", http.MethodPost, ContextFabricInvestigationsPath, "10", 10 * time.Second},
		{"below minimum ignored", http.MethodPost, ContextFabricInvestigationsPath, "9", 150 * time.Second},
		{"not a number ignored", http.MethodPost, ContextFabricInvestigationsPath, "45s", 150 * time.Second},
		{"negative ignored", http.MethodPost, ContextFabricInvestigationsPath, "-45", 150 * time.Second},
		{"overflowing value never wraps negative", http.MethodPost, ContextFabricInvestigationsPath, "9223372037", 150 * time.Second},
		{"equal to timeout", http.MethodPost, ContextFabricInvestigationsPath, "150", 150 * time.Second},
		{"above timeout never extends", http.MethodPost, ContextFabricInvestigationsPath, "600", 150 * time.Second},
		{"absent", http.MethodPost, ContextFabricInvestigationsPath, "", 150 * time.Second},
		{"another route", http.MethodPost, "/api/v1/context-packets", "45", 150 * time.Second},
		{"result read", http.MethodGet, ContextFabricInvestigationsPath + "/x", "45", 150 * time.Second},
		{"same path other method", http.MethodGet, ContextFabricInvestigationsPath, "45", 150 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "https://acr.example.test"+tc.path, strings.NewReader(""))
			if tc.header != "" {
				request.Header.Set(contractsv1.ContextFabricWaitBudgetHeader, tc.header)
			}
			if got := app.requestBudget(request); got != tc.want {
				t.Fatalf("budget = %s, want %s", got, tc.want)
			}
		})
	}
}

func postTeamQuestionWithHeader(t *testing.T, app *App, token, name, value string) *httptest.ResponseRecorder {
	t.Helper()
	request := surfaceRequest("request_team_question")
	request.Question = "How is the Platform team doing?"
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "https://acr.example.test"+ContextFabricInvestigationsPath, bytes.NewReader(payload))
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-ACR-Client-Version", "1.2.5")
	httpRequest.Header.Set(name, value)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httpRequest)
	return recorder
}
