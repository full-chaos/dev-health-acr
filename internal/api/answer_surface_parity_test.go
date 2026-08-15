package api

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestAnswerSurfaceParityBetweenRealAPIAndRealMCP is the CHAOS-3746
// differential parity check.
//
// It drives BOTH REAL SURFACES against one another: a real api.App served
// over a real TLS listener, and the real acr-mcp server bootstrapped against
// that same listener, answering through the real sidecar client. It then
// requires the projection each returns to be byte-identical.
//
// An earlier version computed its "API side" by calling
// answerprojection.Project directly. That proved the projection helper is
// deterministic -- never in doubt -- while saying nothing about whether the
// shipped API returns the same thing, because at the time the API route did
// not project at all (codex round-1 F2). Comparing two real handlers is
// what makes the parity guarantee structural: if either surface ever grows
// its own narrowing, or the hosted view stops routing through the shared
// projection, this fails.
//
// Byte equality is the correct bar precisely because both sides are meant
// to run one function. Anything weaker would tolerate the drift this exists
// to catch.
func TestAnswerSurfaceParityBetweenRealAPIAndRealMCP(t *testing.T) {
	result := parityInvestigationResult()
	if err := result.Validate(); err != nil {
		t.Fatalf("parity fixture is not a valid canonical result: %v", err)
	}

	store := memoryinvestigation.NewStore()
	if err := store.Save(context.Background(), storage.Principal{OrgID: callerOrgID}, result, contextfabric.SourceWatermarkSnapshot{}, nil, contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}); err != nil {
		t.Fatalf("seed result: %v", err)
	}
	investigator := investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	})

	app, token := newParityHostedApp(t, investigator, store)
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)

	// The hosted API must actually advertise the answer tools, or the
	// sidecar will not register them and the comparison would be vacuous.
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("sidecar bootstrap against the real hosted API failed: %v", err)
	}
	assertAdvertised(t, boot.Capabilities.EnabledTools, "investigate_question", "investigation_result")

	budget := struct{ drivers, cohort, evidence int }{drivers: 3, cohort: 1, evidence: 10}

	mcpProjection := callRealMCPInvestigateQuestion(t, boot, result.Question, budget.drivers, budget.cohort, budget.evidence)
	apiProjection := getRealAPIProjection(t, server, token, result.ResultID, budget.drivers, budget.cohort, budget.evidence)

	mcpEncoded, err := json.Marshal(mcpProjection)
	if err != nil {
		t.Fatal(err)
	}
	apiEncoded, err := json.Marshal(apiProjection)
	if err != nil {
		t.Fatal(err)
	}
	if string(mcpEncoded) != string(apiEncoded) {
		t.Fatalf("the real MCP tool and the real API route returned different projections.\n MCP = %s\n API = %s", mcpEncoded, apiEncoded)
	}

	// Parity on an identical payload is only meaningful if the payload is
	// actually a narrowed answer. A budget that dropped nothing could pass
	// this test while hiding divergence in the truncation paths.
	if !apiProjection.ProjectionBudget.Truncated {
		t.Error("the parity budget dropped nothing; the comparison would not exercise truncation")
	}
	if apiProjection.DirectJudgment != result.DirectJudgment {
		t.Error("both surfaces agree with each other but disagree with the canonical judgment")
	}
}

// TestAPIProjectionViewRejectsAnUnknownView proves the view is a closed set
// and fails loudly. A caller who asked for a projection and silently
// received a canonical result would not notice the difference until
// something downstream broke.
func TestAPIProjectionViewRejectsAnUnknownView(t *testing.T) {
	store := memoryinvestigation.NewStore()
	seeded := seedResult(t, store, callerOrgID, "result_route_test01")
	app, token := newContextFabricTestAppWithResults(t, nil, store)

	for _, query := range []string{"?view=summary", "?view=projection&max_drivers=0", "?view=projection&max_drivers=999", "?max_drivers=3"} {
		t.Run(query, func(t *testing.T) {
			request := investigationResultRequest(t, token, seeded.ResultID)
			request.URL.RawQuery = query[1:]
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for %q", recorder.Code, query)
			}
		})
	}
}

