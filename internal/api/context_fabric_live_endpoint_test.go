package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// Live endpoint acceptance for CHAOS-3770.
//
// Every other test in this package drives the investigation route with a
// stubbed Investigator, which proves the route's own behavior but never
// exercises a model. This one drives the SAME route -- real authentication,
// scope, entitlement, rate limiting, resource accounting, audit, and JSON
// encoding -- against a real contextfabric.Engine whose model runtime is the
// real provider built by internal/contextfabric/modelprovider. It answers the
// question CHAOS-3770 actually asks: does a real investigation come back
// through the endpoint, not a 503.
//
// GraphReader and CanonicalFactReader are fakes here, deliberately and for
// the same reason internal/contextfabric/acceptance_test.go fakes them: they
// have their own dedicated suites (CHAOS-3752/3754 and the fact registry),
// and a live graph backend would add a container dependency without changing
// what this test proves. What is NOT faked is the part CHAOS-3770 owns --
// the model runtime, the value-level closure that binds the model's claims to
// the canonical facts, and the endpoint itself.
//
// Skipped unless a credential is supplied:
//
//	ACR_TEST_MODEL_API_KEY=... go test ./internal/api -run LiveEndpoint -v
func TestLiveEndpointAnswersARealInvestigation(t *testing.T) {
	apiKey := os.Getenv("ACR_TEST_MODEL_API_KEY")
	if apiKey == "" {
		t.Skip("ACR_TEST_MODEL_API_KEY is not set; skipping live endpoint acceptance")
	}

	// Given a real model runtime and a real engine behind the route.
	modelConfig := modelprovider.Config{
		Provider: modelprovider.DefaultProvider, Model: modelprovider.DefaultModel,
		APIKey: apiKey, Timeout: 90 * time.Second, MaxAttempts: 2, MaxTransportRetries: 2,
	}
	if value := os.Getenv("ACR_TEST_MODEL"); value != "" {
		modelConfig.Model = value
	}
	// Setting a fallback exercises the production mitigation for a primary
	// model that fails value-level closure: genkitruntime retries the whole
	// operation on the stronger model when the primary's output does not
	// validate.
	if value := os.Getenv("ACR_TEST_MODEL_FALLBACK"); value != "" {
		modelConfig.FallbackModel = value
	}
	modelRuntime, err := modelprovider.New(context.Background(), modelConfig)
	if err != nil {
		t.Fatal(err)
	}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	facts := liveCanonicalFacts(project)
	results := memoryinvestigation.NewStore()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: modelRuntime},
		Graph:       liveGraphReader{project: project},
		Facts:       liveFactReader{bundle: facts},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: modelRuntime, Options: contextfabric.RuntimeAnswerSynthesizerOptions{
			ServiceVersion: "live-endpoint-acceptance", Backend: "graph",
		}},
		Results: results,
	}, contextfabric.EngineOptions{
		ServiceVersion: "live-endpoint-acceptance",
		Now:            func() time.Time { return time.Now().UTC() },
		NewResultID:    func() string { return "result_live_acceptance01" },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := newLiveContextFabricTestApp(t, engine)

	// When a real client posts a real question to the real route.
	request := investigationRequest(t, token)
	recorder := httptest.NewRecorder()
	started := time.Now()
	app.Handler().ServeHTTP(recorder, request)
	latency := time.Since(started)

	// Then
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s), want 200 -- a 503 here means the model runtime was not wired",
			recorder.Code, recorder.Body.String())
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("returned result failed its own contract validation: %v", err)
	}
	switch result.Status {
	case contractsv1.ContextFabricInvestigationComplete, contractsv1.ContextFabricInvestigationPartial:
	default:
		t.Fatalf("status = %q, want an answer-capable status", result.Status)
	}
	if result.DirectJudgment == "" || result.DeterministicAnswer == "" || len(result.Drivers) == 0 {
		t.Fatalf("result is not independently useful: %#v", result)
	}
	// The point of value-level closure: every claim the answer rests on must
	// restate a canonical fact field/value verbatim. Assert it here directly
	// rather than trusting that SynthesisDraft.ValidateAgainst ran.
	if len(result.ClaimedFacts) == 0 {
		t.Fatal("result carries no claimed facts; nothing binds the answer to canonical data")
	}
	for _, claim := range result.ClaimedFacts {
		if !liveClaimMatchesCanonicalFact(claim, facts) {
			t.Fatalf("claimed fact %q (%s.%s) does not match any canonical fact supplied to the engine", claim.ClaimID, claim.Kind, claim.Field)
		}
	}
	// A canonical-fact-shaped driver must cite one of those claims.
	cited := 0
	for _, driver := range result.Drivers {
		cited += len(driver.ClaimedFactIDs)
	}
	if cited == 0 {
		t.Fatal("no driver cites a claimed fact; the answer is not closed to canonical values")
	}
	// answer_length only, never the answer text itself -- a live test log
	// must not become a place answer content leaks into (CHAOS-3770 F1;
	// AC-3770-5).
	t.Logf("live endpoint acceptance: model=%s status=%s latency=%s drivers=%d claimed_facts=%d bytes=%d answer_length=%d",
		modelConfig.Model, result.Status, latency.Round(time.Millisecond), len(result.Drivers),
		len(result.ClaimedFacts), recorder.Body.Len(), len(result.DeterministicAnswer))
}

