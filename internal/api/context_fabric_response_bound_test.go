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
// The fix went through two rounds. R1 raised defaultMaxOutputTokens
// (reverted -- see internal/config/config.go's defaultMaxOutputTokens doc
// comment for why that default must stay 4000: it is also the
// capabilities-advertised ceiling and the Context Packet/MCP wire
// validators' own ceiling). R2 tried Tokens: 0 (charging nothing);
// team-lead's ruling rejected that as a false accounting record -- a 70KB
// response costs real tokens whether or not a ceiling rejects it, and
// Manager.Usage()'s org/credential window totals must stay truthful. The
// shipped fix: both Context Fabric response routes
// (context_fabric_routes.go, context_fabric_result_routes.go) charge the
// REAL measured Tokens estimate via CompleteUsageWithBudget, evaluated
// against an override budget with Tokens UNLIMITED (MaxTokens: 0, see
// limits.Claim.CompleteWithBudget) instead of RequestClassContext's
// shared one -- so accounting stays accurate while the ceiling that was
// never sized for Rows tables stops rejecting a response that fits
// comfortably in bytes. ACR_MAX_SERIALIZED_BYTES -- already correctly
// sized, already configurable, unchanged by this ticket -- is the
// authoritative "does this fit the wire" gate on this path.

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

// runJRepositoryStatusShapedResult reproduces the exact answer shape Run J
// measured live against a plain "what is the status of this repository"
// question (CHAOS-4450 handoff-2026-08-29.md §2 Wall C,
// req_751c7014cab1c9d804277c469ed460fc / req_f84f6773...): 27
// ContextFabricRelationshipPath entries (graph-evidence provenance for the
// one driver), 10 SubjectResolution.Candidates, 1 Driver, and 3
// Rows-bearing ClaimedFacts -- 41 items by the pre-CHAOS-4523 count (Run J
// measured 42-43; the +1/+2 is answer-shape noise this fixture does not
// need to reproduce exactly). CHAOS-4418 (#324) gave repository facts real
// Rows tables; #324 did not grow Paths or Candidates -- those pre-date it
// -- but it was the first question shape to combine Rows-bearing claims
// with a graph-dense subject, which is what first pushed the pre-existing
// Paths/Candidates total over ACR_MAX_ITEMS=30.
func runJRepositoryStatusShapedResult(resultID string) contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	result.ResultID = resultID
	repo := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repo_dev_health_ops", Label: "full-chaos/dev-health-ops"}
	result.SubjectResolution.Committed = []contractsv1.ContextFabricSubjectRef{repo}
	for i := 0; i < 10; i++ {
		result.SubjectResolution.Candidates = append(result.SubjectResolution.Candidates, contractsv1.ContextFabricSubjectCandidate{
			ReceiptID: "receipt_" + strconv.Itoa(i), Subject: repo, State: contractsv1.ContextFabricResolutionProposed,
			MatchReasons: []string{"name_match"}, Confidence: 0.6,
		})
	}
	result.Drivers = []contractsv1.ContextFabricDriverJudgment{{
		DriverID: "driver_repo_status", Standing: contractsv1.ContextFabricDriverPrincipal,
		Category:         "status",
		Title:            "Repository activity is nominal",
		Summary:          "Recent activity matches the observed baseline.",
		AffectedSubjects: []contractsv1.ContextFabricSubjectRef{repo}, EvidenceRefIDs: []string{"evidence_repo_1"},
		ClaimedFactIDs: []string{"claim_repo_status_0"}, Derivation: contractsv1.ContextFabricDerivationCanonicalStructured,
		EpistemicStatus: contractsv1.ContextFabricEpistemicObserved, Confidence: 0.8, Current: true,
	}}
	for i := 0; i < 3; i++ {
		result.ClaimedFacts = append(result.ClaimedFacts, rowsBearingClaimedFact("claim_repo_status_"+strconv.Itoa(i), contractsv1.ContextFabricFactStatus, repo))
	}
	// CHAOS-4523 codex P2 finding: ContextFabricRelationshipPath.Validate
	// requires an 8-256 char PathID, at least 2 UNIQUE nodes, and exactly
	// len(nodes)-1 continuous, individually-valid edges -- a single-node,
	// short-ID path (the pre-fix fixture) can never be a production
	// InvestigationResult, so a regression test built on one proves
	// nothing about what production can actually emit. Each path here
	// pairs the repository with a distinct evidence subject and one
	// RELATED_TO edge between them.
	for i := 0; i < 27; i++ {
		evidenceSubject := contractsv1.ContextFabricSubjectRef{
			Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pr_repo_status_" + strconv.Itoa(i), Label: "pull request " + strconv.Itoa(i),
		}
		result.Paths = append(result.Paths, contractsv1.ContextFabricRelationshipPath{
			PathID: "path_repo_status_" + strconv.Itoa(i), Nodes: []contractsv1.ContextFabricSubjectRef{repo, evidenceSubject},
			Edges: []contractsv1.ContextFabricRelationshipEdge{{
				Type: contractsv1.ContextFabricRelationshipRelatedTo, From: repo, To: evidenceSubject,
				Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
				EvidenceRefIDs: []string{"evidence_repo_1"},
			}},
			WhyRelevant: "supports driver_repo_status", EvidenceRefIDs: []string{"evidence_repo_1"},
		})
	}
	result.EvidenceRefIDs = []string{"evidence_repo_1"}
	if err := result.Validate(); err != nil {
		panic("runJRepositoryStatusShapedResult built an invalid fixture: " + err.Error())
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

// usageProbeSubject looks up per-org usage totals after a request. Only
// OrgID needs to match the caller's (callerOrgID, "org_1" -- see
// issueScopedCredential); Manager.Usage's per-org window is keyed by
// OrgID alone (internal/limits/manager.go's Usage), so any
// syntactically-valid CredentialID is enough to satisfy Subject.validate
// -- this probe never needs the real issued credential's ID.
var usageProbeSubject = limits.Subject{OrgID: callerOrgID, CredentialID: "usage_probe"}

// TestContextFabricInvestigationRouteRowsBearingResultFitsProductionResponseBudget
// is the CHAOS-4355 response-bound test for the POST /investigations
// route: the live-pilot-shaped fixture exceeds ACR_MAX_OUTPUT_TOKENS by
// more than 4x (documenting why a Tokens-gated design rejects it) yet
// returns 200, because this route evaluates the response against an
// override budget with Tokens unlimited (see
// limits.Claim.CompleteWithBudget) instead of RequestClassContext's
// shared one -- ACR_MAX_SERIALIZED_BYTES, comfortably clear here, is what
// actually governs. The REAL measured token estimate is still charged:
// the org usage window's Tokens total must grow by exactly that amount,
// proving accounting stays truthful even though the ceiling doesn't bind
// (team-lead ruling, CHAOS-4355 codex R2).
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
	before, err := app.limits.Usage(usageProbeSubject, limits.RequestClassContext)
	if err != nil {
		t.Fatal(err)
	}
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
	after, err := app.limits.Usage(usageProbeSubject, limits.RequestClassContext)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Org.Tokens - before.Org.Tokens; got != estimatedTokens {
		t.Fatalf("org window Tokens grew by %d, want exactly %d (the real measured estimate) -- a 200 response must not under-report its own token cost", got, estimatedTokens)
	}
}

