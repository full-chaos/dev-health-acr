package devhealthfacts_test

// CHAOS-12330 harness: the real Engine.Investigate over the real
// FactCapabilityRegistry and the real devhealthfacts providers, for a
// project-only caller. Only the graph and the model are controlled, and the
// graph decides each node with the production predicate
// (graphrank.NodeCandidate). The client serves no row: a withheld path must
// not read one anyway.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type projectOnlyEngineGraph struct {
	t            *testing.T
	subject      contextfabric.SubjectRef
	subjectAttrs map[string]interface{}
	resolved     int
	discovered   int
	narrowings   int
}

func (*projectOnlyEngineGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "chaos12330", Epoch: 1}, nil
}

func (g *projectOnlyEngineGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range outcomes {
		outcomes[i] = contextfabric.StoredSubjectAdmitted
	}
	return outcomes, nil
}

func (g *projectOnlyEngineGraph) ResolveSubjects(_ context.Context, p storage.Principal, r contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	g.resolved++
	candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, "Payments", graphrank.CandidateNode{UUID: "subject-node", Name: "Payments", Attributes: g.subjectAttrs}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
	if !ok {
		return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
	}
	candidate.State = contextfabric.ResolutionCommitted
	candidate.ReceiptID = "receipt_chaos12330"
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{candidate.Subject}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

func (g *projectOnlyEngineGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	g.discovered++
	return contextfabric.GraphContext{
		Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

func (g *projectOnlyEngineGraph) ResolveProjectNarrowing(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, projectIDs []string) (*contractsv1.ContextFabricProjectNarrowing, error) {
	g.narrowings++
	return &contractsv1.ContextFabricProjectNarrowing{Identities: projectIDs, Complete: true}, nil
}

type projectOnlyEngineModel struct {
	kinds []contextfabric.FactKind
	// memberKind, when set, makes the question ask for the members of that
	// kind under the named subject, so the answer plan names the kind.
	memberKind  contextfabric.SubjectKind
	facts       contextfabric.CanonicalFactBundle
	input       bool
	interpreted int
	synthesized int
}

func (m *projectOnlyEngineModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	m.interpreted++
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFamily = contextfabric.QuestionFamilySubjectInvestigation
	receipt.QuestionFrame = &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"Payments"}}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}
	requirements := make([]contextfabric.FactRequirement, 0, len(m.kinds))
	for _, kind := range m.kinds {
		requirements = append(requirements, contextfabric.FactRequirement{Kind: kind})
	}
	shape := contextfabric.ShapeSingleSubject
	if m.memberKind != "" {
		shape = contextfabric.ShapeDiscoveredCohort
		receipt.QuestionFamily = contextfabric.QuestionFamilyScopedCohortStatus
		receipt.ScopeAnchorKind = contextfabric.SubjectProject
		receipt.ScopeAnchorTerm = "Payments"
		receipt.RequestedSubjectKind = m.memberKind
		receipt.QuestionFrame.SubjectExpression = contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Payments"}, MemberKind: m.memberKind}}
	}
	return contextfabric.InterpretedQuestion{Shape: shape, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Payments"}, FactRequirements: requirements}, receipt, nil
}

func (m *projectOnlyEngineModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	m.synthesized++
	m.facts = input.Facts
	m.input = true
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: "No facts were available.", CurrentState: "No facts were available.", DeterministicAnswer: "No facts were available.", StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

type projectOnlyEngineRun struct {
	result     contextfabric.InvestigationResult
	err        error
	graph      *projectOnlyEngineGraph
	model      *projectOnlyEngineModel
	client     *fakeClient
	operations *fakeOperationCaller
	results    contextfabric.InvestigationResultStore
}

const projectOnlyEngineOrg = "org-1"

// projectOnlyProjectNode is a project node of the grant, the shape the graph
// projects.
func projectOnlyProjectNode(subject contextfabric.SubjectRef) map[string]interface{} {
	return map[string]interface{}{"subject_kind": "project", "canonical_id": subject.CanonicalID, "label": "Payments", "authorization_repositories": []string{"acme/allowed"}, "authorization_projects": "*", "authorization_teams": "*"}
}

func runProjectOnlyEngine(t *testing.T, principal storage.Principal, subject contextfabric.SubjectRef, attrs map[string]interface{}, kinds []contextfabric.FactKind, scope contextfabric.RequestedScope) projectOnlyEngineRun {
	t.Helper()
	return runProjectOnlyEngineWith(t, principal, subject, attrs, &projectOnlyEngineModel{kinds: kinds}, scope)
}

func runProjectOnlyEngineWith(t *testing.T, principal storage.Principal, subject contextfabric.SubjectRef, attrs map[string]interface{}, model *projectOnlyEngineModel, scope contextfabric.RequestedScope) projectOnlyEngineRun {
	t.Helper()
	client := &fakeClient{}
	caller := &fakeOperationCaller{outcome: devhealthfacts.OperationOutcome{Served: true, Complete: true, Data: sourceHealthData(`{"provider":"github","scope":"git","lastSyncAt":"2026-10-08T01:00:00Z","lastFailure":null}`)}}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProvidersWithOperations(client, sourceHealthHolder(caller)), contextfabric.FactRegistryOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	graph := &projectOnlyEngineGraph{t: t, subject: subject, subjectAttrs: attrs}
	at := time.Now().UTC()
	results := memoryinvestigation.NewStore()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:        graph,
		Facts:        registry,
		Requirements: registry,
		Telemetry:    contextfabric.NewSlogEngineTelemetry(slog.New(slog.DiscardHandler)),
		Synthesizer:  contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "chaos12330", Backend: "graph"}},
		Results:      results,
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "chaos12330", Now: func() time.Time { return at }, NewResultID: func() string { return "result_chaos12330" }})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_chaos12330", Question: "How is Payments doing?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: scope, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "chaos12330", Version: "1.0.0", Surface: "workbench"}}
	result, investigateErr := engine.Investigate(context.Background(), principal, request)
	return projectOnlyEngineRun{result: result, err: investigateErr, graph: graph, model: model, client: client, operations: caller, results: results}
}