// TestAPICanonicalViewRemainsTheDefault pins backwards compatibility: a
// caller that never heard of the view parameter keeps getting the canonical
// result.
func TestAPICanonicalViewRemainsTheDefault(t *testing.T) {
	store := memoryinvestigation.NewStore()
	seeded := seedResult(t, store, callerOrgID, "result_route_test01")
	app, token := newContextFabricTestAppWithResults(t, nil, store)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, seeded.ResultID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["schema_version"] != contractsv1.ContextFabricInvestigationResultSchema {
		t.Errorf("default view returned %v, want the canonical result contract", decoded["schema_version"])
	}
}

func assertAdvertised(t *testing.T, advertised []string, want ...string) {
	t.Helper()
	present := make(map[string]bool, len(advertised))
	for _, name := range advertised {
		present[name] = true
	}
	for _, name := range want {
		if !present[name] {
			t.Fatalf("hosted API did not advertise %q (advertised: %v); the parity comparison would be vacuous", name, advertised)
		}
	}
}

func configureSidecarEnvironment(t *testing.T, server *httptest.Server, token string) {
	t.Helper()
	caPath := filepath.Join(t.TempDir(), "answer-parity-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, caPath)
	t.Setenv(sidecar.TokenEnvironment, token)
}

// callRealMCPInvestigateQuestion drives the real MCP server over the SDK's
// in-memory transport, so the tool call goes through genuine protocol
// dispatch rather than a direct handler invocation.
func callRealMCPInvestigateQuestion(t *testing.T, boot *acrmcp.Bootstrap, question string, maxDrivers, maxCohort, maxEvidence int) contractsv1.ContextFabricAnswerProjection {
	t.Helper()
	ctx := context.Background()
	server := acrmcp.NewServer(boot, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "parity-client", Version: "0.0.1"}, nil)

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("mcp server connect: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	defer clientSession.Close()

	arguments, err := json.Marshal(contractsv1.MCPInvestigateQuestionRequest{
		Question: question,
		Budget: &contractsv1.MCPInvestigationBudget{
			MaxDrivers: maxDrivers, MaxCohortMembers: maxCohort, MaxEvidenceRefs: maxEvidence,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	called, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "investigate_question", Arguments: json.RawMessage(arguments),
	})
	if err != nil {
		t.Fatalf("investigate_question call: %v", err)
	}
	if called.IsError {
		t.Fatalf("investigate_question reported an error: %s", mustRawJSON(t, called.Content))
	}
	var response contractsv1.MCPInvestigateQuestionResponse
	if err := json.Unmarshal(mustRawJSON(t, called.StructuredContent), &response); err != nil {
		t.Fatalf("decode tool response: %v", err)
	}
	return response.Structured
}

// getRealAPIProjection requests the projection view from the real hosted
// route over real HTTP.
func getRealAPIProjection(t *testing.T, server *httptest.Server, token, resultID string, maxDrivers, maxCohort, maxEvidence int) contractsv1.ContextFabricAnswerProjection {
	t.Helper()
	url := server.URL + "/api/v1/context-fabric/investigations/" + resultID +
		"?view=projection" +
		"&max_drivers=" + strconv.Itoa(maxDrivers) +
		"&max_cohort_members=" + strconv.Itoa(maxCohort) +
		"&max_evidence_refs=" + strconv.Itoa(maxEvidence)

	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.2.5")

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("hosted projection request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("hosted projection status = %d, body %s", response.StatusCode, body)
	}
	var projection contractsv1.ContextFabricAnswerProjection
	if err := json.Unmarshal(body, &projection); err != nil {
		t.Fatalf("decode hosted projection: %v", err)
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("hosted projection failed contract validation: %v", err)
	}
	return projection
}

func mustRawJSON(t *testing.T, value any) []byte {
	t.Helper()
	if raw, ok := value.(json.RawMessage); ok {
		return raw
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("re-encode structured content: %v", err)
	}
	return encoded
}

// newParityHostedApp builds a hosted App wired the way a real deployment
// with the Context Fabric answer surface composed is wired: real
// capabilities sourced from contractsv1.AllSchemaVersions, both read scopes
// (the sidecar's startup gate requires context:read AND evidence:read), and
// both the investigator and the result store present so the capabilities
// handler actually advertises the answer tools.
func newParityHostedApp(t *testing.T, investigator contextfabric.Investigator, results contextfabric.InvestigationResultStore) (*App, string) {
	t.Helper()
	return newParityHostedAppWithBudget(t, investigator, results,
		limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20})
}

// newParityHostedAppWithBudget is newParityHostedApp with the per-request
// resource budget exposed, for a fixture whose payload is legitimately large
// (a legacy row carrying 100 full-length narratives serializes past the
// default token allowance, and a 413 there would hide the behaviour under
// test rather than exercise it).
func newParityHostedAppWithBudget(t *testing.T, investigator contextfabric.Investigator, results contextfabric.InvestigationResultStore, resources limits.ResourceBudget) (*App, string) {
	t.Helper()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialLifecycle(t, audit, now)
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	token := issueScopedCredential(t, credentials, audit, now,
		[]string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, []string{hostedTestRepository})
	entitlements := EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil })
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: resources},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 100},
	}})
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: 5 * time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Now: func() time.Time { return now }, Value: realHostedCapabilities()},
		Limits:       manager, Now: func() time.Time { return now },
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: entitlements,
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(func() time.Time { return now })),
			ReadinessChecks:            exactRuntimeChecks(),
			Investigator:               investigator,
			InvestigationResults:       results,
		},
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	return app, token
}

