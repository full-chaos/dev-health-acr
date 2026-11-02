package devhealthfacts_test

// CHAOS-12606: "how is team Platform investing across its repositories" arrives
// as a children_of_scope frame under a team anchor, with the goal set
// {allocate_investment}. This runs it through the real Engine.Investigate over a
// real ClickHouse with the production registry. Only the graph (it commits the
// anchor team, as a scoped resolution does, and records the discovery request)
// and the model are controlled.

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const teamAllocationTeam = "team-platform"

// teamChildrenGraph commits the anchor team and records what discovery is asked.
type teamChildrenGraph struct {
	servedLandscapeGraph
	subjects  []contextfabric.SubjectRef
	discovery []contextfabric.GraphDiscoveryRequest
}

func (g *teamChildrenGraph) DiscoverContext(ctx context.Context, p storage.Principal, request contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	g.discovery = append(g.discovery, request)
	return g.servedLandscapeGraph.DiscoverContext(ctx, p, request)
}

func (g *teamChildrenGraph) ResolveSubjects(_ context.Context, p storage.Principal, r contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	resolution := contextfabric.SubjectResolution{}
	for i, subject := range g.subjects {
		candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, subject.Label, graphrank.CandidateNode{UUID: fmt.Sprintf("node-%d", i), Name: subject.Label, Attributes: map[string]interface{}{"subject_kind": string(subject.Kind), "canonical_id": subject.CanonicalID, "label": subject.Label, "authorization_repositories": []string{servedLandscapeRepo, "acme/second"}, "authorization_projects": "*", "authorization_teams": "*"}}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
		if !ok {
			g.t.Fatalf("NodeCandidate rejected %+v", subject)
		}
		candidate.State = contextfabric.ResolutionCommitted
		candidate.ReceiptID = fmt.Sprintf("receipt_team_children_%d", i)
		resolution.Candidates = append(resolution.Candidates, candidate)
		resolution.Committed = append(resolution.Committed, candidate.Subject)
	}
	return resolution, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

// teamChildrenModel interprets the team children frame and claims the theme
// shares of every investment fact it is offered.
type teamChildrenModel struct {
	goals    []contextfabric.InvestigationGoal
	shape    contextfabric.InvestigationShape
	offered  []string
	draftErr error
}

func (m *teamChildrenModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFrame = &contextfabric.QuestionFrame{
		Goals:             m.goals,
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: contextfabric.SubjectRepository}},
		Temporal:          contextfabric.TemporalIntentBoundedWindow,
	}
	receipt.ScopeAnchorKind = contextfabric.SubjectTeam
	receipt.ScopeAnchorTerm = "Platform"
	receipt.RequestedSubjectKind = contextfabric.SubjectRepository
	return contextfabric.InterpretedQuestion{Shape: m.shape, RequestedJudgment: "How the team invests across its repositories", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Platform"}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactInvestment}}}, receipt, nil
}

func (m *teamChildrenModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	text := "Where the team's effort went."
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: text, CurrentState: text, DeterministicAnswer: text, StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	for index, fact := range input.Facts.Facts {
		if fact.Kind != contextfabric.FactInvestment {
			continue
		}
		m.offered = append(m.offered, fact.Subject.CanonicalID)
		var ids []string
		for _, field := range []string{"theme_feature_delivery", "theme_maintenance", "theme_quality", "theme_operational", "theme_risk"} {
			value, scalar := scalarOfFactValue(fact.Fields[field])
			if !scalar {
				continue
			}
			id := fmt.Sprintf("claim_team_children_%d_%s", index, field)
			ids = append(ids, id)
			draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: id, Kind: fact.Kind, Subject: fact.Subject, Field: field, Value: value, FactID: fact.ID})
		}
		if len(ids) == 0 {
			continue
		}
		draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, fact.EvidenceRefIDs...)
		draft.Drivers = append(draft.Drivers, contextfabric.DriverJudgment{
			DriverID: fmt.Sprintf("driver_team_children_%d", index), Standing: contextfabric.DriverPrincipal, Category: "narrative",
			Title: "Investment", Summary: text,
			AffectedSubjects: []contextfabric.SubjectRef{fact.Subject}, EvidenceRefIDs: append([]string(nil), fact.EvidenceRefIDs...),
			ClaimedFactIDs: ids,
			Derivation:     contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.9, Current: true,
		})
	}
	if len(draft.ClaimedFacts) == 0 {
		draft.Status = contextfabric.InvestigationPartial
		draft.Limitations = []string{"No investment fact was found."}
	}
	m.draftErr = draft.ValidateAgainst(input)
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

