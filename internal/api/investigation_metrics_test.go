package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func runInvestigationWithMetrics(t *testing.T, result contextfabric.InvestigationResult, failure error) map[string]int64 {
	t.Helper()
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	app, token, _ := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, failure
	}))
	app.metrics = instruments
	app.Handler().ServeHTTP(httptest.NewRecorder(), investigationRequest(t, token))
	return read()
}

func TestInvestigationRouteCountsFailuresAtTheCertifiedLine(t *testing.T) {
	abort := func(cause error) error {
		return &contextfabric.StageError{Stage: contextfabric.StageFactRead, Err: fmt.Errorf("%w: read canonical facts: %w", contextfabric.ErrFactReadAborted, &contextfabric.FactReadAbortDetail{
			RequirementCount: 2, SubjectKinds: []string{"team"}, MemberKind: "project", Err: cause,
		})}
	}
	cells := []struct {
		name    string
		failure error
		want    map[string]int64
	}{
		{"no_fact_requirements", abort(contextfabric.ErrNoFactRequirements), map[string]int64{
			"acr_fact_read_aborts_total{cause=no_fact_requirements}": 1,
			"acr_investigation_latency_seconds{status=error}":        1,
		}},
		{"other_cause", abort(errors.New("fact query subjects must be unique: team:X")), map[string]int64{
			"acr_fact_read_aborts_total{cause=other}":         1,
			"acr_investigation_latency_seconds{status=error}": 1,
		}},
		{"budget_refusal", contextfabric.AnswerBudgetRefusal{
			Overrun: contractsv1.ContextFabricBudgetOverrunItems, MeasuredItems: 41, MaxItems: 30, MaxSerializedBytes: 1 << 20,
		}, map[string]int64{
			"acr_budget_refusals_total{}":                     1,
			"acr_investigation_latency_seconds{status=error}": 1,
		}},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			got := runInvestigationWithMetrics(t, contextfabric.InvestigationResult{}, cell.failure)
			if len(got) != len(cell.want) {
				t.Fatalf("cells %v, want %v", got, cell.want)
			}
			for key, value := range cell.want {
				if got[key] != value {
					t.Errorf("%s = %d, want %d (all %v)", key, got[key], value, got)
				}
			}
		})
	}
}

func TestInvestigationRouteRecordsLatencyByTerminalStatus(t *testing.T) {
	got := runInvestigationWithMetrics(t, contextfabric.InvestigationResult{Status: contractsv1.ContextFabricInvestigationPartial}, nil)
	if got["acr_investigation_latency_seconds{status=partial}"] != 1 {
		t.Fatalf("cells %v, want one partial latency sample", got)
	}
}

// A 413 from a response-size gate refuses the answer: it is a budget refusal,
// and its latency is "error", not the status of the answer it did not deliver.
func TestInvestigationRouteResponseSizeRefusalIsCountedAsRefusalAndError(t *testing.T) {
	result := twelveRollupResult("result_metrics_over")
	result.Status = contractsv1.ContextFabricInvestigationComplete
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	app, token, _ := newContextFabricTestAppWithProductionLimitsAndLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	app.metrics = instruments
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationRequest(t, token))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", response.Code, response.Body.String())
	}
	want := map[string]int64{
		"acr_budget_refusals_total{}":                     1,
		"acr_investigation_latency_seconds{status=error}": 1,
	}
	got := read()
	if len(got) != len(want) {
		t.Fatalf("cells %v, want %v", got, want)
	}
	for cell, value := range want {
		if got[cell] != value {
			t.Errorf("%s = %d, want %d (all %v)", cell, got[cell], value, got)
		}
	}
}