// parityInvestigationResult is deliberately rich enough that projecting it
// requires real choices: drivers across several standings including a
// withheld one, claimed facts, a cohort, and partial coverage. A thin
// fixture would let both surfaces agree trivially.
func parityInvestigationResult() contractsv1.ContextFabricInvestigationResult {
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	teamA := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team_a", Label: "Team A"}
	teamB := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: "team_b", Label: "Team B"}
	amber := "amber"

	build := func(id string, standing contractsv1.ContextFabricDriverStanding, category, title string, claims []string) contractsv1.ContextFabricDriverJudgment {
		return contractsv1.ContextFabricDriverJudgment{
			DriverID: id, Standing: standing, Category: category, Title: title,
			Summary:          title + " summary.",
			AffectedSubjects: []contractsv1.ContextFabricSubjectRef{project},
			EvidenceRefIDs:   []string{"evidence_" + id},
			ClaimedFactIDs:   claims,
			Derivation:       contractsv1.ContextFabricDerivationCanonicalStructured,
			EpistemicStatus:  contractsv1.ContextFabricEpistemicObserved,
			Confidence:       0.83, Current: true,
		}
	}
	withheld := build("driver_withheld_01", contractsv1.ContextFabricDriverWithheld, "narrative", "Withheld", nil)
	withheld.Qualification = "Evidence was too thin to stand behind."

	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_parity_0001", RequestID: "request_parity_001",
		GeneratedAt: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
		Status:      contractsv1.ContextFabricInvestigationComplete,
		Question:    "Which teams need attention on Ask Dev, and why?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeDiscoveredCohort, RequestedJudgment: "attention_and_drivers",
			TimeContext:      contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements: []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{
			Candidates: []contractsv1.ContextFabricSubjectCandidate{{
				ReceiptID: "receipt_parity_0001", Subject: project, State: contractsv1.ContextFabricResolutionCommitted,
				MatchReasons: []string{"exact label"}, Confidence: 0.98,
			}},
			Committed: []contractsv1.ContextFabricSubjectRef{project},
		},
		DirectJudgment:     "Two teams need attention on Ask Dev.",
		CurrentState:       "Blockers and stalled reviews concentrate on Team A.",
		StrongestPressures: []string{"open blockers", "stalled reviews"},
		Drivers: []contractsv1.ContextFabricDriverJudgment{
			build("driver_symptom_001", contractsv1.ContextFabricDriverSymptom, "reviews", "Reviews stalled", []string{"claim_reviews_01"}),
			build("driver_principal_1", contractsv1.ContextFabricDriverPrincipal, "blockers", "Blockers remain", []string{"claim_blockers_1"}),
			withheld,
			build("driver_principal_2", contractsv1.ContextFabricDriverPrincipal, "status", "Status is amber", []string{"claim_status_001"}),
			build("driver_contrib_001", contractsv1.ContextFabricDriverContributing, "work", "Work outstanding", []string{"claim_work_00001"}),
		},
		RemainingWork: []contractsv1.ContextFabricFinding{},
		ReadinessGaps: []contractsv1.ContextFabricFinding{},
		Paths:         []contractsv1.ContextFabricRelationshipPath{},
		Conflicts:     []contractsv1.ContextFabricFinding{},
		Cohort: &contractsv1.ContextFabricCohort{
			Kind: contractsv1.ContextFabricSubjectTeam,
			Members: []contractsv1.ContextFabricCohortMember{
				{Subject: teamA, Rank: 1, InclusionReasons: []string{"highest blocker load"}, EvidenceRefIDs: []string{"evidence_cohort_a"}},
				{Subject: teamB, Rank: 2, InclusionReasons: []string{"rising review latency"}, EvidenceRefIDs: []string{"evidence_cohort_b"}},
			},
			Rationale: "Teams carrying the most open blockers.", Complete: true,
		},
		Limitations:    []string{"deployments source unavailable"},
		EvidenceRefIDs: []string{"evidence_driver_principal_1"},
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{
			{ClaimID: "claim_blockers_1", Kind: contractsv1.ContextFabricFactBlockers, Subject: project, Field: "open_blockers", Value: contractsv1.ContextFabricScalarValue{String: &amber}},
			{ClaimID: "claim_status_001", Kind: contractsv1.ContextFabricFactStatus, Subject: project, Field: "status", Value: contractsv1.ContextFabricScalarValue{String: &amber}},
			{ClaimID: "claim_reviews_01", Kind: contractsv1.ContextFabricFactReviews, Subject: project, Field: "stalled_reviews", Value: contractsv1.ContextFabricScalarValue{String: &amber}},
			{ClaimID: "claim_work_00001", Kind: contractsv1.ContextFabricFactWork, Subject: project, Field: "open_items", Value: contractsv1.ContextFabricScalarValue{String: &amber}},
		},
		Coverage: contractsv1.ContextFabricCoverage{
			Sources: []contractsv1.ContextFabricSourceObservation{
				{Source: "work_items", State: contractsv1.ContextFabricSourceAvailable},
				{Source: "deployments", State: contractsv1.ContextFabricSourceUnavailable, Reason: "source not configured"},
			},
			Partial: true, DegradedReasons: []string{"deployments unavailable"},
		},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1",
		},
		DeterministicAnswer: "Two teams need attention because blockers and stalled reviews concentrate there.",
		Warnings:            []string{},
	}
}