func teamChildrenStore(t *testing.T, ctx context.Context, orgID string) contextpacket.ClickHouseQueryClient {
	t.Helper()
	rawQuery, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	at := time.Now().UTC().AddDate(0, 0, -5).Truncate(24 * time.Hour)
	for _, repo := range []struct{ label, name string }{{"repo-tc-a", servedLandscapeRepo}, {"repo-tc-b", "acme/second"}} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repo.label), orgID, repo.name, "github", at); err != nil {
			t.Fatal(err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", teamAllocationTeam, repoUUID(repo.label), repo.name, "exact", "native", uint8(1), uint16(100), int32(0), at, nil, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, unit := range []struct {
		id, repo string
		themes   map[string]float64
	}{
		{"wu-tc-feature", "repo-tc-a", map[string]float64{"feature_delivery": 1}},
		{"wu-tc-mixed", "repo-tc-b", map[string]float64{"maintenance": 0.5, "quality": 0.5}},
	} {
		if err := direct.Exec(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			unit.id, at, at, repoUUID(unit.repo), 1.0, unit.themes, map[string]float64{}, `{"issues":[],"prs":[]}`, at, orgID); err != nil {
			t.Fatal(err)
		}
	}
	return statementthreads.API(rawQuery)
}

func serveTeamChildrenQuestion(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, model *teamChildrenModel, withMembers bool) (contextfabric.InvestigationResult, *teamChildrenGraph) {
	t.Helper()
	return serveTeamChildrenQuestionWith(t, ctx, query, orgID, model, withMembers, true)
}

func serveTeamChildrenQuestionWith(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, model *teamChildrenModel, withMembers, withAnchor bool) (contextfabric.InvestigationResult, *teamChildrenGraph) {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subjects := []contextfabric.SubjectRef{}
	if withAnchor {
		subjects = append(subjects, contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + teamAllocationTeam, Label: "Platform"})
	}
	if withMembers {
		// A hint or a carry can commit the members beside the anchor.
		subjects = append(subjects,
			contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID("repo-tc-a"), Label: servedLandscapeRepo},
			contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID("repo-tc-b"), Label: "acme/second"})
	}
	graph := &teamChildrenGraph{servedLandscapeGraph: servedLandscapeGraph{t: t}, subjects: subjects}
	now := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:       graph, Facts: registry, Requirements: registry,
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "team-children", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}},
		Results:     memoryinvestigation.NewStore(),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "team-children", Now: func() time.Time { return now }, NewResultID: func() string { return "result_team_children" }, InvestmentTreemapEnabled: true, FactCapabilities: registry.Capabilities()})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_team_children", Question: "How is team Platform investing across its repositories over the last 90 days?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "team-children", Version: "1.0.0", Surface: "workbench"}}
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: orgID}, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v", err)
	}
	if model.draftErr != nil {
		t.Fatalf("the draft does not validate: %v", model.draftErr)
	}
	return result, graph
}

