package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	return newContextFabricTestAppWithResults(t, investigator, nil)
}

// newContextFabricTestAppWithResults additionally wires the CHAOS-3746
// investigation result store the retrieval route reads. A nil store is the
// "not configured" case that route degrades to a 503 for.
func newContextFabricTestAppWithResults(t *testing.T, investigator contextfabric.Investigator, results contextfabric.InvestigationResultStore) (*App, string) {
	t.Helper()
	app, token, _ := newContextFabricTestAppWithResultsAndLogs(t, investigator, results)
	return app, token
}

// newContextFabricTestAppWithLogs is newContextFabricTestApp plus the log
// buffer, for the CHAOS-3811 assertions on what a failed investigation
// actually records.
func newContextFabricTestAppWithLogs(t *testing.T, investigator contextfabric.Investigator) (*App, string, *bytes.Buffer) {
	t.Helper()
	return newContextFabricTestAppWithResultsAndLogs(t, investigator, nil)
}

// newContextFabricTestAppWithResultsAndLogs is the one constructor the three
// wrappers above narrow: CHAOS-3746 needed the result store, CHAOS-3811 needed
// the log buffer, and a failure on the retrieval route needs both.
func newContextFabricTestAppWithResultsAndLogs(t *testing.T, investigator contextfabric.Investigator, results contextfabric.InvestigationResultStore) (*App, string, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
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
			InvestigationResults:       results,
		},
	}, testLogger(logs))
	if err != nil {
		t.Fatal(err)
	}
	return app, token, logs
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
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1", ModelIdentity: "test/model-v1",
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
		{"provider error", contextfabric.ErrModelOutput, http.StatusBadGateway},
		{"interpretation rejected", contextfabric.ErrInterpretationRejected, http.StatusUnprocessableEntity},
		{"synthesis rejected", contextfabric.ErrSynthesisRejected, http.StatusUnprocessableEntity},
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

// TestContextFabricInvestigationRouteReportsDistinctCodesAndBoundNames is
// the CHAOS-3784 probe: interpretation rejections, synthesis rejections,
// and provider errors must report distinct machine-readable `code` values,
// and a bound violation (as opposed to a business-rule rejection) must
// carry the violated bound's name in `details.violated_bound` -- never raw
// model output.
func TestContextFabricInvestigationRouteReportsDistinctCodesAndBoundNames(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantBound   string
		wantNoBound bool
	}{
		{
			name:       "interpretation bound violation names the bound",
			err:        fmt.Errorf("%w: %v", contextfabric.ErrInterpretationRejected, errors.New("interpreted question violates v1 bounds")),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "interpretation_rejected",
			wantBound: "interpretation.requested_judgment.max_length",
		},
		{
			name:       "interpretation business rule carries no bound",
			err:        contextfabric.ErrInterpretationRejected,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "interpretation_rejected", wantNoBound: true,
		},
		{
			name:       "synthesis bound violation names the bound",
			err:        fmt.Errorf("%w: %v", contextfabric.ErrSynthesisRejected, errors.New("driver judgment violates v1 bounds")),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "synthesis_rejected",
			wantBound: "synthesis.driver.title.max_length",
		},
		{
			name:       "synthesis claim-binding rejection carries no bound",
			err:        contextfabric.ErrSynthesisRejected,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "synthesis_rejected", wantNoBound: true,
		},
		{
			name:       "upstream invalid output carries no bound",
			err:        contextfabric.ErrModelOutput,
			wantStatus: http.StatusBadGateway, wantCode: "upstream_invalid_output", wantNoBound: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.err
			if testCase.wantBound != "" {
				err = contextfabric.NewModelBoundViolation(testCase.wantBound, err)
			}
			app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, err
			}))
			response := httptest.NewRecorder()

			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			var body contractsv1.ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v (body=%s)", err, response.Body.String())
			}
			if body.Error.Code != testCase.wantCode {
				t.Fatalf("code = %q, want %q", body.Error.Code, testCase.wantCode)
			}
			gotBound, _ := body.Error.Details["violated_bound"].(string)
			if testCase.wantNoBound {
				if _, present := body.Error.Details["violated_bound"]; present {
					t.Fatalf("details.violated_bound = %q, want absent", gotBound)
				}
				return
			}
			if gotBound != testCase.wantBound {
				t.Fatalf("details.violated_bound = %q, want %q", gotBound, testCase.wantBound)
			}
		})
	}
}

