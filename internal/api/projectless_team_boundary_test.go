package api

import (
	"context"
	"encoding/json"
	"errors"
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

// A team that owns no project, asked which projects it owns. The graph commits
// the team and discovers no project members; the interpretation names no fact
// kind. What an MCP client receives from the real engine, hosted route and MCP
// server must be a served answer, never an internal error.

var projectlessTeam = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:6b0f2d1e-3c44-4a57-9d2e-1f7a8c5b9e02", Label: "platform"}

type projectlessTeamGraph struct {
	cutDeploymentsGraph
	denied bool
}

func (projectlessTeamGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	candidate := contextfabric.SubjectCandidate{
		ReceiptID: "receipt_projectless_team", Subject: projectlessTeam, State: contextfabric.ResolutionCommitted,
		MatchedTerms: []string{"platform"}, MatchReasons: []string{"exact"}, Confidence: 1,
		EvidenceRefIDs: []string{"evidence_identity_1234"}, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchAlias},
	}
	bases := contextfabric.CommitBasisSet{}
	bases.Record(projectlessTeam, contextfabric.CommitBasisAuthoritativeIdentity)
	digests := contextfabric.CommitDecisionDigestSet{}
	digests.Record(projectlessTeam, contextfabric.CommitDecisionDigest{CommitGate: "identity_fast_path", IdentityProven: true})
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{projectlessTeam}}, contextfabric.StructureOfferMaterial{}, bases, digests, nil
}

func (g projectlessTeamGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	coverage := contextfabric.Coverage{Sources: []contextfabric.SourceObservation{{Source: "context-fabric:graph", State: contextfabric.SourceAvailable, ObservedAt: ptrTimeCut(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))}}}
	if g.denied {
		count := 2
		raw := "cohort_denied_by_authorization:2"
		detail := contextfabric.CoverageDetail{
			DetailID: "cov-graph-01", Source: "context-fabric:graph",
			Code: contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization, Degrading: true, Count: &count, Raw: raw,
		}
		detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
		coverage.Partial, coverage.DegradedReasons, coverage.Details = true, []string{raw}, []contextfabric.CoverageDetail{detail}
	}
	return contextfabric.GraphContext{
		Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: coverage,
	}, nil
}

type projectlessTeamModel struct {
	freshTupleModel
	anchorKind   contextfabric.SubjectKind
	requirements []contextfabric.FactRequirement
}

func (m projectlessTeamModel) InterpretQuestion(ctx context.Context, p storage.Principal, r contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	interpreted, receipt, err := m.freshTupleModel.InterpretQuestion(ctx, p, r)
	receipt.ScopeAnchorKind = m.anchorKind
	receipt.ScopeAnchorTerm = "platform"
	receipt.RequestedSubjectKind = contextfabric.SubjectProject
	interpreted.SubjectTerms = []string{"platform"}
	interpreted.FactRequirements = append([]contextfabric.FactRequirement{}, m.requirements...)
	return interpreted, receipt, err
}