func TestATeamsChildrenAllocationQuestionDrawsTheTeamsTreemap(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-team-children"
	query := teamChildrenStore(t, ctx, orgID)
	team := "team:" + teamAllocationTeam
	for _, run := range []struct {
		shape       contextfabric.InvestigationShape
		withMembers bool
	}{{contextfabric.ShapeOpen, false}, {contextfabric.ShapeDiscoveredCohort, false}, {contextfabric.ShapeSingleSubject, false}, {contextfabric.ShapeOpen, true}} {
		shape := run.shape
		model := &teamChildrenModel{goals: []contextfabric.InvestigationGoal{contextfabric.GoalAllocateInvestment}, shape: shape}
		result, graph := serveTeamChildrenQuestion(t, ctx, query, orgID, model, run.withMembers)
		if result.AnswerPlan == nil || result.AnswerPlan.Family != contextfabric.QuestionFamilyInvestmentAllocation {
			t.Fatalf("%s: plan %+v, want investment_allocation", shape, result.AnswerPlan)
		}
		if len(graph.discovery) != 1 || !graph.discovery[0].AnchorFactOnly {
			t.Fatalf("%s: discovery requests %d, want one that reads no member set", shape, len(graph.discovery))
		}
		if result.Cohort != nil {
			t.Fatalf("%s: cohort %+v, want none", shape, result.Cohort)
		}
		sort.Strings(model.offered)
		if len(model.offered) != 1 || model.offered[0] != team {
			t.Fatalf("%s: investment facts offered for %v, want the team's alone", shape, model.offered)
		}
		var treemaps []contractsv1.ContextFabricRenderShape
		for _, s := range result.RenderShapes {
			if s.Kind == contractsv1.ContextFabricRenderKindTreemap {
				treemaps = append(treemaps, s)
			}
		}
		if len(treemaps) != 1 || len(treemaps[0].Series[0].Points) != 5 {
			t.Fatalf("%s: treemaps %+v, limitations %v: want one of the 5 themes", shape, treemaps, result.Limitations)
		}
		want := map[string]float64{"Feature Delivery": 0.5, "Maintenance / Tech Debt": 0.25, "Quality / Reliability": 0.25, "Operational / Support": 0, "Risk / Security": 0}
		for _, point := range treemaps[0].Series[0].Points {
			if share, named := want[point.Label]; !named || point.Value != share {
				t.Fatalf("%s: point %s = %v, want %v", shape, point.Label, point.Value, share)
			}
		}
		for _, limitation := range result.Limitations {
			if limitation == contractsv1.ContextFabricSynthesisNarrativeWithheldLimitation {
				t.Fatalf("%s: the narrative was withheld: %v", shape, result.Limitations)
			}
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("%s: the served result does not validate: %v", shape, err)
		}
	}
}

// Check 10: a second goal keeps the table's family, and discovery reads members
// as before.
func TestATeamsChildrenFrameWithASecondGoalKeepsItsFamilyInTheServedAnswer(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-team-children-two-goals"
	query := teamChildrenStore(t, ctx, orgID)
	model := &teamChildrenModel{goals: []contextfabric.InvestigationGoal{contextfabric.GoalAllocateInvestment, contextfabric.GoalAssessState}, shape: contextfabric.ShapeDiscoveredCohort}
	result, graph := serveTeamChildrenQuestion(t, ctx, query, orgID, model, false)
	if result.AnswerPlan != nil && result.AnswerPlan.Family == contextfabric.QuestionFamilyInvestmentAllocation {
		t.Fatalf("plan %+v: a second goal must keep the table's family", result.AnswerPlan)
	}
	for _, request := range graph.discovery {
		if request.AnchorFactOnly {
			t.Fatal("a second goal's discovery was told to read no member set")
		}
	}
}

// Codex r1 (executed): with the anchor team not committed, the route read the
// repositories committed beside it. The anchor's own fact is the only basis of
// this route, so with no anchor nothing is read: no repository's investment is
// offered, and the answer is not an allocation answer about them.
func TestATeamsChildrenAllocationQuestionWithNoCommittedAnchorReadsNoMember(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-team-children-no-anchor"
	query := teamChildrenStore(t, ctx, orgID)
	model := &teamChildrenModel{goals: []contextfabric.InvestigationGoal{contextfabric.GoalAllocateInvestment}, shape: contextfabric.ShapeOpen}
	result, _ := serveTeamChildrenQuestionWith(t, ctx, query, orgID, model, true, false)
	if len(model.offered) != 0 {
		t.Fatalf("investment facts offered for %v, want none: no anchor team was committed", model.offered)
	}
	for _, claim := range result.ClaimedFacts {
		if claim.Kind == contextfabric.FactInvestment {
			t.Fatalf("an investment claim was served without the anchor: %+v", claim)
		}
	}
	if result.Status == contextfabric.InvestigationComplete {
		t.Fatalf("status %q: an allocation question whose anchor was not committed is not answered", result.Status)
	}
	t.Logf("status=%s limitations=%v", result.Status, result.Limitations)
}