// routeTestModelRuntime is a minimal contextfabric.ModelRuntime a route
// test can wire directly into contextfabric.RuntimeQuestionInterpreter /
// RuntimeAnswerSynthesizer, so a route test exercises the REAL
// classification code (question.Validate(), draft.ValidateAgainst(),
// contextfabric.ClassifyInterpretationRejection/ClassifySynthesisRejection)
// instead of a hand-built *contextfabric.ModelBoundViolation (CHAOS-3784
// round-2 F6).
type routeTestModelRuntime struct {
	interpreted   contextfabric.InterpretedQuestion
	interpretErr  error
	draft         contextfabric.SynthesisDraft
	synthesizeErr error
}

func (f routeTestModelRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return f.interpreted, validRouteTestModelReceipt(contextfabric.ModelOperationInterpret), f.interpretErr
}

func (f routeTestModelRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return f.draft, validRouteTestModelReceipt(contextfabric.ModelOperationSynthesize), f.synthesizeErr
}

type noopModelReceiptSink struct{}

func (noopModelReceiptSink) RecordModelExecution(context.Context, storage.Principal, contextfabric.ModelExecutionReceipt) error {
	return nil
}

func validRouteTestModelReceipt(operation contextfabric.ModelOperation) contextfabric.ModelExecutionReceipt {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return contextfabric.ModelExecutionReceipt{
		Operation: operation, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: "prompt-v1", SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1",
		StartedAt: now, CompletedAt: now.Add(time.Second), Attempts: 1,
		InputDigest: contextfabric.DigestModelValue([]byte("route-test-input")),
	}
}

func validRouteTestSynthesisInput() contextfabric.SynthesisInput {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return contextfabric.SynthesisInput{
		Request: investigationRequestDomain(),
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		},
		Graph: contextfabric.GraphContext{
			Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{project}},
			Coverage:   contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
		},
		Facts: contextfabric.CanonicalFactBundle{
			Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1",
		},
	}
}

func validRouteTestSynthesisDraft() contextfabric.SynthesisDraft {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	return contextfabric.SynthesisDraft{
		Status: contextfabric.InvestigationComplete, DirectJudgment: "Ask Dev is not release-ready.",
		CurrentState: "Diverges.", StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{},
		ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{},
		EvidenceRefIDs: []string{}, Warnings: []string{},
		Drivers: []contextfabric.DriverJudgment{{
			DriverID: "driver_12345678", Standing: contextfabric.DriverPrincipal, Category: "relationship",
			Title: "Release acceptance remains open", Summary: "Required acceptance has not completed.",
			AffectedSubjects: []contextfabric.SubjectRef{project}, Derivation: contextfabric.DerivationRuleInferred,
			EpistemicStatus: contextfabric.EpistemicInferred, Confidence: 0.9, Current: true,
			EvidenceRefIDs: []string{},
		}},
		DeterministicAnswer: "Ask Dev is not release-ready because release acceptance remains open.",
	}
}

// investigationRequestDomain returns the exact contextfabric.InvestigationRequest
// (a type alias of contractsv1.ContextFabricInvestigationRequest) the route
// decodes investigationRequest's HTTP body into, so a fake ModelRuntime
// wired through contextfabric.RuntimeQuestionInterpreter sees the identical
// request the route itself would pass to a real investigator.
func investigationRequestDomain() contextfabric.InvestigationRequest {
	return investigationRequestBody()
}

