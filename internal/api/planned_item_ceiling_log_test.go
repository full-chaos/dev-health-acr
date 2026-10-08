package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The measured line states the ceiling the response was gated against: a
// listing planned room for more items than the configured ceiling reports the
// planned one.
func TestMeasuredLineReportsThePlannedItemCeiling(t *testing.T) {
	result := threeRollupProjectStatusResult("result_planned_ceiling")
	planned := productionMaxItems + 20
	result.AnswerPlan = validAnswerPlanForCeiling(planned)

	app, token, logs := newContextFabricTestAppWithProductionLimitsAndLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	entry := decodeLogLine(t, logs.String(), "context fabric response measured")
	if got, want := entry["max_items"], float64(planned); got != want {
		t.Fatalf("max_items = %#v, want the planned %v (status=%d body=%s)", got, want, response.Code, response.Body.String())
	}
}

func validAnswerPlanForCeiling(items int) *contractsv1.ContextFabricAnswerPlan {
	return &contractsv1.ContextFabricAnswerPlan{
		Family:        contractsv1.ContextFabricQuestionFamilyScopedCohortStatus,
		FamilySource:  contractsv1.ContextFabricQuestionFamilySourceModel,
		FamilyVersion: contextfabric.QuestionFamilyTableVersion,
		Budget:        contractsv1.ContextFabricAnswerPlanBudget{MaxItems: items},
	}
}
