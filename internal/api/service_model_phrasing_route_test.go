package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const phrasedKindLabel = "which kind of change did you mean?"

// countingPhrasingRuntime is the deployment phrasing model: it counts calls and
// phrases every offered option.
type countingPhrasingRuntime struct {
	mu    sync.Mutex
	calls int
}

func (r *countingPhrasingRuntime) PhraseStructureOffers(_ context.Context, _ storage.Principal, input contextfabric.StructureOfferPhrasingInput) (contextfabric.StructureOfferPhrasingDraft, contextfabric.ModelExecutionReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	draft := contextfabric.StructureOfferPhrasingDraft{}
	for _, option := range input.Options {
		draft.Phrasings = append(draft.Phrasings, contextfabric.StructureOfferPhrasingEntry{OptionID: option.OptionID, Phrasing: phrasedKindLabel})
	}
	return draft, clientRouteReceipt(contextfabric.ModelOperationPhraseOffers), nil
}

func (r *countingPhrasingRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
}

func (r *countingPhrasingRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
}

// byoPhrasingConfigs is the resolver's view of the organization's own model
// configuration; its runtime is the counting BYO phrasing model.
type byoPhrasingConfigs struct{ found bool }

func (c byoPhrasingConfigs) ResolveOrgModelConfig(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	if !c.found {
		return contextfabric.ResolvedOrgModelConfig{}, false, nil
	}
	return contextfabric.ResolvedOrgModelConfig{Provider: "openai_compatible", Model: "org-model", Generation: 1}, true, nil
}

// phrasingResolver composes the production per-organization resolver over the
// counting deployment phrasing model and the counting BYO phrasing model.
func phrasingResolver(deployment, byo *countingPhrasingRuntime, hasBYO, absentDeployment bool, logger *slog.Logger) *modelruntimeresolver.Resolver {
	var deploymentRuntime contextfabric.ModelRuntime
	if !absentDeployment {
		deploymentRuntime = deployment
	}
	resolver := modelruntimeresolver.New(deploymentRuntime, byoPhrasingConfigs{found: hasBYO}, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		return byo, nil
	})
	resolver.Logger = logger
	return resolver
}

func (r *countingPhrasingRuntime) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type phrasingRouteRig struct {
	app      *App
	token    string
	model    *clientRouteModel
	phrasing *countingPhrasingRuntime
	byo      *countingPhrasingRuntime
	logs     *bytes.Buffer
}

// newPhrasingRouteRig composes the real API over the real engine with the
// caller's own interpretation accepted, a graph that commits nothing and offers
// one kind option, and the offer phraser wired to a counting deployment model.
func newPhrasingRouteRig(t *testing.T, grants *serviceModelGrants, hasBYO bool) *phrasingRouteRig {
	return newPhrasingRouteRigWith(t, grants, hasBYO, false)
}