// TestContextFabricInvestigationRouteEndToEndClassifiesRealFailures is the
// CHAOS-3784 round-2 F6 probe: unlike
// TestContextFabricInvestigationRouteReportsDistinctCodesAndBoundNames
// (which unit-tests the route's own switch against hand-built errors), this
// drives a fake ModelRuntime's genuinely invalid output through the REAL
// contextfabric.RuntimeQuestionInterpreter/RuntimeAnswerSynthesizer
// classification code (question.Validate(), draft.ValidateAgainst(),
// Classify*Rejection) and the route, then binds the FULL response contract:
// status, code, http_status, retryable, request ID, a fixed (non-model)
// message, and details.violated_bound where expected -- plus a negative
// check that neither the model's own text nor a raw provider error string
// ever reaches the body.
func TestContextFabricInvestigationRouteEndToEndClassifiesRealFailures(t *testing.T) {
	const secretModelText = "SECRET MODEL PROSE ACR MUST NEVER RETURN"
	cases := []struct {
		name        string
		investigate func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error)
		wantStatus  int
		wantCode    string
		wantBound   string
	}{
		{
			name: "real interpretation bound violation",
			investigate: func(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				invalid := contextfabric.InterpretedQuestion{
					Shape: contextfabric.ShapeOpen, RequestedJudgment: secretModelText + strings.Repeat("a", 259),
					TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				}
				interpreter := contextfabric.RuntimeQuestionInterpreter{
					Runtime: routeTestModelRuntime{interpreted: invalid}, Sink: noopModelReceiptSink{},
				}
				_, err := interpreter.Interpret(ctx, principal, request)
				return contextfabric.InvestigationResult{}, err
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "interpretation_rejected",
			wantBound: "interpretation.requested_judgment.max_length",
		},
		{
			name: "real synthesis bound violation",
			investigate: func(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				input := validRouteTestSynthesisInput()
				draft := validRouteTestSynthesisDraft()
				draft.Drivers[0].Title = secretModelText + strings.Repeat("a", 513)
				synthesizer := contextfabric.RuntimeAnswerSynthesizer{
					Runtime: routeTestModelRuntime{draft: draft}, Sink: noopModelReceiptSink{},
				}
				_, err := synthesizer.Synthesize(ctx, principal, input)
				return contextfabric.InvestigationResult{}, err
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "synthesis_rejected",
			wantBound: "synthesis.driver.title.max_length",
		},
		{
			name: "real upstream invalid output",
			investigate: func(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				interpreter := contextfabric.RuntimeQuestionInterpreter{
					Runtime: routeTestModelRuntime{interpretErr: fmt.Errorf("%w: provider status INTERNAL: %s", contextfabric.ErrModelOutput, secretModelText)},
					Sink:    noopModelReceiptSink{},
				}
				_, err := interpreter.Interpret(ctx, principal, request)
				return contextfabric.InvestigationResult{}, err
			},
			wantStatus: http.StatusBadGateway, wantCode: "upstream_invalid_output",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, token := newContextFabricTestApp(t, investigatorFunc(testCase.investigate))
			response := httptest.NewRecorder()

			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			raw := response.Body.String()
			if strings.Contains(raw, secretModelText) {
				t.Fatalf("response body leaked model/provider text: %s", raw)
			}
			var body contractsv1.ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v (body=%s)", err, raw)
			}
			if body.RequestID == "" {
				t.Fatalf("request_id is empty: %s", raw)
			}
			if body.Error.Code != testCase.wantCode {
				t.Fatalf("code = %q, want %q", body.Error.Code, testCase.wantCode)
			}
			if body.Error.HTTPStatus != testCase.wantStatus {
				t.Fatalf("http_status = %d, want %d", body.Error.HTTPStatus, testCase.wantStatus)
			}
			if !body.Error.Retryable {
				t.Fatalf("retryable = false, want true")
			}
			if body.Error.Message == "" || strings.Contains(body.Error.Message, secretModelText) {
				t.Fatalf("message = %q", body.Error.Message)
			}
			gotBound, _ := body.Error.Details["violated_bound"].(string)
			if testCase.wantBound == "" {
				if _, present := body.Error.Details["violated_bound"]; present {
					t.Fatalf("details.violated_bound = %q, want absent", gotBound)
				}
				return
			}
			if gotBound != testCase.wantBound {
				t.Fatalf("details.violated_bound = %q, want %q", gotBound, testCase.wantBound)
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

// TestContextFabricInvestigationRouteInvalidTimeBoundIsClientError is the
// route half of CHAOS-3781, replacing the H6 mapping it succeeded.
//
// The H6 version asserted that any non-current axis surfaced as a 400.
// That is now wrong: historical questions are answered. What still
// surfaces as a 400 is a request whose BOUNDS are not answerable -- a time
// in the future, or a range wider than this service reads -- and it must
// still be a 400 rather than a 5xx, because a 5xx reads as an ACR outage
// and invites a retry that can never succeed.
func TestContextFabricInvestigationRouteInvalidTimeBoundIsClientError(t *testing.T) {
	app, token := newContextFabricTestApp(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, contextfabric.ErrInvalidTimeBound
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
		t.Fatal("an unanswerable time bound was marked retryable, but retrying the same request can never succeed")
	}
}

// TestContextFabricInvestigationRouteAnswersAHistoricalQuestion is the
// AC-3781-6 guard at this layer: no stale refusal may survive here. A
// historical request that the engine answers must reach the caller as a
// 200 carrying the temporal label, not as the 400 this route used to
// return for every non-current axis.
func TestContextFabricInvestigationRouteAnswersAHistoricalQuestion(t *testing.T) {
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	app, token := newContextFabricTestApp(t, investigatorFunc(func(_ context.Context, _ storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		result := validContextFabricInvestigationResult()
		result.Interpretation.TimeContext = contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &asOf}
		result.Temporal = &contractsv1.ContextFabricTemporalLabel{
			Requested:        contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &asOf},
			Effective:        contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &asOf},
			Grain:            contractsv1.ContextFabricGrainDay,
			CoverageComplete: false,
		}
		return result, nil
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Temporal *struct {
			Grain string `json:"grain"`
		} `json:"temporal"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Temporal == nil {
		t.Fatal("the temporal label did not survive to the wire; a historical answer must state the time it speaks for")
	}
	if payload.Temporal.Grain != string(contractsv1.ContextFabricGrainDay) {
		t.Fatalf("grain = %q, want %q", payload.Temporal.Grain, contractsv1.ContextFabricGrainDay)
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

// CHAOS-4088. contextFabricInnermostErrorType walks to the deepest
// single-Unwrap() node and stops there: it must not panic or infinite-loop
// on a plain error, a single wrap, or a double wrap (Unwrap() []error,
// which errors.Unwrap deliberately does not follow).
func TestContextFabricInnermostErrorType(t *testing.T) {
	plain := errors.New("boom")
	if got := contextFabricInnermostErrorType(plain); got != "*errors.errorString" {
		t.Fatalf("contextFabricInnermostErrorType(plain) = %q, want %q", got, "*errors.errorString")
	}

	wrapped := fmt.Errorf("outer: %w", plain)
	if got := contextFabricInnermostErrorType(wrapped); got != "*errors.errorString" {
		t.Fatalf("contextFabricInnermostErrorType(wrapped) = %q, want the wrapped cause's type %q", got, "*errors.errorString")
	}

	// A double-%w wrap implements Unwrap() []error, which errors.Unwrap does
	// not follow -- the walk must stop AT this node (its own type), not
	// panic or pick one branch arbitrarily.
	doubleWrapped := fmt.Errorf("%w: %w", plain, errors.New("second"))
	got := contextFabricInnermostErrorType(doubleWrapped)
	if got == "*errors.errorString" {
		t.Fatalf("contextFabricInnermostErrorType(doubleWrapped) = %q, want it to stop at the multi-error node's own type, not descend into it", got)
	}
}
