package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4355 (response-bound follow-up, live pilot rev 18,
// req_9ea9eea3f36f7d37d85e5c5c6bd386cf): a project-status answer whose
// ClaimedFacts carry #308's (CHAOS-4363) Rows-shaped team_breakdown/
// risk_breakdown tables -- each capped at
// contractsv1.ContextFabricClaimedFactMaxRows (64) rows -- serialized far
// past the OLD production default ACR_MAX_OUTPUT_TOKENS (4000, i.e. a
// 16,000-byte token budget at the route's 4-bytes-per-token estimate)
// long before it ever approached ACR_MAX_SERIALIZED_BYTES (262,144). The
// synthesized ANSWER can be tiny (few drivers, zero findings) while the
// canonical result the POST route serializes whole still carries every
// ClaimedFact a driver closed to -- that was the defect: production sized
// MaxOutputTokens for a text-only answer, before CHAOS-4347/CHAOS-4363
// gave a claimed fact a renderable table.
//
// internal/config/config.go's defaultMaxOutputTokens is now
// defaultMaxSerializedBytes/4 (65536), so the Tokens estimate can never
// bind tighter than the Bytes budget it approximates. This file both
// proves the historical defect (the fixture below exceeds the OLD 4000
// ceiling by more than 4x) and proves the fix (the SAME fixture now
// succeeds against the current production defaults).

// productionShapedBreakdownRow mirrors one row of investment.go's
// readProjectInvestment team_breakdown table (10 fields: team_id,
// team_name, day, delivery_units, work_items_completed, prs_merged,
// churn_loc, cycle_p50_hours, investment_area, project_stream) -- the
// widest of the #308 rollup shapes (investment/workload/readiness/health
// carry 6-10 fields each; see internal/contextfabric/devhealthfacts).
func productionShapedBreakdownRow(i int) contractsv1.ContextFabricClaimedFactRow {
	teamID := "team_" + strconv.Itoa(i)
	teamName := "Platform Team " + strconv.Itoa(i)
	day := "2026-08-27"
	area := "area_" + strconv.Itoa(i%4)
	stream := "stream_" + strconv.Itoa(i%5)
	delivery := int64(120 + i)
	items := int64(45 + i)
	prs := int64(12 + i)
	churn := int64(3400 + i*7)
	cycle := 18.5 + float64(i)*0.1
	return contractsv1.ContextFabricClaimedFactRow{Fields: map[string]contractsv1.ContextFabricScalarValue{
		"team_id":              {String: &teamID},
		"team_name":            {String: &teamName},
		"day":                  {String: &day},
		"delivery_units":       {Integer: &delivery},
		"work_items_completed": {Integer: &items},
		"prs_merged":           {Integer: &prs},
		"churn_loc":            {Integer: &churn},
		"cycle_p50_hours":      {Number: &cycle},
		"investment_area":      {String: &area},
		"project_stream":       {String: &stream},
	}}
}

// rowsBearingClaimedFact builds one ClaimedFact with a full
// ContextFabricClaimedFactMaxRows-row (64) renderable table, the shape
// attachCanonicalRows (internal/contextfabric/model_runtime.go) produces
// once a driver closes to a #308 project rollup.
func rowsBearingClaimedFact(claimID string, kind contractsv1.ContextFabricFactKind, subject contractsv1.ContextFabricSubjectRef) contractsv1.ContextFabricClaimedFact {
	rows := make([]contractsv1.ContextFabricClaimedFactRow, contractsv1.ContextFabricClaimedFactMaxRows)
	for i := range rows {
		rows[i] = productionShapedBreakdownRow(i)
	}
	teamCount := int64(contractsv1.ContextFabricClaimedFactMaxRows)
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: claimID, Kind: kind, Subject: subject, Field: "team_count",
		Value: contractsv1.ContextFabricScalarValue{Integer: &teamCount},
		Rows:  rows,
	}
}

// Constants mirroring internal/config's shipped production values
// (internal/config/config.go), which are unexported there. Keep these in
// sync with that file: legacyMaxOutputTokens/legacyMaxSerializedBytes are
// what production served BEFORE this ticket (the values that made the
// live pilot 413); fixedMaxOutputTokens/fixedMaxSerializedBytes/
// productionMaxItems are what it serves now.
const (
	legacyMaxOutputTokens        = 4000
	fixedMaxOutputTokens         = 65536
	productionMaxSerializedBytes = 262144
	productionMaxItems           = 30
)

