package api

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type freshTupleProducerFixture struct {
	engine        *contextfabric.Engine
	model         *freshTupleModel
	dependencies  contextfabric.EngineDependencies
	engineOptions contextfabric.EngineOptions
	client        *freshTupleQueryClient
	graph         *freshTupleGraph
	gate          *contextfabric.WorkItemMembershipGate
	store         *memoryinvestigation.Store
	app           *App
	token         string
	principal     storage.Principal
	engineErr     error
}

// newFreshTupleProducerFixture exposes the real construction to the separate
// lifetime tests. Only backend query rows and model drafts are controlled.
func newFreshTupleProducerFixture(t *testing.T, failure string) *freshTupleProducerFixture {
	t.Helper()
	return newFreshTupleProducerFixtureWithBudget(t, failure, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16000, MaxBytes: 1 << 20})
}

func newFreshTupleProducerFixtureWithBudget(t *testing.T, failure string, budget limits.ResourceBudget) *freshTupleProducerFixture {
	t.Helper()
	projectID, _, _ := identity.Derive(identity.KindProject, []string{"linear", "P1"}, nil)
	memberID, _, _ := identity.Derive(identity.KindWorkItem, []string{"repo-1", "work-1"}, nil)
	client := &freshTupleQueryClient{memberID: memberID, fail: failure}
	gate := newResponseOwnerAPITestGate(t)
	reader, err := devhealthfacts.NewWorkItemMembershipReader(client, devhealthfacts.WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model := &freshTupleModel{frame: contextfabric.QuestionFrame{Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalCountOrAggregate}, SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Project Alpha"}, MemberKind: contextfabric.SubjectWorkItem}}, Temporal: contextfabric.TemporalIntentCurrent}}
	graph := &freshTupleGraph{t: t, projectID: projectID}
	store := memoryinvestigation.NewStore()
	dependencies := contextfabric.EngineDependencies{Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry}, Graph: graph, Facts: registry, Requirements: registry, WorkItemMembership: reader, CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
		return true, contextfabric.CandidateVerificationValid
	}, Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "tuple-test", Backend: "graph", ProjectionVersion: "projection-v1", QueryVersion: "query-v1"}}, Results: store}
	engineOptions := contextfabric.EngineOptions{ServiceVersion: "tuple-test", MaxItems: int(budget.MaxItems), MaxSerializedBytes: 262144, SynthesisDeadlineReserve: 5 * time.Second / 3, Now: func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }, NewResultID: func() string { return "result_tuple_producer_001" }}
	engine, err := contextfabric.NewEngine(dependencies, engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &freshTupleProducerFixture{engine: engine, model: model, dependencies: dependencies, engineOptions: engineOptions, client: client, graph: graph, gate: gate, store: store}
	app, token := newParityHostedAppWithBudget(t, investigatorFunc(func(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		result, err := fixture.engine.Investigate(ctx, p, r)
		fixture.engineErr = err
		fixture.principal = p
		return result, err
	}), store, budget)
	// Match the parity fixture's real capabilities and five-second deadline.
	// S1 requires at least one whole second remaining at admission.
	app.config.RequestTimeout = 5 * time.Second
	app.config.MaxItems = int(budget.MaxItems)
	fixture.app, fixture.token = app, token
	return fixture
}