// TestLiveEndpointReturnsCleanFiveOhThreeWithoutAModelRuntime exercises the
// preserved CHAOS-3755 behavior through the same live-configured route: an
// engine composed exactly like the one above, but with the model runtime left
// nil, must answer 503 upstream_unavailable rather than failing some other
// way. This runs without a credential, so it is a permanent guard rather than
// a live-only probe.
func TestLiveEndpointReturnsCleanFiveOhThreeWithoutAModelRuntime(t *testing.T) {
	// Given the same composition with no model runtime.
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	results := memoryinvestigation.NewStore()
	var modelRuntime contextfabric.ModelRuntime
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: modelRuntime},
		Graph:       liveGraphReader{project: project},
		Facts:       liveFactReader{bundle: liveCanonicalFacts(project)},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: modelRuntime, Options: contextfabric.RuntimeAnswerSynthesizerOptions{
			ServiceVersion: "live-endpoint-acceptance", Backend: "graph",
		}},
		Results: results,
	}, contextfabric.EngineOptions{
		ServiceVersion: "live-endpoint-acceptance",
		Now:            func() time.Time { return time.Now().UTC() },
		NewResultID:    func() string { return "result_live_acceptance01" },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := newLiveContextFabricTestApp(t, engine)

	// When
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationRequest(t, token))

	// Then
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (body %s), want 503", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "upstream_unavailable" || !body.Error.Retryable {
		t.Fatalf("error = %#v, want a retryable upstream_unavailable", body.Error)
	}
}

// newLiveContextFabricTestApp mirrors newContextFabricTestApp but allows a
// request timeout long enough for a real model call. The shared helper pins
// RequestTimeout to one second, which every stubbed-investigator test wants
// and a live one cannot use.
func newLiveContextFabricTestApp(t *testing.T, investigator contextfabric.Investigator) (*App, string) {
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
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: 5 * time.Minute}, Dependencies{
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

type liveGraphReader struct{ project contextfabric.SubjectRef }

func (g liveGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "live-key", Epoch: 0}, nil
}

func (g liveGraphReader) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, error) {
	// CHAOS-4085: nil CommitBasisSet -- every commit this double returns reads
	// back as CommitBasisUnknown, the strict (must-be-affirmed) treatment.
	return contextfabric.SubjectResolution{
		Candidates: []contextfabric.SubjectCandidate{},
		Committed:  []contextfabric.SubjectRef{g.project},
	}, contextfabric.StructureOfferMaterial{}, nil, nil
}

func (g liveGraphReader) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	acceptance := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_release_acceptance", Label: "Release acceptance"}
	return contextfabric.GraphContext{
		Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{g.project}},
		Paths: []contextfabric.RelationshipPath{{
			PathID: "path_release_0001",
			Nodes:  []contextfabric.SubjectRef{g.project, acceptance},
			Edges: []contextfabric.RelationshipEdge{{
				Type: "REQUIRES", From: g.project, To: acceptance,
				Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
				EvidenceRefIDs: []string{"evidence_release_0001"},
			}},
			WhyRelevant:    "Release acceptance is still open and gates the release.",
			EvidenceRefIDs: []string{"evidence_release_0001"},
		}},
		DriverCandidates: []contextfabric.DriverJudgment{},
		EvidenceRefIDs:   []string{"evidence_release_0001"},
		FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{g.project}}},
		Coverage:         contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type liveFactReader struct {
	bundle contextfabric.CanonicalFactBundle
}

func (f liveFactReader) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return f.bundle, nil
}

func liveCanonicalFacts(project contextfabric.SubjectRef) contextfabric.CanonicalFactBundle {
	return contextfabric.CanonicalFactBundle{
		Facts: []contextfabric.CanonicalFact{
			{
				Kind: contextfabric.FactStatus, Subject: project,
				Fields:         map[string]contextfabric.FactValue{"status": contextfabric.StringFactValue("in_progress")},
				EvidenceRefIDs: []string{"evidence_status_0001"}, SourceState: contextfabric.SourceAvailable,
				Source: "ops", SourceVersion: "v1",
			},
			{
				Kind: contextfabric.FactReadiness, Subject: project,
				Fields:         map[string]contextfabric.FactValue{"release_ready": contextfabric.BooleanFactValue(false)},
				EvidenceRefIDs: []string{"evidence_readiness_0001"}, SourceState: contextfabric.SourceAvailable,
				Source: "ops", SourceVersion: "v1",
			},
		},
		Coverage: contextfabric.Coverage{
			Sources: []contextfabric.SourceObservation{
				{Source: "canonical_fact:status", State: contextfabric.SourceAvailable},
				{Source: "canonical_fact:readiness", State: contextfabric.SourceAvailable},
			},
			DegradedReasons: []string{},
		},
		Version: "ops-v1",
	}
}

// liveClaimMatchesCanonicalFact re-checks value-level closure independently of
// the engine: the claim must name a fact kind, subject, field, and value that
// the canonical bundle actually contains.
func liveClaimMatchesCanonicalFact(claim contractsv1.ContextFabricClaimedFact, bundle contextfabric.CanonicalFactBundle) bool {
	for _, fact := range bundle.Facts {
		if fact.Kind != claim.Kind || fact.Subject.CanonicalID != claim.Subject.CanonicalID {
			continue
		}
		value, ok := fact.Fields[claim.Field]
		if !ok {
			continue
		}
		// FactValue and ContextFabricScalarValue are structurally identical
		// pointer-field unions, so compare by value (DeepEqual follows the
		// pointers) rather than by identity.
		if reflect.DeepEqual(contractsv1.ContextFabricScalarValue(value), claim.Value) {
			return true
		}
	}
	return false
}