// TestContextFabricInvestigationResultRouteRowsBearingResultFitsProductionResponseBudget
// is the GET-by-id sibling: a result that returns once from POST must also
// be re-readable by GET /investigations/{result_id} under the SAME bound
// (both routes read a.config.MaxSerializedBytes/MaxItems, and both charge
// the real Tokens estimate against an unlimited-Tokens override rather
// than RequestClassContext's shared budget) -- see
// context_fabric_result_routes.go's doc comment on
// ContextFabricInvestigationResultPath and the matching test above for
// why the org usage window must still grow.
func TestContextFabricInvestigationResultRouteRowsBearingResultFitsProductionResponseBudget(t *testing.T) {
	result := threeRollupProjectStatusResult("result_4355_get")
	measuredBytes := marshaledSize(t, result)
	estimatedTokens := (measuredBytes + 3) / 4
	store := memoryinvestigation.NewStore()
	seeded := seedResult3355(t, store, callerOrgID, result)

	app, token := newContextFabricTestAppWithProductionLimits(t, nil, store)
	before, err := app.limits.Usage(usageProbeSubject, limits.RequestClassContext)
	if err != nil {
		t.Fatal(err)
	}
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
	after, err := app.limits.Usage(usageProbeSubject, limits.RequestClassContext)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Org.Tokens - before.Org.Tokens; got != estimatedTokens {
		t.Fatalf("org window Tokens grew by %d, want exactly %d (the real measured estimate)", got, estimatedTokens)
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

// TestContextFabricInvestigationRouteRunJRepositoryStatusShapeFitsDefaultItemBudget
// is the CHAOS-4523 pinning test for CHAOS-4450 Run J Wall C: at the
// shipped ACR_MAX_ITEMS=30 default, a plain repository-status answer 413'd
// with "reason=items measured_items=42 max_items=30" even though its
// bytes (41,662) and estimated tokens (10,416) were nowhere near their own
// ceilings -- the fixture below reproduces that shape (41+ items by the
// pre-fix, all-inclusive count) and FAILS this way before CHAOS-4523 (the
// route charged contextFabricResultItems' full total, Paths included,
// against ACR_MAX_ITEMS) and PASSES after it (contextFabricItemCounts.
// budgeted excludes Paths -- see that method's doc comment for why: a
// RelationshipPath is graph-evidence provenance nothing in the web client
// renders today, not answer content the way a claimed fact or an offered
// candidate is).
func TestContextFabricInvestigationRouteRunJRepositoryStatusShapeFitsDefaultItemBudget(t *testing.T) {
	result := runJRepositoryStatusShapedResult("result_4523_repo_status")
	totalItems := contextFabricResultItems(result)
	if totalItems <= productionMaxItems {
		t.Fatalf("fixture measured %d total items, want > ACR_MAX_ITEMS (%d) -- fixture does not reproduce Run J's Wall C shape (42-43 measured_items)", totalItems, productionMaxItems)
	}
	measuredBytes := marshaledSize(t, result)
	if measuredBytes >= productionMaxSerializedBytes {
		t.Fatalf("fixture measured %d bytes, want < ACR_MAX_SERIALIZED_BYTES (%d) -- Run J's Wall C is an ITEMS wall, not a bytes wall", measuredBytes, productionMaxSerializedBytes)
	}

	app, token := newContextFabricTestAppWithProductionLimits(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}), nil)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 at the shipped ACR_MAX_ITEMS=%d default (measured %d total items, %d of them Paths that no longer count against the budget) body=%s",
			response.Code, productionMaxItems, totalItems, len(result.Paths), response.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Paths) != len(result.Paths) {
		t.Fatalf("response carried %d paths, want all %d -- excluding Paths from the item BUDGET must not drop them from the wire payload", len(got.Paths), len(result.Paths))
	}
	if len(got.ClaimedFacts) != len(result.ClaimedFacts) {
		t.Fatalf("response carried %d claimed facts, want all %d", len(got.ClaimedFacts), len(result.ClaimedFacts))
	}
}