// newContextFabricTestAppWithProductionLimits is
// newContextFabricTestApp, except the app config and its limits.Manager
// resource budget are wired to the SAME production defaults
// internal/config.Config ships today (fixedMaxOutputTokens/
// productionMaxSerializedBytes/productionMaxItems) instead of the
// generous test-harness ceiling newContextFabricTestApp otherwise gets
// from NewApp's zero-value defaults. Route wiring is otherwise identical
// to newContextFabricTestAppWithResultsAndLogs.
func newContextFabricTestAppWithProductionLimits(t *testing.T, investigator contextfabric.Investigator) (*App, string) {
	t.Helper()
	app, token, _ := newContextFabricTestAppWithResultsAndLogs(t, investigator, nil)
	app.config.MaxItems = productionMaxItems
	app.config.MaxOutputTokens = fixedMaxOutputTokens
	app.config.MaxSerializedBytes = productionMaxSerializedBytes
	manager, err := limits.NewManager(limits.Options{
		Now: app.now, PerOrgConcurrency: 4,
		Policies: limits.PolicySet{
			Auth: limits.AuthPolicy{Window: 0, PerOrgLimit: 0, PerCredentialLimit: 0},
			Context: limits.ContextPolicy{
				Window: time.Minute, PerOrgLimit: 100, PerCredentialLimit: 100,
				Resources: limits.ResourceBudget{MaxItems: productionMaxItems, MaxTokens: fixedMaxOutputTokens, MaxBytes: productionMaxSerializedBytes},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.limits = manager
	return app, token
}

// threeRollupProjectStatusResult is the CHAOS-4355 live-pilot-shaped
// fixture: a project-status answer whose synthesized ANSWER is small (2
// drivers, 0 findings) but whose ClaimedFacts carry the #308 (CHAOS-4363)
// Rows-shaped team_breakdown tables -- 3 rollups (investment/workload/
// readiness), each the full ContextFabricClaimedFactMaxRows (64) rows.
func threeRollupProjectStatusResult() contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{
		rowsBearingClaimedFact("claim_investment_rollup", contractsv1.ContextFabricFactInvestment, project),
		rowsBearingClaimedFact("claim_workload_rollup", contractsv1.ContextFabricFactWorkload, project),
		rowsBearingClaimedFact("claim_readiness_rollup", contractsv1.ContextFabricFactReadiness, project),
	}
	result.Drivers = []contractsv1.ContextFabricDriverJudgment{
		{
			DriverID: "driver_investment", Standing: contractsv1.ContextFabricDriverPrincipal,
			Category:         "investment",
			Title:            "Investment is concentrated in one stream",
			Summary:          "Most delivery units land in one project stream this period.",
			AffectedSubjects: []contractsv1.ContextFabricSubjectRef{project}, EvidenceRefIDs: []string{"evidence_1"},
			ClaimedFactIDs: []string{"claim_investment_rollup"}, Derivation: contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus: contractsv1.ContextFabricEpistemicObserved, Confidence: 0.9, Current: true,
		},
		{
			DriverID: "driver_workload", Standing: contractsv1.ContextFabricDriverContributing,
			Category:         "workload",
			Title:            "Workload forecast is stable",
			Summary:          "Owning teams show a stable capacity forecast.",
			AffectedSubjects: []contractsv1.ContextFabricSubjectRef{project}, EvidenceRefIDs: []string{"evidence_2"},
			ClaimedFactIDs: []string{"claim_workload_rollup"}, Derivation: contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus: contractsv1.ContextFabricEpistemicObserved, Confidence: 0.85, Current: true,
		},
	}
	result.EvidenceRefIDs = []string{"evidence_1", "evidence_2"}
	return result
}

// TestContextFabricInvestigationRouteRowsBearingResultNowFitsProductionResponseBudget
// is the CHAOS-4355 response-bound test: red against the OLD production
// default (4000 tokens), green against the current one (65536).
//
// Before this ticket the route returned 413 for this exact fixture: the
// measured size (asserted below) exceeded ACR_MAX_OUTPUT_TOKENS (4000)
// by more than 4x while using only 27% of ACR_MAX_SERIALIZED_BYTES --
// the Tokens budget, sized for a text-only answer, was the sole and
// spurious blocker. See internal/config/config.go's defaultMaxOutputTokens
// doc comment for the fix and its justification.
func TestContextFabricInvestigationRouteRowsBearingResultNowFitsProductionResponseBudget(t *testing.T) {
	result := threeRollupProjectStatusResult()

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("fixture does not marshal: %v", err)
	}
	measuredBytes := int64(len(encoded))
	measuredTokens := (measuredBytes + 3) / 4
	t.Logf("measured encoded result: %d bytes (~%d estimated tokens); OLD ACR_MAX_OUTPUT_TOKENS=%d (~%d bytes), CURRENT ACR_MAX_OUTPUT_TOKENS=%d (~%d bytes), ACR_MAX_SERIALIZED_BYTES=%d",
		measuredBytes, measuredTokens, legacyMaxOutputTokens, legacyMaxOutputTokens*4, fixedMaxOutputTokens, fixedMaxOutputTokens*4, productionMaxSerializedBytes)

	if measuredTokens <= legacyMaxOutputTokens {
		t.Fatalf("fixture measured %d estimated tokens, want > the OLD production ACR_MAX_OUTPUT_TOKENS (%d) -- fixture does not reproduce the live defect", measuredTokens, legacyMaxOutputTokens)
	}
	if measuredBytes >= productionMaxSerializedBytes {
		t.Fatalf("fixture measured %d bytes, want < ACR_MAX_SERIALIZED_BYTES (%d) -- this must isolate the Tokens-budget bound, not the Bytes one", measuredBytes, productionMaxSerializedBytes)
	}
	if measuredTokens > fixedMaxOutputTokens {
		t.Fatalf("fixture measured %d estimated tokens, want <= the CURRENT production ACR_MAX_OUTPUT_TOKENS (%d) -- the fix does not cover this fixture", measuredTokens, fixedMaxOutputTokens)
	}

	app, token := newContextFabricTestAppWithProductionLimits(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a %d-byte/%d-token Rows-bearing result must fit the CURRENT production response budget; body=%s", response.Code, measuredBytes, measuredTokens, response.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.ClaimedFacts) != 3 {
		t.Fatalf("claimed facts = %d, want 3 -- none of the 3 rollups may be silently dropped", len(got.ClaimedFacts))
	}
	for _, claim := range got.ClaimedFacts {
		if len(claim.Rows) != contractsv1.ContextFabricClaimedFactMaxRows {
			t.Fatalf("claim %q rows = %d, want %d -- Rows must reach the caller whole, never silently trimmed", claim.ClaimID, len(claim.Rows), contractsv1.ContextFabricClaimedFactMaxRows)
		}
	}
}

// TestContextFabricInvestigationRouteStillOverBudgetDisclosesMeasurement
// is the CHAOS-4355 telemetry requirement: a response that genuinely
// cannot fit even the raised production budget must still fail as a
// disclosed, diagnosable 413 -- the measured bytes/tokens and the
// configured ceiling in the error response's details, never a bare
// "exceeded service limits" with no numbers (team-lead brief: "never a
// silent trim"). The fixture here (12 Rows-bearing rollups) is a
// deliberately adversarial size, not a realistic single-project answer --
// it exists to prove the bound still holds and still discloses, not to
// re-litigate the live pilot shape covered above.
func TestContextFabricInvestigationRouteStillOverBudgetDisclosesMeasurement(t *testing.T) {
	result := validContextFabricInvestigationResult()
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	kinds := []contractsv1.ContextFabricFactKind{
		contractsv1.ContextFabricFactInvestment, contractsv1.ContextFabricFactWorkload, contractsv1.ContextFabricFactReadiness,
	}
	for i := 0; i < 12; i++ {
		result.ClaimedFacts = append(result.ClaimedFacts, rowsBearingClaimedFact("claim_rollup_"+strconv.Itoa(i), kinds[i%len(kinds)], project))
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("fixture does not marshal: %v", err)
	}
	measuredBytes := int64(len(encoded))
	if measuredBytes <= productionMaxSerializedBytes {
		t.Fatalf("fixture measured %d bytes, want > ACR_MAX_SERIALIZED_BYTES (%d) -- fixture must still exceed the RAISED production bound", measuredBytes, productionMaxSerializedBytes)
	}

	app, token := newContextFabricTestAppWithProductionLimits(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 -- a %d-byte result must still trip ACR_MAX_SERIALIZED_BYTES (%d); body=%s", response.Code, measuredBytes, productionMaxSerializedBytes, response.Body.String())
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	measuredDetail, ok := envelope.Error.Details["measured_bytes"]
	if !ok {
		t.Fatalf("error details = %#v, want a measured_bytes field -- a size-exceeded 413 must disclose what it measured, never a bare message", envelope.Error.Details)
	}
	if measured, ok := measuredDetail.(float64); !ok || int64(measured) < productionMaxSerializedBytes {
		t.Fatalf("details.measured_bytes = %v, want a number >= %d", measuredDetail, productionMaxSerializedBytes)
	}
	if _, ok := envelope.Error.Details["max_serialized_bytes"]; !ok {
		t.Fatalf("error details = %#v, want a max_serialized_bytes field so the caller can see which ceiling it hit", envelope.Error.Details)
	}
}