// legacyResultStore serves a stored result WITHOUT re-validating it against
// the write bounds.
//
// This models the only situation the narrative clamp path exists for. The
// write bounds cap limitations at 100 entries of 2000 runes, and
// memoryinvestigation.Save enforces exactly that, so no result saved today
// can reach boundedNarrative's clamp or its cap. The stored-read bounds
// still accept the historical 250 x 4000 (contextFabricLegacyBounds), which
// is what a row written before CHAOS-3746 round 3 tightened them looks like.
// Such a row is legitimately readable and must project correctly.
type legacyResultStore struct {
	result contractsv1.ContextFabricInvestigationResult
}

func (s legacyResultStore) Save(context.Context, storage.Principal, contextfabric.InvestigationResult, contextfabric.SourceWatermarkSnapshot, contextfabric.RebuildEpoch, string, contextfabric.ReuseRetrievalIdentity) error {
	return nil
}

func (s legacyResultStore) Get(_ context.Context, _ storage.Principal, resultID string) (contextfabric.InvestigationResult, error) {
	if resultID != s.result.ResultID {
		return contractsv1.ContextFabricInvestigationResult{}, contextfabric.ErrInvestigationResultNotFound
	}
	return s.result, nil
}

// TestValuesClampedCountsOnlySurvivorsAtTheRealSurfaces is the surface-level
// half of codex round-10 F1.
//
// The projection helper is shared, so one fix covers both surfaces -- but
// "shared helper" is a claim about today's wiring, not a property, and the
// wiring is exactly what round-1 F2 showed can be wrong. So the count is
// checked where a consumer actually reads it.
//
// Reachability differs by surface, and that asymmetry is asserted rather
// than assumed:
//
//   - The API projection view reads a STORED result, so it can be asked to
//     project a legacy row and is where the clamp path is genuinely live.
//   - investigate_question runs a NEW investigation, and the hosted write
//     path rejects a result carrying legacy-shaped narratives, so the clamp
//     is not reachable there. That is correct: a result produced today must
//     satisfy today's bounds. The MCP case below pins the consequence
//     without hard-coding today's error, so it still holds if that path
//     ever starts succeeding.
//
// The fixture carries 101 distinct over-long limitations. The projection
// keeps 100 and drops one, so a correct values_clamped is 100: the dropped
// entry never reached the wire in any form, shortened or otherwise.
func TestValuesClampedCountsOnlySurvivorsAtTheRealSurfaces(t *testing.T) {
	const overflow = contractsv1.ContextFabricProjectedNarrativeMaxCount + 1

	result := parityInvestigationResult()
	limitations := make([]string, 0, overflow)
	for i := 0; i < overflow; i++ {
		limitations = append(limitations,
			"legacy limitation "+strconv.Itoa(i)+" "+strings.Repeat("x", contractsv1.ContextFabricProjectedNarrativeMaxLength))
	}
	result.Limitations = limitations

	// The fixture must be a legitimately READABLE stored row, or the test
	// would prove the projection tolerates something the service would have
	// rejected on the way in.
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("the legacy fixture is not a valid stored result: %v", err)
	}
	if err := result.Validate(); err == nil {
		t.Fatal("the fixture passes the WRITE bounds, so it does not model a legacy row and the clamp path would not be exercised")
	}

	store := legacyResultStore{result: result}
	investigator := investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	})

	app, token := newParityHostedAppWithBudget(t, investigator, store,
		limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20})
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)

	t.Run("API projection view over a legacy stored row", func(t *testing.T) {
		projection := getRealAPIProjection(t, server, token, result.ResultID, 3, 1, 10)
		budget := projection.ProjectionBudget
		if got := len(projection.Limitations); got != contractsv1.ContextFabricProjectedNarrativeMaxCount {
			t.Fatalf("limitations on the wire = %d, want %d", got, contractsv1.ContextFabricProjectedNarrativeMaxCount)
		}
		if budget.LimitationsOmitted != 1 {
			t.Errorf("limitations_omitted = %d, want 1", budget.LimitationsOmitted)
		}
		if budget.ValuesClamped != contractsv1.ContextFabricProjectedNarrativeMaxCount {
			t.Errorf("values_clamped = %d, want %d: it must count only values this surface actually returned in shortened form, never one dropped by the count cap",
				budget.ValuesClamped, contractsv1.ContextFabricProjectedNarrativeMaxCount)
		}
	})

	t.Run("MCP investigate_question cannot mint a legacy-shaped answer", func(t *testing.T) {
		boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
		if err != nil {
			t.Fatalf("sidecar bootstrap: %v", err)
		}
		assertAdvertised(t, boot.Capabilities.EnabledTools, "investigate_question", "investigation_result")

		projection, ok := tryRealMCPInvestigateQuestion(t, boot, result.Question, 3, 1, 10)
		if !ok {
			// The hosted write path refused to emit a fresh result carrying
			// legacy-shaped narratives. That is the correct outcome and the
			// reason this surface cannot reach the clamp path at all.
			return
		}
		// If it ever does succeed, the counter must obey the same rule.
		if got := projection.ProjectionBudget.ValuesClamped; got > len(projection.Limitations) {
			t.Errorf("values_clamped = %d exceeds the %d limitations actually returned, so it is counting values the caller never received",
				got, len(projection.Limitations))
		}
	})
}