// TestContextFabricInvestigationRouteManyClaimedFactsStillTripsItemBudget
// proves the item-count GATE still bites on genuine answer-content growth
// after CHAOS-4523 -- Paths is excluded from the charged count, but a
// result whose Candidates/Drivers/ClaimedFacts/CohortMembers alone exceed
// ACR_MAX_ITEMS must still 413, exactly as
// TestContextFabricInvestigationRouteCountsClaimedFactsTowardItemBudget
// already pins at the test-harness's generous 50-item ceiling. This
// repeats that property at the PRODUCTION 30-item default with the Run J
// fixture's own Paths/Candidates already present, so a regression that
// silently widened budgeted() back to total() would show up as a 200 here
// while still passing the shape-fit test above by coincidence -- and a
// regression that dropped items from the budget entirely would show up as
// this test alone going red.
func TestContextFabricInvestigationRouteManyClaimedFactsStillTripsItemBudget(t *testing.T) {
	result := runJRepositoryStatusShapedResult("result_4523_repo_status_over")
	repo := result.SubjectResolution.Committed[0]
	// Lightweight, Rows-free claims (unlike rowsBearingClaimedFact's 64-row
	// tables) so this fixture isolates the items bound: 30 more scalar
	// claims add 30 items but only a few KB, keeping total bytes well
	// under ACR_MAX_SERIALIZED_BYTES.
	inProgress := "in_progress"
	for i := 3; i < 33; i++ {
		result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
			ClaimID: "claim_repo_status_" + strconv.Itoa(i), Kind: contractsv1.ContextFabricFactStatus, Subject: repo,
			Field: "status", Value: contractsv1.ContextFabricScalarValue{String: &inProgress},
		})
	}
	measuredBytes := marshaledSize(t, result)
	if measuredBytes >= productionMaxSerializedBytes {
		t.Fatalf("fixture measured %d bytes, want < ACR_MAX_SERIALIZED_BYTES (%d) -- this must isolate the items bound", measuredBytes, productionMaxSerializedBytes)
	}

	app, token := newContextFabricTestAppWithProductionLimits(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}), nil)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 -- %d claimed facts plus 10 candidates and 1 driver (43 budgeted items) must still exceed ACR_MAX_ITEMS (%d) body=%s",
			response.Code, len(result.ClaimedFacts), productionMaxItems, response.Body.String())
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	breakdown, ok := envelope.Error.Details["items_breakdown"].(map[string]any)
	if !ok {
		t.Fatalf("error details = %#v, want an items_breakdown field (CHAOS-4523 telemetry deliverable) so an items 413 is diagnosable without re-running", envelope.Error.Details)
	}
	if paths, ok := breakdown["paths"].(float64); !ok || int64(paths) != 27 {
		t.Fatalf("items_breakdown.paths = %v, want 27", breakdown["paths"])
	}
	if claims, ok := breakdown["claimed_facts"].(float64); !ok || int64(claims) != int64(len(result.ClaimedFacts)) {
		t.Fatalf("items_breakdown.claimed_facts = %v, want %d", breakdown["claimed_facts"], len(result.ClaimedFacts))
	}
}
