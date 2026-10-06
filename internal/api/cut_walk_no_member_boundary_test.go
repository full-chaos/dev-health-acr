package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A project whose deployments walk was cut before it reached a member. The
// coverage detail below has the shape the graph reader writes at that site
// (falkorgraph TestAProjectWalkCutToNoMemberHasItsOwnCode asserts the reader's
// own row); this test reads what an MCP client receives from the real engine,
// hosted route and MCP server for it.

var cutProject = contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:6b0f2d1e-3c44-4a57-9d2e-1f7a8c5b9e01", Label: "payments"}

type cutDeploymentsGraph struct{}

func (cutDeploymentsGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "walk-cut", Epoch: 1}, nil
}

func (cutDeploymentsGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	candidate := contextfabric.SubjectCandidate{
		ReceiptID: "receipt_walk_cut_project", Subject: cutProject, State: contextfabric.ResolutionCommitted,
		MatchedTerms: []string{"payments"}, MatchReasons: []string{"exact"}, Confidence: 1,
		EvidenceRefIDs: []string{"evidence_identity_1234"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchAlias},
	}
	bases := contextfabric.CommitBasisSet{}
	bases.Record(cutProject, contextfabric.CommitBasisAuthoritativeIdentity)
	digests := contextfabric.CommitDecisionDigestSet{}
	digests.Record(cutProject, contextfabric.CommitDecisionDigest{CommitGate: "identity_fast_path", IdentityProven: true})
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{cutProject}}, contextfabric.StructureOfferMaterial{}, bases, digests, nil
}

func (cutDeploymentsGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	raw := "walk_cut_before_member:deployment"
	detail := contextfabric.CoverageDetail{
		DetailID: "cov-graph-01", Source: "context-fabric:graph",
		Code: contractsv1.ContextFabricCoverageDetailCode("graph_walk_cut_before_member"), Degrading: true,
		Kind: contextfabric.SubjectDeployment, Raw: raw,
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	return contextfabric.GraphContext{
		Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{
			Partial: true, DegradedReasons: []string{raw}, Details: []contextfabric.CoverageDetail{detail},
			Sources: []contextfabric.SourceObservation{{Source: "context-fabric:graph", State: contextfabric.SourceAvailable, ObservedAt: ptrTimeCut(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))}},
		},
	}, nil
}

func (cutDeploymentsGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range out {
		out[i] = contextfabric.StoredSubjectAdmitted
	}
	return out, nil
}

func ptrTimeCut(t time.Time) *time.Time { return &t }

type cutDeploymentsModel struct{ freshTupleModel }

func (m cutDeploymentsModel) InterpretQuestion(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	interpreted, receipt, err := m.freshTupleModel.InterpretQuestion(ctx, p, r)
	receipt.ScopeAnchorKind = contextfabric.SubjectProject
	receipt.ScopeAnchorTerm = "payments"
	receipt.RequestedSubjectKind = contextfabric.SubjectDeployment
	interpreted.SubjectTerms = []string{"payments"}
	return interpreted, receipt, err
}

func TestAProjectWalkCutToNoMemberReachesTheClientWithItsOwnCode(t *testing.T) {
	fixture := newFreshTupleProducerFixtureWithBudget(t, "", limits.ResourceBudget{MaxItems: 30, MaxTokens: 16000, MaxBytes: 1 << 20})
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(fixture.client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := memoryinvestigation.NewStore()
	dependencies := fixture.dependencies
	dependencies.Interpreter = contextfabric.RuntimeQuestionInterpreter{Runtime: cutDeploymentsModel{freshTupleModel: deploymentsFrameModel(*fixture.model)}, Requirements: registry}
	dependencies.Graph = cutDeploymentsGraph{}
	dependencies.Results = store
	options := fixture.engineOptions
	options.MaxItems = 30
	options.NewResultID = func() string { return "result_walk_cut_01" }
	options.Now = func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
	engine, err := contextfabric.NewEngine(dependencies, options)
	if err != nil {
		t.Fatal(err)
	}
	app, token := newParityHostedAppWithBudget(t, investigatorFunc(func(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		result, err := engine.Investigate(ctx, p, r)
		if err != nil {
			t.Logf("engine error: %v", err)
		}
		return result, err
	}), store, limits.ResourceBudget{MaxItems: 30, MaxTokens: 500_000, MaxBytes: 8 << 20})
	app.config.RequestTimeout = 30 * time.Second
	app.config.MaxItems = 30
	server := httptest.NewTLSServer(app.InstrumentedHandler(app.Handler()))
	t.Cleanup(server.Close)
	configureSidecarEnvironment(t, server, token)
	boot, err := acrmcp.NewBootstrap(context.Background(), "1.2.5")
	if err != nil {
		t.Fatalf("real MCP bootstrap: %v", err)
	}
	answer := callRealMCPTool(t, boot, "investigate_question", contractsv1.MCPInvestigateQuestionRequest{
		Question:       "Which deployments belong to project payments?",
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
	})
	var node struct {
		Structured struct {
			Details []struct {
				Code     string  `json:"code"`
				Kind     string  `json:"kind"`
				Declared *int    `json:"declared"`
				Served   *int    `json:"served"`
				Label    string  `json:"label"`
				Raw      *string `json:"raw"`
			} `json:"coverage_details"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(answer.structured, &node); err != nil {
		t.Fatal(err)
	}
	var codes []string
	found := false
	for _, d := range node.Structured.Details {
		codes = append(codes, d.Code)
		if d.Code == "graph_walk_cut_before_member" {
			found = true
			if d.Kind != "deployment" || d.Declared != nil || d.Served != nil {
				t.Fatalf("detail = %+v, want kind deployment and no census counts", d)
			}
			if !strings.Contains(d.Label, "cut before it reached any deployment") {
				t.Fatalf("label = %q", d.Label)
			}
		}
		if d.Code == "kind_census_truncated" {
			t.Fatalf("served kind_census_truncated for a walk that listed nothing: %+v", d)
		}
	}
	if !found {
		t.Fatalf("served coverage codes = %v (%s), want graph_walk_cut_before_member", codes, fmt.Sprint(len(answer.markdown)))
	}
}

func deploymentsFrameModel(base freshTupleModel) freshTupleModel {
	base.frame = contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"payments"}, MemberKind: contextfabric.SubjectDeployment},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	return base
}