func newPhrasingRouteRigWith(t *testing.T, grants *serviceModelGrants, hasBYO, absentDeployment bool) *phrasingRouteRig {
	t.Helper()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rig := &phrasingRouteRig{model: &clientRouteModel{}, phrasing: &countingPhrasingRuntime{}, byo: &countingPhrasingRuntime{}, logs: logs}
	supplied, err := genkitruntime.NewSuppliedInterpreter(genkitruntime.SuppliedInterpreterConfig{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	material := contextfabric.StructureOfferMaterial{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{
			{Label: "a pull request", Kind: contractsv1.ContextFabricSubjectPullRequest, OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
	store := memoryinvestigation.NewStore()
	next := 0
	telemetry := contextfabric.NewSlogEngineTelemetry(logger)
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: rig.model, Supplied: supplied},
		Graph: surfaceGraph{resolution: contextfabric.SubjectResolution{
			Candidates: surfacePool(contextfabric.SubjectPullRequest, contextfabric.SubjectWorkItem), Committed: []contextfabric.SubjectRef{},
			ClarificationPrompt: "Which subject did you mean: Candidate A, Candidate B?",
		}, material: material},
		Facts: surfaceFacts{t: t},
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{
			Runtime: rig.model, ClientSynthesis: synthesisprompt.ClientAssembly(),
			Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "phrasing-route", Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"},
		},
		Results:      store,
		Telemetry:    telemetry,
		OfferPhraser: contextfabric.RuntimeOfferPhraser{Runtime: phrasingResolver(rig.phrasing, rig.byo, hasBYO, absentDeployment, logger), Sink: acceptingSink{}, Logger: logger},
	}, contextfabric.EngineOptions{
		ServiceVersion: "phrasing-route",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			next++
			return fmt.Sprintf("result_phrasing_route%02d", next)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.app, rig.token = newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, logs)
	rig.app.runtime.Entitlements = grants
	rig.app.runtime.OrgModelConfigs = ownModelConfigs{found: hasBYO}
	return rig
}

// post sends a turn that runs on the caller's own model: its own interpretation
// and synthesis "client".
func (r *phrasingRouteRig) post(t *testing.T) contractsv1.ContextFabricInvestigationResponse {
	t.Helper()
	body := investigationRequestBody()
	body.Question = writeBackQuestion
	body.SynthesisMode = contractsv1.ContextFabricSynthesisModeClient
	body.SuppliedInterpretation = writeBackInterpretation(t)
	body.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(recorder, request)
	return decodeEnvelope(t, recorder)
}

func phrasingOutcomes(logs *bytes.Buffer) []string {
	var outcomes []string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry struct {
			Msg     string `json:"msg"`
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "context fabric offer phrasing outcome" {
			outcomes = append(outcomes, entry.Outcome)
		}
	}
	return outcomes
}

func kindOptionsOf(t *testing.T, envelope contractsv1.ContextFabricInvestigationResponse) []contractsv1.ContextFabricKindOption {
	t.Helper()
	if envelope.Status != contractsv1.ContextFabricInvestigationClarificationRequired || envelope.StructureNeeds == nil || len(envelope.StructureNeeds.KindOptions) != 1 {
		t.Fatalf("status = %q structure_needs = %+v, want clarification_required with one kind option", envelope.Status, envelope.StructureNeeds)
	}
	return envelope.StructureNeeds.KindOptions
}

// A clarification turn on the caller's own model for an organization without
// the feature or its own model: the offer reaches the caller with its
// structural label and no phrasing, the deployment phrasing model is never
// called, and the decision and the phrasing outcome are both in the trace.
func TestClarificationOnTheClientPathWithoutTheFeatureIsNeverPhrasedOnTheDeploymentModel(t *testing.T) {
	rig := newPhrasingRouteRig(t, &serviceModelGrants{}, false)
	options := kindOptionsOf(t, rig.post(t))
	if got := rig.phrasing.count(); got != 0 || rig.byo.count() != 0 {
		t.Fatalf("phrasing calls = deployment %d byo %d, want 0 and 0", got, rig.byo.count())
	}
	if options[0].Label != "a pull request" || options[0].Phrasing != "" {
		t.Fatalf("kind option = %+v, want the structural label and no phrasing", options[0])
	}
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
	}
	if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "client_model" {
		t.Fatalf("decision log bases = %v, want [client_model]", got)
	}
	if got := phrasingOutcomes(rig.logs); len(got) != 1 || got[0] != string(contextfabric.OfferPhrasingWithheld) {
		t.Fatalf("phrasing outcomes = %v, want [%s]", got, contextfabric.OfferPhrasingWithheld)
	}
}

// The mirror: the same turn for an organization with the feature is phrased
// once on the deployment model.
func TestClarificationOnTheClientPathWithTheFeatureIsPhrasedOnTheDeploymentModel(t *testing.T) {
	rig := newPhrasingRouteRig(t, &serviceModelGrants{feature: true, fallback: true}, false)
	options := kindOptionsOf(t, rig.post(t))
	if got := rig.phrasing.count(); got != 1 {
		t.Fatalf("deployment phrasing calls = %d, want 1", got)
	}
	if options[0].Phrasing != phrasedKindLabel {
		t.Fatalf("kind option phrasing = %q, want %q", options[0].Phrasing, phrasedKindLabel)
	}
	if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "platform_model_fallback" {
		t.Fatalf("decision log bases = %v, want [platform_model_fallback]", got)
	}
	if got := phrasingOutcomes(rig.logs); len(got) != 1 || got[0] != string(contextfabric.OfferPhrasingGenerated) {
		t.Fatalf("phrasing outcomes = %v, want [%s]", got, contextfabric.OfferPhrasingGenerated)
	}
}

// newWindowGatePhrasingRig composes the same API and engine for a turn that
// meets the window gate with subject offers. The interpretation is the
// organization's own model (a fixed reading here, no model call), so the
// caller is served on its own model configuration.
func newWindowGatePhrasingRig(t *testing.T, grants *serviceModelGrants, hasBYO bool) *phrasingRouteRig {
	t.Helper()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rig := &phrasingRouteRig{model: &clientRouteModel{}, phrasing: &countingPhrasingRuntime{}, byo: &countingPhrasingRuntime{}, logs: logs}
	project := contextfabric.SubjectProject
	ciRun := contractsv1.ContextFabricSubjectCIRun
	reading := surfaceReading(t, contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"named-project"}, ExpectedKind: &project}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	})
	material := contextfabric.StructureOfferMaterial{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{
			{Label: "CI run 29213415002", Kind: ciRun, CanonicalID: "ci_pipeline_run.v2:x:29213415002", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
	store := memoryinvestigation.NewStore()
	next := 0
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: surfaceInterpreter{frame: reading.Frame, drivers: true},
		Graph: surfaceGraph{resolution: contextfabric.SubjectResolution{
			Candidates: surfacePool(ciRun, ciRun), Committed: []contextfabric.SubjectRef{},
			ClarificationPrompt: "Which subject did you mean: Candidate A, Candidate B?",
		}, material: material},
		Facts: surfaceFacts{t: t}, Synthesizer: surfaceSynthesizer{t: t},
		Results:      store,
		Telemetry:    contextfabric.NewSlogEngineTelemetry(logger),
		OfferPhraser: contextfabric.RuntimeOfferPhraser{Runtime: phrasingResolver(rig.phrasing, rig.byo, hasBYO, false, logger), Sink: acceptingSink{}, Logger: logger},
	}, contextfabric.EngineOptions{
		ServiceVersion: "window-gate-phrasing-route",
		Now:            func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) },
		NewResultID: func() string {
			next++
			return fmt.Sprintf("result_window_gate_phrasing%02d", next)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.app, rig.token = newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 500, MaxTokens: 500_000, MaxBytes: 8 << 20}, logs)
	rig.app.runtime.Entitlements = grants
	rig.app.runtime.OrgModelConfigs = ownModelConfigs{found: hasBYO}
	return rig
}