// tryRealMCPInvestigateQuestion is callRealMCPInvestigateQuestion without the
// assertion that the call succeeded, for cases where a refusal is a valid --
// indeed the expected -- outcome.
func tryRealMCPInvestigateQuestion(t *testing.T, boot *acrmcp.Bootstrap, question string, maxDrivers, maxCohort, maxEvidence int) (contractsv1.ContextFabricAnswerProjection, bool) {
	t.Helper()
	ctx := context.Background()
	server := acrmcp.NewServer(boot, "test-version")
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "clamp-client", Version: "0.0.1"}, nil)

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("mcp server connect: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	defer clientSession.Close()

	arguments, err := json.Marshal(contractsv1.MCPInvestigateQuestionRequest{
		Question: question,
		Budget: &contractsv1.MCPInvestigationBudget{
			MaxDrivers: maxDrivers, MaxCohortMembers: maxCohort, MaxEvidenceRefs: maxEvidence,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	called, err := clientSession.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "investigate_question", Arguments: json.RawMessage(arguments),
	})
	if err != nil || called.IsError {
		return contractsv1.ContextFabricAnswerProjection{}, false
	}
	var response contractsv1.MCPInvestigateQuestionResponse
	if err := json.Unmarshal(mustRawJSON(t, called.StructuredContent), &response); err != nil {
		t.Fatalf("decode tool response: %v", err)
	}
	return response.Structured, true
}
