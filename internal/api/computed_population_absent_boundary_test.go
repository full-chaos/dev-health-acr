package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
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

// A count over a project's deployments when the graph resolves no member set.
// Reads what an MCP client receives from the real engine, hosted route and MCP
// server: the count row must say its population is absent, not that a fact
// was pruned.

func TestACountWithNoMemberSetReachesTheClientWithItsOwnCause(t *testing.T) {
	fixture := newFreshTupleProducerFixtureWithBudget(t, "", limits.ResourceBudget{MaxItems: 30, MaxTokens: 16000, MaxBytes: 1 << 20})
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(fixture.client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := memoryinvestigation.NewStore()
	dependencies := fixture.dependencies
	dependencies.Interpreter = contextfabric.RuntimeQuestionInterpreter{Runtime: cutDeploymentsModel{freshTupleModel: countDeploymentsFrameModel(*fixture.model)}, Requirements: registry}
	dependencies.Graph = cutDeploymentsGraph{}
	dependencies.Results = store
	options := fixture.engineOptions
	options.MaxItems = 30
	options.NewResultID = func() string { return "result_population_absent_01" }
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
		Question:       "How many deployments does project payments have?",
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
	})
	var node struct {
		Structured struct {
			Completeness struct {
				Outcomes []struct {
					Requirement string `json:"requirement"`
					Outcome     string `json:"outcome"`
					Cause       string `json:"cause_coverage"`
				} `json:"outcomes"`
			} `json:"completeness"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(answer.structured, &node); err != nil {
		t.Fatal(err)
	}
	var seen []string
	found := false
	for _, row := range node.Structured.Completeness.Outcomes {
		seen = append(seen, row.Requirement+"="+row.Outcome+"/"+row.Cause)
		if row.Requirement == "count/member/deployment" && row.Outcome == "unavailable" {
			found = true
			if row.Cause != "computed_population_absent" {
				t.Fatalf("count row cause = %q, want computed_population_absent (a pruned fact is a different claim)", row.Cause)
			}
		}
	}
	if !found {
		t.Fatalf("no unavailable count/member/deployment row served: %v", seen)
	}
}

func countDeploymentsFrameModel(base freshTupleModel) freshTupleModel {
	base = deploymentsFrameModel(base)
	base.frame.Goals = []contextfabric.InvestigationGoal{contextfabric.GoalAssessState, contextfabric.GoalCountOrAggregate}
	return base
}