// postWithoutWindow sends a service-interpreted turn with no evidence window.
func (r *phrasingRouteRig) postWithoutWindow(t *testing.T) contractsv1.ContextFabricInvestigationResponse {
	t.Helper()
	encoded, err := json.Marshal(investigationRequestBody())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(recorder, request)
	return decodeEnvelope(t, recorder)
}

func windowGateCandidateOptions(t *testing.T, envelope contractsv1.ContextFabricInvestigationResponse) []contractsv1.ContextFabricCandidateOption {
	t.Helper()
	needs := envelope.StructureNeeds
	if envelope.Status != contractsv1.ContextFabricInvestigationClarificationRequired || needs == nil || len(needs.WindowOptions) == 0 || len(needs.CandidateOptions) != 1 {
		t.Fatalf("status = %q structure_needs = %+v, want the window gate with one candidate option", envelope.Status, needs)
	}
	return needs.CandidateOptions
}

// An organization served on its own model without the feature is phrased on
// its own model, never the deployment's.
func TestWindowGateClarificationOnTheOwnModelWithoutTheFeatureIsPhrasedOnTheOwnModel(t *testing.T) {
	rig := newWindowGatePhrasingRig(t, &serviceModelGrants{}, true)
	options := windowGateCandidateOptions(t, rig.postWithoutWindow(t))
	if rig.phrasing.count() != 0 || rig.byo.count() != 1 {
		t.Fatalf("phrasing calls = deployment %d byo %d, want 0 and 1", rig.phrasing.count(), rig.byo.count())
	}
	if options[0].Label != "CI run 29213415002" || options[0].Phrasing != phrasedKindLabel {
		t.Fatalf("candidate option = %+v, want the structural label and the phrasing", options[0])
	}
	if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "own_model" {
		t.Fatalf("decision log bases = %v, want [own_model]", got)
	}
	if got := phrasingOutcomes(rig.logs); len(got) != 1 || got[0] != string(contextfabric.OfferPhrasingGenerated) {
		t.Fatalf("phrasing outcomes = %v, want [%s]", got, contextfabric.OfferPhrasingGenerated)
	}
}

