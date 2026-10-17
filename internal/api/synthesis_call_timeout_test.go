package api

import (
	"bytes"
	"context"
	"github.com/full-chaos/dev-health-go/llm"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// stalledModelProvider is an OpenAI-compatible endpoint that holds every call
// open until the client gives up.
func stalledModelProvider(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	return server.URL
}

// The production model runtime, two attempts, each ending at the per-call
// timeout, after every fact was read. The answer is stored degraded.
func TestAModelCallThatTimesOutOnEveryAttemptIsServedAsADegradedAnswer(t *testing.T) {
	logs := &bytes.Buffer{}
	runtime, err := modelprovider.New(context.Background(), modelprovider.Config{
		Provider: modelprovider.DefaultProvider, BaseURL: stalledModelProvider(t), Model: modelprovider.DefaultModel,
		APIKey: llm.NewSecret("sk-configured"), Timeout: time.Second, MaxAttempts: 2, MaxTransportRetries: 0, AllowInsecureBaseURL: true,
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := teamSynthesisApp(t, runtime, nil, teamExpander{targets: 3, complete: true, workItemsOnly: true}, logs)

	response := postTeamQuestion(t, app, token)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a stored degraded answer; body=%s logs=%s", response.Code, response.Body.String(), logs.String())
	}
	result := decodeTeamAnswer(t, response, logs)
	var failure *cf.CoverageDetail
	for index := range result.Coverage.Details {
		if result.Coverage.Details[index].Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed {
			failure = &result.Coverage.Details[index]
		}
	}
	if result.Status != cf.InvestigationDegraded || failure == nil || failure.RetryHint != contractsv1.ContextFabricRetryMaySucceed {
		t.Fatalf("status = %q failure detail = %+v, want degraded with synthesis_model_failed and retry_may_succeed", result.Status, failure)
	}
}

// A call that would run to the end of the request is ended before it, so the
// degraded answer can still be composed and stored: the request does not end
// as a 504 with no answer, and the log says the reserve ended the call.
func TestAModelCallThatWouldOutlastTheRequestIsEndedInTimeToServeADegradedAnswer(t *testing.T) {
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
	app.config.RequestTimeout = 12 * time.Second

	response := postTeamQuestion(t, app, token)

	result := decodeTeamAnswer(t, response, logs)
	if result.Status != cf.InvestigationDegraded || !result.Coverage.Partial {
		t.Fatalf("status = %q partial = %v, want a degraded partial answer", result.Status, result.Coverage.Partial)
	}
	entries := logLines(t, logs.String(), modelFailureWarn)
	if len(entries) != 1 || entries[0]["composition_reserve_fired"] != true {
		t.Fatalf("warn lines = %v, want one that says the composition reserve ended the call", entries)
	}
}
