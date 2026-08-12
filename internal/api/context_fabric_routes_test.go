package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// investigatorFunc adapts a plain function to contextfabric.Investigator so
// each test can return exactly the result/error it needs.
type investigatorFunc func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error)

func (f investigatorFunc) Investigate(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	return f(ctx, principal, request)
}

// newContextFabricTestApp mirrors newHostedTestAppWithUsageTelemetry
// (read_test_helpers_test.go) but also wires the given Investigator into
// RuntimeDependencies, which that shared helper does not expose.
func newContextFabricTestApp(t *testing.T, investigator contextfabric.Investigator) (*App, string) {
	t.Helper()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialLifecycle(t, audit, now)
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	token := issueScopedCredential(t, credentials, audit, now, []string{auth.ScopeContextRead}, []string{hostedTestRepository})
	entitlements := EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil })
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:    limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context: limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider := StaticCapabilitiesProvider{Now: func() time.Time { return now }, Value: hostedCapabilities()}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: provider, Limits: manager, Now: func() time.Time { return now },
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: entitlements,
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(func() time.Time { return now })),
			ReadinessChecks:            exactRuntimeChecks(),
			Investigator:               investigator,
		},
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	return app, token
}

type noopAssembler struct{}

func (noopAssembler) Assemble(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ContextPacket, error) {
	return contractsv1.ContextPacket{}, errors.New("not used by context fabric route tests")
}

type noopEvidenceStore struct{}

func (noopEvidenceStore) ResolveScope(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{}, nil
}
func (noopEvidenceStore) ContextForTask(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	return storage.EvidenceBundle{}, nil
}
func (noopEvidenceStore) ResolveEvidence(context.Context, storage.Principal, string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, storage.ErrNotFound
}

func investigationRequestBody() contractsv1.ContextFabricInvestigationRequest {
	return contractsv1.ContextFabricInvestigationRequest{
		SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema,
		RequestID:     "request_12345678",
		Question:      "Most of the work is closed, so why is Ask Dev still not ready to ship?",
		TimeContext:   contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		Options: contractsv1.ContextFabricInvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contractsv1.ContextFabricConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
	}
}

func investigationRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	body, err := json.Marshal(investigationRequestBody())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func validContextFabricInvestigationResult() contractsv1.ContextFabricInvestigationResult {
	project := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_route_test01", RequestID: "request_12345678", GeneratedAt: time.Now().UTC(),
		Status: contractsv1.ContextFabricInvestigationComplete, Question: "why is Ask Dev not ready to ship?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeSingleSubject, RequestedJudgment: "status",
			TimeContext:      contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements: []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{}, Committed: []contractsv1.ContextFabricSubjectRef{project}},
		DirectJudgment:    "Ask Dev is on track.", CurrentState: "Nominal.", StrongestPressures: []string{},
		Drivers: []contractsv1.ContextFabricDriverJudgment{}, RemainingWork: []contractsv1.ContextFabricFinding{},
		ReadinessGaps: []contractsv1.ContextFabricFinding{}, Paths: []contractsv1.ContextFabricRelationshipPath{},
		Conflicts: []contractsv1.ContextFabricFinding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{},
		Coverage:     contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{}, DegradedReasons: []string{}},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "test", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1",
		},
		DeterministicAnswer: "Ask Dev is on track based on available context.", Warnings: []string{},
	}
}

func TestContextFabricInvestigationRouteSucceeds(t *testing.T) {
	want := validContextFabricInvestigationResult()
	app, token := newContextFabricTestApp(t, investigatorFunc(func(_ context.Context, principal storage.Principal, _ contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		if principal.OrgID == "" {
			t.Fatal("investigator received an empty principal")
		}
		return want, nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var got contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ResultID != want.ResultID || got.Status != want.Status {
		t.Fatalf("result = %#v", got)
	}
}

func TestContextFabricInvestigationRouteMapsErrorsToStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"model rate limited", contextfabric.ErrModelRateLimited, http.StatusTooManyRequests},
		{"graph rate limited", contextfabric.ErrRateLimited, http.StatusTooManyRequests},
		{"graph unavailable", contextfabric.ErrUnavailable, http.StatusServiceUnavailable},
		{"model unavailable", contextfabric.ErrModelUnavailable, http.StatusServiceUnavailable},
		{"invalid model output", contextfabric.ErrModelOutput, http.StatusBadGateway},
		{"deadline exceeded", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"unclassified", errors.New("something unexpected broke"), http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, testCase.err
			}))
			response := httptest.NewRecorder()

			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			if response.Code != testCase.want {
				t.Fatalf("status = %d, want %d body=%s", response.Code, testCase.want, response.Body.String())
			}
		})
	}
}