func TestWindowGateClarificationWithTheFeatureIsPhrasedOnTheDeploymentModel(t *testing.T) {
	rig := newWindowGatePhrasingRig(t, &serviceModelGrants{feature: true, fallback: true}, false)
	options := windowGateCandidateOptions(t, rig.postWithoutWindow(t))
	if rig.phrasing.count() != 1 || rig.byo.count() != 0 {
		t.Fatalf("phrasing calls = deployment %d byo %d, want 1 and 0", rig.phrasing.count(), rig.byo.count())
	}
	if options[0].Phrasing != phrasedKindLabel {
		t.Fatalf("candidate option phrasing = %q, want %q", options[0].Phrasing, phrasedKindLabel)
	}
}

// An organization with the feature and its own model is phrased on its own
// model: the deployment model is not used while the organization has one.
func TestWindowGateClarificationWithTheFeatureAndAnOwnModelIsPhrasedOnTheOwnModel(t *testing.T) {
	rig := newWindowGatePhrasingRig(t, &serviceModelGrants{feature: true, fallback: true}, true)
	options := windowGateCandidateOptions(t, rig.postWithoutWindow(t))
	if rig.phrasing.count() != 0 || rig.byo.count() != 1 {
		t.Fatalf("phrasing calls = deployment %d byo %d, want 0 and 1", rig.phrasing.count(), rig.byo.count())
	}
	if options[0].Phrasing != phrasedKindLabel {
		t.Fatalf("candidate option phrasing = %q, want %q", options[0].Phrasing, phrasedKindLabel)
	}
}

// A caller-model turn for an organization with its own model, without the
// feature, is phrased on the organization's own model.
func TestClarificationOnTheClientPathWithAnOwnModelIsPhrasedOnTheOwnModel(t *testing.T) {
	rig := newPhrasingRouteRig(t, &serviceModelGrants{}, true)
	options := kindOptionsOf(t, rig.post(t))
	if rig.phrasing.count() != 0 || rig.byo.count() != 1 {
		t.Fatalf("phrasing calls = deployment %d byo %d, want 0 and 1", rig.phrasing.count(), rig.byo.count())
	}
	if options[0].Phrasing != phrasedKindLabel {
		t.Fatalf("kind option phrasing = %q, want %q", options[0].Phrasing, phrasedKindLabel)
	}
}

// A deployment with no model runtime and an organization with no model of its
// own: the offer is served with its structural labels, the outcome is the
// withheld class (not a failed call), and nothing is refused.
func TestClarificationWithNoModelConfiguredIsServedWithStructuralLabelsAsWithheld(t *testing.T) {
	rig := newPhrasingRouteRigWith(t, &serviceModelGrants{feature: true, fallback: true}, false, true)
	options := kindOptionsOf(t, rig.post(t))
	if rig.phrasing.count() != 0 || rig.byo.count() != 0 {
		t.Fatalf("phrasing calls = deployment %d byo %d, want 0 and 0", rig.phrasing.count(), rig.byo.count())
	}
	if options[0].Label != "a pull request" || options[0].Phrasing != "" {
		t.Fatalf("kind option = %+v, want the structural label and no phrasing", options[0])
	}
	if got := phrasingOutcomes(rig.logs); len(got) != 1 || got[0] != string(contextfabric.OfferPhrasingWithheld) {
		t.Fatalf("phrasing outcomes = %v, want [%s]", got, contextfabric.OfferPhrasingWithheld)
	}
	if !strings.Contains(rig.logs.String(), "no_model_configured") {
		t.Fatal("logs carry no no_model_configured basis line")
	}
}
