package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
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

// CHAOS-12348: a single-subject answer resting on a cut graph read. The two
// coverage details below have the shape the graph reader writes
// (falkorgraph chaos12348_noncohort_read_disclosure_test.go asserts the reader's
// own rows); this test reads what an MCP client receives from the real engine,
// hosted route and MCP server for them. The same fixture with a whole read is
// the control for the rows. It is no control for coverage_partial: this
// fixture's fact read is partial on its own, so
// contextfabric.TestACutNonCohortReadServesPartialCoverageAndKeepsItsDraftStatus
// holds that the rows alone set it. Status does not move: no served-status floor
// reads these codes, so a complete draft stays complete beside coverage_partial.

type cutGraphReadGraph struct {
	cutDeploymentsGraph
	cut bool
}

func (g cutGraphReadGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	two := 2
	details := []contextfabric.CoverageDetail{}
	if g.cut {
		details = append(details,
			contextfabric.CoverageDetail{DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailCode("graph_hop_walk_truncated"), Degrading: true, Raw: "hop_walk_truncated"},
			contextfabric.CoverageDetail{DetailID: "cov-graph-02", Source: "context-fabric:graph", Code: contractsv1.ContextFabricCoverageDetailCode("graph_team_narrowing_undecided_paths"), Degrading: true, Count: &two, Raw: "team_narrowing_undecided_paths:2"})
	}
	reasons := make([]string, 0, len(details))
	for i := range details {
		details[i].Label = contractsv1.ComposeCoverageDetailLabel(details[i])
		reasons = append(reasons, details[i].Raw)
	}
	return contextfabric.GraphContext{
		Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{
			Partial: g.cut, DegradedReasons: reasons, Details: details,
			Sources: []contextfabric.SourceObservation{{Source: "context-fabric:graph", State: contextfabric.SourceAvailable, ObservedAt: ptrTimeCut(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))}},
		},
	}, nil
}

type cutGraphReadModel struct{ freshTupleModel }

func (m cutGraphReadModel) InterpretQuestion(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	interpreted, receipt, err := m.freshTupleModel.InterpretQuestion(ctx, p, r)
	receipt.QuestionFamily = contextfabric.QuestionFamilySubjectInvestigation
	receipt.ScopeAnchorKind, receipt.ScopeAnchorTerm, receipt.RequestedSubjectKind = "", "", contextfabric.SubjectProject
	interpreted.Shape = contextfabric.ShapeSingleSubject
	interpreted.SubjectTerms = []string{"payments"}
	interpreted.FactRequirements = []contextfabric.FactRequirement{{Kind: contextfabric.FactWork}}
	return interpreted, receipt, err
}

func namedProjectFrameModel(base freshTupleModel) freshTupleModel {
	project := contextfabric.SubjectProject
	base.frame = contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{"payments"}, ExpectedKind: &project}},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	return base
}

type cutGraphReadAnswer struct {
	Status          string `json:"status"`
	CoveragePartial bool   `json:"coverage_partial"`
	Details         []struct {
		Code  string `json:"code"`
		Count *int   `json:"count"`
		Label string `json:"label"`
	} `json:"coverage_details"`
}

func serveCutGraphRead(t *testing.T, cut bool) cutGraphReadAnswer {
	t.Helper()
	fixture := newFreshTupleProducerFixtureWithBudget(t, "", limits.ResourceBudget{MaxItems: 30, MaxTokens: 16000, MaxBytes: 1 << 20})
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(fixture.client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := memoryinvestigation.NewStore()
	dependencies := fixture.dependencies
	dependencies.Interpreter = contextfabric.RuntimeQuestionInterpreter{Runtime: cutGraphReadModel{freshTupleModel: namedProjectFrameModel(*fixture.model)}, Requirements: registry}
	dependencies.Graph = cutGraphReadGraph{cut: cut}
	dependencies.Results = store
	options := fixture.engineOptions
	options.MaxItems = 30
	options.NewResultID = func() string { return "result_cut_read_01" }
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
		Question:       "What is going on with project payments?",
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
	})
	var node struct {
		Structured cutGraphReadAnswer `json:"structured"`
	}
	if err := json.Unmarshal(answer.structured, &node); err != nil {
		t.Fatal(err)
	}
	return node.Structured
}

func TestACutNonCohortReadReachesTheClientAsPartialCoverage(t *testing.T) {
	whole := serveCutGraphRead(t, false)
	if whole.Status != "complete" {
		t.Fatalf("control: a whole read served status %q, want the draft's complete", whole.Status)
	}
	for _, d := range whole.Details {
		if d.Code == "graph_hop_walk_truncated" || d.Code == "graph_team_narrowing_undecided_paths" {
			t.Fatalf("control: a whole read served %+v", d)
		}
	}
	answer := serveCutGraphRead(t, true)
	if !answer.CoveragePartial {
		t.Fatalf("coverage_partial = false over a cut read, details %+v", answer.Details)
	}
	var codes []string
	for _, d := range answer.Details {
		codes = append(codes, d.Code)
		switch d.Code {
		case "graph_hop_walk_truncated":
			if d.Count != nil || !strings.Contains(d.Label, "cut at its limit") {
				t.Fatalf("detail = %+v, want no count and the cut label", d)
			}
		case "graph_team_narrowing_undecided_paths":
			if d.Count == nil || *d.Count != 2 || !strings.Contains(d.Label, "team ownership could not be decided") {
				t.Fatalf("detail = %+v, want count 2 and the undecided label", d)
			}
		}
	}
	for _, want := range []string{"graph_hop_walk_truncated", "graph_team_narrowing_undecided_paths"} {
		if !slices.Contains(codes, want) {
			t.Fatalf("served coverage codes = %v, want %s", codes, want)
		}
	}
	if answer.Status != "complete" {
		t.Fatalf("status = %q: no floor reads these codes, so the draft's complete stands; a floor is a deliberate edit here", answer.Status)
	}
}
