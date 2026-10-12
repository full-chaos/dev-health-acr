package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestStoredReadMeasuresTheBudgetAfterThePlanRequirementFill(t *testing.T) {
	stored := validContextFabricInvestigationResult()
	stored.ResultID = "result_byid_fillbudget01"
	stored.AnswerPlan = storedCountPlan()
	stored.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{}
	stored.Completeness.Outcomes = nil
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	app, token, _ := newCompletenessAuthorityTestApp(t, legacyResultStore{result: stored}, false)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() <= len(raw) {
		t.Fatalf("served %d bytes, stored %d: the read-time fill added nothing, so this test no longer exercises it", recorder.Body.Len(), len(raw))
	}

	app.config.MaxSerializedBytes = len(raw) + 1
	recorder = httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, stored.ResultID))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: the filled document exceeds a budget the stored one fits (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"budget_refusal"`) {
		t.Fatalf("413 body is not a budget_refusal: %s", recorder.Body.String())
	}
}