func TestAProjectlessTeamIsServedNotAnInternalError(t *testing.T) {
	node := askProjectlessTeam(t, false)
	// partial: the team resolved and the search found no project, which does
	// not prove none exist; complete would read as a proven empty answer.
	if node.Structured.Status != "partial" {
		t.Fatalf("status = %q, want partial", node.Structured.Status)
	}
	found := false
	for _, d := range node.Structured.Details {
		if d.Code == "graph_no_member_found" {
			found = true
			if d.Kind != "project" || !d.Degrading || !strings.Contains(d.Label, "No project was found under the named subject") {
				t.Fatalf("detail = %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("no graph_no_member_found row served: %+v", node.Structured.Details)
	}
}

// An empty cohort the graph says was denied by authorization is not a search
// that found nothing: the served rows must not claim it was.
func TestAProjectlessTeamWhoseCohortWasDeniedIsNotServedAsNoneFound(t *testing.T) {
	node := askProjectlessTeam(t, true)
	denied := false
	for _, d := range node.Structured.Details {
		if d.Code == "graph_no_member_found" {
			t.Fatalf("served graph_no_member_found beside an authorization denial: %+v", node.Structured.Details)
		}
		if d.Code == "graph_cohort_denied_by_authorization" {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("denial row lost: %+v", node.Structured.Details)
	}
	if node.Structured.Status == "complete" {
		t.Fatalf("status = complete over a denied cohort")
	}
}

// The same team asked with a plan that carries a fact requirement: the cohort
// census is still empty, so the none-found row and the partial floor must be
// served exactly as on the empty-plan path.
func TestAProjectlessTeamWithAPlannedFactReadIsServedAsNoneFound(t *testing.T) {
	node := askProjectlessTeam(t, false, contextfabric.FactRequirement{Kind: contextfabric.FactMembership})
	if node.Structured.Status != "partial" {
		t.Fatalf("status = %q, want partial", node.Structured.Status)
	}
	rows := 0
	for _, d := range node.Structured.Details {
		if d.Code == "graph_no_member_found" {
			rows++
			if d.Kind != "project" || !d.Degrading || !strings.Contains(d.Label, "No project was found under the named subject") {
				t.Fatalf("detail = %+v", d)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("graph_no_member_found rows = %d, want 1: %+v", rows, node.Structured.Details)
	}
}

func TestAProjectlessTeamWithAPlannedFactReadWhoseCohortWasDeniedIsNotServedAsNoneFound(t *testing.T) {
	node := askProjectlessTeam(t, true, contextfabric.FactRequirement{Kind: contextfabric.FactMembership})
	denied := false
	for _, d := range node.Structured.Details {
		if d.Code == "graph_no_member_found" {
			t.Fatalf("served graph_no_member_found beside an authorization denial: %+v", node.Structured.Details)
		}
		if d.Code == "graph_cohort_denied_by_authorization" {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("denial row lost: %+v", node.Structured.Details)
	}
}

type projectlessTeamNode struct {
	Structured struct {
		Status        string `json:"status"`
		Clarification *struct {
			Prompt     string `json:"prompt"`
			Candidates []struct {
				Subject struct {
					Kind  string `json:"kind"`
					Label string `json:"label"`
				} `json:"subject"`
				Provider string `json:"provider"`
			} `json:"candidates"`
		} `json:"clarification"`
		Cohort *struct {
			Kind  string `json:"kind"`
			Total int    `json:"total"`
		} `json:"cohort"`
		Details []struct {
			Code      string `json:"code"`
			Kind      string `json:"kind"`
			Degrading bool   `json:"degrading"`
			Label     string `json:"label"`
		} `json:"coverage_details"`
	} `json:"structured"`
	byID struct {
		Status  string
		Details []string
	}
}

func askProjectlessTeam(t *testing.T, denied bool, requirements ...contextfabric.FactRequirement) projectlessTeamNode {
	t.Helper()
	return askTeamOwnership(t, projectlessTeamGraph{denied: denied}, requirements...)
}

func askTeamOwnership(t *testing.T, graph contextfabric.GraphReader, requirements ...contextfabric.FactRequirement) projectlessTeamNode {
	t.Helper()
	return askOwnershipAnchoredOn(t, graph, contextfabric.SubjectTeam, requirements...)
}

func askOwnershipAnchoredOn(t *testing.T, graph contextfabric.GraphReader, anchorKind contextfabric.SubjectKind, requirements ...contextfabric.FactRequirement) projectlessTeamNode {
	t.Helper()
	fixture := newFreshTupleProducerFixtureWithBudget(t, "", limits.ResourceBudget{MaxItems: 30, MaxTokens: 16000, MaxBytes: 1 << 20})
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(fixture.client), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := memoryinvestigation.NewStore()
	dependencies := fixture.dependencies
	model := *fixture.model
	model.frame = contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"platform"}, MemberKind: contextfabric.SubjectProject},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}
	dependencies.Interpreter = contextfabric.RuntimeQuestionInterpreter{Runtime: projectlessTeamModel{freshTupleModel: model, anchorKind: anchorKind, requirements: requirements}, Requirements: registry}
	dependencies.Graph = graph
	dependencies.Results = store
	options := fixture.engineOptions
	options.MaxItems = 30
	options.NewResultID = func() string { return "result_projectless_team_01" }
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
		Question:       "Which projects does team platform own?",
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing90D},
	})
	var node projectlessTeamNode
	if err := json.Unmarshal(answer.structured, &node); err != nil {
		t.Fatal(err)
	}
	t.Logf("served status=%q details=%+v", node.Structured.Status, node.Structured.Details)
	stored := callRealMCPTool(t, boot, "investigation_result", contractsv1.MCPInvestigationResultRequest{ResultID: "result_projectless_team_01"})
	var read struct {
		Structured struct {
			Status   string `json:"status"`
			Coverage struct {
				Details []struct {
					Code string `json:"code"`
				} `json:"details"`
			} `json:"coverage"`
		} `json:"structured"`
	}
	if err := json.Unmarshal(stored.structured, &read); err != nil {
		t.Fatal(err)
	}
	node.byID.Status = read.Structured.Status
	for _, d := range read.Structured.Coverage.Details {
		node.byID.Details = append(node.byID.Details, d.Code)
	}
	return node
}

func TestAFactReadAbortLogCarriesNoErrorText(t *testing.T) {
	err := &contextfabric.StageError{Stage: contextfabric.StageFactRead, Err: fmt.Errorf("%w: read canonical facts: %w", contextfabric.ErrFactReadAborted, &contextfabric.FactReadAbortDetail{
		RequirementCount: 2, SubjectKinds: []string{"team"}, MemberKind: "project",
		Err: errors.New("fact query subjects must be unique: team:SECRETMARKER appears more than once"),
	})}
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, err
	}))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationRequest(t, token))
	if strings.Contains(logs.String(), "SECRETMARKER") {
		t.Fatalf("the failure log carries the fact-read error text: %s", logs.String())
	}
	entry := decodeFailureLog(t, logs.String())
	if entry["fact_read_requirement_count"] != float64(2) || entry["fact_read_member_kind"] != "project" {
		t.Fatalf("abort shape missing from the log: %v", entry)
	}
}

func TestAPlannedFactReadOverADeniedCohortIsNotServedComplete(t *testing.T) {
	node := askProjectlessTeam(t, true, contextfabric.FactRequirement{Kind: contextfabric.FactMembership})
	if node.byID.Status != "partial" {
		t.Fatalf("by-id status = %q, want partial", node.byID.Status)
	}
	denied := false
	for _, code := range node.byID.Details {
		denied = denied || code == "graph_cohort_denied_by_authorization"
	}
	if !denied {
		t.Fatalf("by-id read lost the denial row: %v", node.byID.Details)
	}
	if node.Structured.Status != "partial" {
		t.Fatalf("status = %q, want partial over a denied cohort: %+v", node.Structured.Status, node.Structured.Details)
	}
}