// TestContextFabricInvestigationRouteContextCanceledWritesNoResponse proves
// the route matches the same "caller went away, don't bother writing a
// response" convention writeReadDependencyError already uses for other
// hosted read routes.
func TestContextFabricInvestigationRouteContextCanceledWritesNoResponse(t *testing.T) {
	app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, context.Canceled
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want the ResponseRecorder's default 200 (no status was ever written) body=%s", response.Code, response.Body.String())
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want nothing written for a canceled request", response.Body.String())
	}
}

func TestContextFabricInvestigationRouteWithoutInvestigatorReturns503(t *testing.T) {
	app, token := newContextFabricTestApp(t, nil)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when no investigator is configured", response.Code)
	}
}

// TestContextFabricInvestigationRouteRequiresAuthEvenWithoutInvestigator is
// the H5 probe (Codex adversarial review, CHAOS-3755): when the hosted
// investigator is not configured, the route must still run through the
// full protectedRuntimeHandler boundary (auth, scope, rate limit) before
// deciding it's unavailable -- an UNAUTHENTICATED caller must get the
// normal 401, not a 503 that both skips auth and reveals whether the
// investigator happens to be configured in this deployment.
func TestContextFabricInvestigationRouteRequiresAuthEvenWithoutInvestigator(t *testing.T) {
	app, _ := newContextFabricTestApp(t, nil)
	request := investigationRequest(t, "not-a-real-token")
	request.Header.Set("Authorization", "Bearer fcacr_totallyinvalidtoken0000000000")
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (auth must run before the nil-investigator check) body=%s", response.Code, response.Body.String())
	}
}

// TestContextFabricResultItemsCountsClaimedFacts is the M3 probe (Codex
// adversarial review, CHAOS-3755): ClaimedFacts is a real, potentially
// large response component (one entry per canonical-fact-shaped
// driver/finding) and must count toward the response's Items usage
// budget the same way Drivers/Paths/Findings already do -- omitting it
// would let a response with many claims under-report its own resource
// usage.
func TestContextFabricResultItemsCountsClaimedFacts(t *testing.T) {
	result := validContextFabricInvestigationResult()
	before := contextFabricResultItems(result)
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{
		{ClaimID: "claim_1", Kind: contractsv1.ContextFabricFactStatus, Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}, Field: "status", Value: contractsv1.ContextFabricScalarValue{String: ptrString("in_progress")}},
	}
	after := contextFabricResultItems(result)
	if after != before+1 {
		t.Fatalf("contextFabricResultItems() = %d after adding one claim (was %d), want %d", after, before, before+1)
	}
}

func ptrString(value string) *string { return &value }

// TestContextFabricInvestigationRouteUnsupportedTimeAxisIsClientError is
// the route half of the H6 fix: a historical or point-in-time question the
// engine refuses must surface as a 400 the caller can act on, NOT a 5xx
// that reads as an ACR outage and invites a retry that can never succeed.
func TestContextFabricInvestigationRouteUnsupportedTimeAxisIsClientError(t *testing.T) {
	app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, contextfabric.ErrUnsupportedTimeAxis
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Error struct {
			Retryable bool `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Retryable {
		t.Fatal("unsupported time axis was marked retryable, but retrying the same request can never succeed")
	}
}

// TestContextFabricInvestigationRouteCountsClaimedFactsTowardItemBudget is
// the M3 probe (Codex adversarial review, CHAOS-3755). ClaimedFacts was the
// one result collection excluded from the response item budget, so a result
// carrying an unbounded number of them billed as zero items and passed a
// budget it should have exceeded. The fixture's other collections are all
// empty, so the item count here is exactly the claimed-fact count.
func TestContextFabricInvestigationRouteCountsClaimedFactsTowardItemBudget(t *testing.T) {
	// MaxItems is 50 in the test limits policy (see newContextFabricTestApp).
	const overBudget = 51
	result := validContextFabricInvestigationResult()
	subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	ready := false
	for i := 0; i < overBudget; i++ {
		result.ClaimedFacts = append(result.ClaimedFacts, contractsv1.ContextFabricClaimedFact{
			ClaimID: "claim_" + strconv.Itoa(1000+i), Kind: contractsv1.ContextFabricFactStatus,
			Subject: subject, Field: "release_ready", Value: contractsv1.ContextFabricScalarValue{Boolean: &ready},
		})
	}
	app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return result, nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 -- %d claimed facts must exceed the 50-item budget body=%s", response.Code, overBudget, response.Body.String())
	}
}
