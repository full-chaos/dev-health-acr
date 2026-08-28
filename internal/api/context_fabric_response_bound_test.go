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
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4355 (response-bound follow-up, live pilot rev 18,
// req_9ea9eea3f36f7d37d85e5c5c6bd386cf): a project-status answer whose
// ClaimedFacts carry #308's (CHAOS-4363) Rows-shaped team_breakdown/
// risk_breakdown tables -- each capped at
// contractsv1.ContextFabricClaimedFactMaxRows (64) rows -- serialized far
// past ACR_MAX_OUTPUT_TOKENS's default (4000, i.e. a 16,000-byte token
// budget at the route's 4-bytes-per-token estimate) while using only 27%
// of ACR_MAX_SERIALIZED_BYTES (262,144). The synthesized ANSWER can be
// tiny (few drivers, zero findings) while the canonical result the route
// serializes whole still carries every ClaimedFact a driver closed to --
// that was the defect: production sized MaxOutputTokens for a text-only
// answer, before CHAOS-4347/CHAOS-4363 gave a claimed fact a renderable
// table, and the shared RequestClassContext Tokens budget the routes were
// charging it against was ALSO tripping first and alone.
//
// The fix (codex R1 corrected an initial "just raise the default"
// attempt, see internal/config/config.go's defaultMaxOutputTokens doc
// comment for why that default must stay 4000): both Context Fabric
// response routes (context_fabric_routes.go, context_fabric_result_routes.go)
// stop charging usage.Tokens against the shared budget entirely.
// ACR_MAX_SERIALIZED_BYTES -- already correctly sized, already
// configurable, unchanged by this ticket -- is the authoritative "does
// this fit the wire" gate; the Tokens estimate is still measured and
// disclosed for diagnostics, never enforced, on this path.

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

// Constants mirroring internal/config's shipped production defaults
// (internal/config/config.go), which are unexported there. This ticket
// does not change any of them -- the fix is entirely in route logic (see
// this file's package doc comment) -- so these stay equal to what
// production has always served.
const (
	productionMaxOutputTokens    = 4000
	productionMaxSerializedBytes = 262144
	productionMaxItems           = 30
)

// newContextFabricTestAppWithProductionLimits is
// newContextFabricTestAppWithResults, except the app config and its
// limits.Manager resource budget are wired to the SAME production
// defaults internal/config.Config ships today instead of the generous
// test-harness ceiling newContextFabricTestApp otherwise gets from
// NewApp's zero-value defaults (16_000/1<<20/50). Route wiring is
// otherwise identical to newContextFabricTestAppWithResultsAndLogs.
func newContextFabricTestAppWithProductionLimits(t *testing.T, investigator contextfabric.Investigator, results contextfabric.InvestigationResultStore) (*App, string) {
	t.Helper()
	app, token, _ := newContextFabricTestAppWithResultsAndLogs(t, investigator, results)
	app.config.MaxItems = productionMaxItems
	app.config.MaxOutputTokens = productionMaxOutputTokens
	app.config.MaxSerializedBytes = productionMaxSerializedBytes
	manager, err := limits.NewManager(limits.Options{
		Now: app.now, PerOrgConcurrency: 4,
		Policies: limits.PolicySet{
			Auth: limits.AuthPolicy{Window: 0, PerOrgLimit: 0, PerCredentialLimit: 0},
			Context: limits.ContextPolicy{
				Window: time.Minute, PerOrgLimit: 100, PerCredentialLimit: 100,
				Resources: limits.ResourceBudget{MaxItems: productionMaxItems, MaxTokens: productionMaxOutputTokens, MaxBytes: productionMaxSerializedBytes},
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
func threeRollupProjectStatusResult(resultID string) contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	result.ResultID = resultID
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

// twelveRollupResult is a deliberately adversarial fixture -- not a
// realistic single-project answer -- sized to still exceed
// ACR_MAX_SERIALIZED_BYTES (262144) even after this fix. It exists to
// prove the bound still holds and still discloses its measurement, not to
// re-litigate the live pilot shape above.
func twelveRollupResult(resultID string) contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	result.ResultID = resultID
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	kinds := []contractsv1.ContextFabricFactKind{
		contractsv1.ContextFabricFactInvestment, contractsv1.ContextFabricFactWorkload, contractsv1.ContextFabricFactReadiness,
	}
	for i := 0; i < 12; i++ {
		result.ClaimedFacts = append(result.ClaimedFacts, rowsBearingClaimedFact("claim_rollup_"+strconv.Itoa(i), kinds[i%len(kinds)], project))
	}
	return result
}

func marshaledSize(t *testing.T, v any) int64 {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("fixture does not marshal: %v", err)
	}
	return int64(len(encoded))
}

func assertErrorDetailsDiscloseMeasurement(t *testing.T, body []byte, wantMinBytes int64) {
	t.Helper()
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	measuredDetail, ok := envelope.Error.Details["measured_bytes"]
	if !ok {
		t.Fatalf("error details = %#v, want a measured_bytes field -- a size-exceeded 413 must disclose what it measured, never a bare message", envelope.Error.Details)
	}
	measured, ok := measuredDetail.(float64)
	if !ok || int64(measured) < wantMinBytes {
		t.Fatalf("details.measured_bytes = %v, want a number >= %d", measuredDetail, wantMinBytes)
	}
	maxDetail, ok := envelope.Error.Details["max_serialized_bytes"]
	if !ok {
		t.Fatalf("error details = %#v, want a max_serialized_bytes field so the caller can see which ceiling it hit", envelope.Error.Details)
	}
	if max, ok := maxDetail.(float64); !ok || int64(max) != productionMaxSerializedBytes {
		t.Fatalf("details.max_serialized_bytes = %v, want %d (the configured ceiling, not a hardcoded or stale value)", maxDetail, productionMaxSerializedBytes)
	}
}

// TestContextFabricInvestigationRouteRowsBearingResultFitsProductionResponseBudget
// is the CHAOS-4355 response-bound test for the POST /investigations
// route: the live-pilot-shaped fixture exceeds the production Tokens
// estimate by more than 4x (documenting why a Tokens-gated design fails
// it) yet returns 200 today because these routes no longer charge
// usage.Tokens against the shared budget -- ACR_MAX_SERIALIZED_BYTES,
// comfortably clear here, is what actually governs.
func TestContextFabricInvestigationRouteRowsBearingResultFitsProductionResponseBudget(t *testing.T) {
	result := threeRollupProjectStatusResult("result_4355_post")
	measuredBytes := marshaledSize(t, result)
	estimatedTokens := (measuredBytes + 3) / 4
	t.Logf("measured encoded result: %d bytes (~%d estimated tokens); ACR_MAX_OUTPUT_TOKENS=%d (~%d bytes, NOT enforced on this path), ACR_MAX_SERIALIZED_BYTES=%d",
		measuredBytes, estimatedTokens, productionMaxOutputTokens, productionMaxOutputTokens*4, productionMaxSerializedBytes)

	if estimatedTokens <= productionMaxOutputTokens {
		t.Fatalf("fixture measured %d estimated tokens, want > ACR_MAX_OUTPUT_TOKENS (%d) -- fixture does not reproduce the live defect's shape", estimatedTokens, productionMaxOutputTokens)
	}
	if measuredBytes >= productionMaxSerializedBytes {
		t.Fatalf("fixture measured %d bytes, want < ACR_MAX_SERIALIZED_BYTES (%d) -- this must isolate the (no longer enforced) Tokens bound from the Bytes one", measuredBytes, productionMaxSerializedBytes)
	}

	app, token := newContextFabricTestAppWithProductionLimits(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}), nil)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a %d-byte/%d-token Rows-bearing result must fit the production response budget; body=%s", response.Code, measuredBytes, estimatedTokens, response.Body.String())
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

// TestContextFabricInvestigationResultRouteRowsBearingResultFitsProductionResponseBudget
// is the GET-by-id sibling: a result that returns once from POST must also
// be re-readable by GET /investigations/{result_id} under the SAME bound
// (both routes read a.config.MaxSerializedBytes/MaxItems and neither
// charges Tokens) -- see context_fabric_result_routes.go's doc comment on
// ContextFabricInvestigationResultPath.
func TestContextFabricInvestigationResultRouteRowsBearingResultFitsProductionResponseBudget(t *testing.T) {
	result := threeRollupProjectStatusResult("result_4355_get")
	store := memoryinvestigation.NewStore()
	seeded := seedResult3355(t, store, callerOrgID, result)

	app, token := newContextFabricTestAppWithProductionLimits(t, nil, store)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationResultRequest(t, token, seeded.ResultID))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a stored Rows-bearing result must be re-readable under the same production budget the write path uses; body=%s", response.Code, response.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.ClaimedFacts) != 3 {
		t.Fatalf("claimed facts = %d, want 3", len(got.ClaimedFacts))
	}
	for _, claim := range got.ClaimedFacts {
		if len(claim.Rows) != contractsv1.ContextFabricClaimedFactMaxRows {
			t.Fatalf("claim %q rows = %d, want %d", claim.ClaimID, len(claim.Rows), contractsv1.ContextFabricClaimedFactMaxRows)
		}
	}
}

// seedResult3355 is seedResult (context_fabric_result_routes_test.go) but
// takes an already-built result rather than the shared
// validContextFabricInvestigationResult() fixture, so this file's
// Rows-bearing fixtures can be stored directly.
func seedResult3355(t *testing.T, store *memoryinvestigation.Store, orgID string, result contractsv1.ContextFabricInvestigationResult) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	if err := store.Save(context.Background(), storage.Principal{OrgID: orgID}, result, contextfabric.SourceWatermarkSnapshot{}, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	return result
}

// TestContextFabricInvestigationRouteStillOverBudgetDisclosesMeasurement
// is the CHAOS-4355 telemetry requirement: a response that genuinely
// cannot fit even ACR_MAX_SERIALIZED_BYTES must still fail as a
// disclosed, diagnosable 413 -- the measured bytes and the configured
// ceiling in the error response's details, never a bare "exceeded service
// limits" with no numbers (team-lead brief: "never a silent trim").
func TestContextFabricInvestigationRouteStillOverBudgetDisclosesMeasurement(t *testing.T) {
	result := twelveRollupResult("result_4355_post_over")
	measuredBytes := marshaledSize(t, result)
	if measuredBytes <= productionMaxSerializedBytes {
		t.Fatalf("fixture measured %d bytes, want > ACR_MAX_SERIALIZED_BYTES (%d)", measuredBytes, productionMaxSerializedBytes)
	}

	app, token := newContextFabricTestAppWithProductionLimits(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}), nil)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 -- a %d-byte result must still trip ACR_MAX_SERIALIZED_BYTES (%d); body=%s", response.Code, measuredBytes, productionMaxSerializedBytes, response.Body.String())
	}
	assertErrorDetailsDiscloseMeasurement(t, response.Body.Bytes(), productionMaxSerializedBytes)
}

// TestContextFabricInvestigationResultRouteStillOverBudgetDisclosesMeasurement
// is the GET-by-id sibling of the test above.
func TestContextFabricInvestigationResultRouteStillOverBudgetDisclosesMeasurement(t *testing.T) {
	result := twelveRollupResult("result_4355_get_over")
	measuredBytes := marshaledSize(t, result)
	if measuredBytes <= productionMaxSerializedBytes {
		t.Fatalf("fixture measured %d bytes, want > ACR_MAX_SERIALIZED_BYTES (%d)", measuredBytes, productionMaxSerializedBytes)
	}
	store := memoryinvestigation.NewStore()
	seeded := seedResult3355(t, store, callerOrgID, result)

	app, token := newContextFabricTestAppWithProductionLimits(t, nil, store)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationResultRequest(t, token, seeded.ResultID))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 -- a %d-byte stored result must still trip ACR_MAX_SERIALIZED_BYTES (%d) on re-read; body=%s", response.Code, measuredBytes, productionMaxSerializedBytes, response.Body.String())
	}
	assertErrorDetailsDiscloseMeasurement(t, response.Body.Bytes(), productionMaxSerializedBytes)
}
