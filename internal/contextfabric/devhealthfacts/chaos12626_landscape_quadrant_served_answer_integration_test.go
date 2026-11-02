package devhealthfacts_test

// CHAOS-12626: a comparison of teams that asks for landscape facts, through the
// real Engine.Investigate over a real ClickHouse with the production registry,
// draws the landscape_position quadrant. Only graph resolution and the model are
// controlled.
//
// The teams are the ones `dho fixtures generate` writes for its mixed world
// (testdata/dho_mixed_s9440_teams.json: id, name and provider, copied from the
// frozen world, whose sha256 the file records), one team per provider. No dho
// world holds ic_landscape_rolling_30d rows: Ops computes them from
// user_metrics_daily in the daily finalize job, and no frozen world holds those
// either. So the landscape rows here are seeded by hand for those teams, in the
// table's own columns, as every landscape test in this package seeds them.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

type dhoTeam struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// dhoMixedTeams reads the producer's teams, by provider.
func dhoMixedTeams(t *testing.T) map[string]dhoTeam {
	t.Helper()
	raw, err := os.ReadFile("testdata/dho_mixed_s9440_teams.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SourceSHA256 string    `json:"source_sha256"`
		Teams        []dhoTeam `json:"teams"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.SourceSHA256) != 64 || len(doc.Teams) != 8 {
		t.Fatalf("the dho teams file does not name its frozen world: %d teams, sha %q", len(doc.Teams), doc.SourceSHA256)
	}
	byProvider := map[string]dhoTeam{}
	for _, team := range doc.Teams {
		if _, seen := byProvider[team.Provider]; !seen {
			byProvider[team.Provider] = team
		}
	}
	for _, provider := range []string{"github", "gitlab", "jira", "linear"} {
		if _, ok := byProvider[provider]; !ok {
			t.Fatalf("the dho mixed world has no %s team", provider)
		}
	}
	return byProvider
}

// quadrantSeed is one team's landscape rows on one day: its identities, each
// with the same churn and delivery units on every map, as Ops writes one copy
// per map.
type quadrantSeed struct {
	team       dhoTeam
	daysAgo    int
	identities int
	churn      uint64
	units      uint32
}

func quadrantStore(t *testing.T, ctx context.Context, orgID string, seeds []quadrantSeed) contextpacket.ClickHouseQueryClient {
	t.Helper()
	rawQuery, direct := newDedicatedClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "team_project_ownership", "ic_landscape_rolling_30d") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, seed := range seeds {
		day := today.AddDate(0, 0, -seed.daysAgo)
		for person := range seed.identities {
			for _, mapName := range []string{"churn_throughput", "cycle_throughput", "wip_throughput"} {
				if err := direct.Exec(ctx, `INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					orgID, repoUUID("repo-"+seed.team.ID), day, fmt.Sprintf("identity-%d", person), seed.team.ID, mapName, 1.5, 2.5, 0.5, 0.5, seed.churn, seed.units, 5.0, uint32(3), day); err != nil {
					t.Fatalf("seed %s: %v", seed.team.ID, err)
				}
			}
		}
	}
	return statementthreads.API(rawQuery)
}

// quadrantGraph commits the compared teams through graphrank.NodeCandidate, the
// function that turns a graph node into a candidate.
type quadrantGraph struct {
	t     *testing.T
	teams []dhoTeam
}

func (*quadrantGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	outcomes := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range outcomes {
		outcomes[i] = contextfabric.StoredSubjectAdmitted
	}
	return outcomes, nil
}

func (*quadrantGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "landscape-quadrant", Epoch: 1}, nil
}

func (g *quadrantGraph) ResolveSubjects(_ context.Context, p storage.Principal, r contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	var resolution contextfabric.SubjectResolution
	for index, team := range g.teams {
		candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, team.Name, graphrank.CandidateNode{UUID: fmt.Sprintf("team-node-%d", index), Name: team.Name, Attributes: map[string]interface{}{"subject_kind": string(contractsv1.ContextFabricSubjectTeam), "canonical_id": "team:" + team.ID, "label": team.Name, "authorization_repositories": []string{servedLandscapeRepo}, "authorization_projects": "*", "authorization_teams": "*"}}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
		if !ok {
			g.t.Fatalf("NodeCandidate rejected %s", team.ID)
		}
		candidate.State = contextfabric.ResolutionCommitted
		candidate.ReceiptID = fmt.Sprintf("receipt_landscape_quadrant_%d", index)
		resolution.Candidates = append(resolution.Candidates, candidate)
		resolution.Committed = append(resolution.Committed, candidate.Subject)
	}
	return resolution, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

func (*quadrantGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{
		Paths: []contextfabric.RelationshipPath{}, DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{}, EvidenceRefIDs: []string{},
		Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

// quadrantModel reads the question as a comparison of the teams and claims both
// measures of every landscape fact it is given, from the scalars the synthesis
// input carries, as a model reads them.
type quadrantModel struct {
	teams        []dhoTeam
	requirements []contextfabric.FactRequirement
	draftErr     error
	// seen records each fact the synthesis input carried, as kind/subject.
	seen []string
}

func (m *quadrantModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	var operands []contextfabric.SubjectOperand
	var terms []string
	team := contractsv1.ContextFabricSubjectTeam
	for _, each := range m.teams {
		operands = append(operands, contextfabric.SubjectOperand{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{each.Name}, ExpectedKind: &team}})
		terms = append(terms, each.Name)
	}
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFamily = contextfabric.QuestionFamilyExplicitComparison
	receipt.QuestionFrame = &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionExplicitSet, Explicit: &contextfabric.ExplicitSetExpression{Operands: operands}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeExplicitCohort, RequestedJudgment: "compare", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: terms, FactRequirements: m.requirements}, receipt, nil
}

func (m *quadrantModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: "Where the teams stand.", CurrentState: "Where the teams stand.", DeterministicAnswer: "Where the teams stand.", StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	for index, fact := range input.Facts.Facts {
		m.seen = append(m.seen, string(fact.Kind)+"/"+fact.Subject.CanonicalID)
		if fact.Kind != contextfabric.FactLandscape {
			continue
		}
		var claimIDs []string
		for _, field := range []string{"churn_loc_30d", "delivery_units_30d"} {
			value, scalar := scalarOfFactValue(fact.Fields[field])
			if !scalar {
				continue
			}
			claimID := fmt.Sprintf("claim_landscape_quadrant_%02d_%s", index, field)
			draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: claimID, Kind: fact.Kind, Subject: fact.Subject, Field: field, Value: value})
			claimIDs = append(claimIDs, claimID)
		}
		if len(claimIDs) == 0 {
			continue
		}
		draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, fact.EvidenceRefIDs...)
		draft.Drivers = append(draft.Drivers, contextfabric.DriverJudgment{
			DriverID: fmt.Sprintf("driver_landscape_quadrant_%02d", index), Standing: contextfabric.DriverPrincipal, Category: "narrative",
			Title: "Landscape", Summary: "Where the team stands.",
			AffectedSubjects: []contextfabric.SubjectRef{fact.Subject}, EvidenceRefIDs: append([]string(nil), fact.EvidenceRefIDs...),
			ClaimedFactIDs: claimIDs,
			Derivation:     contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.9, Current: true,
		})
	}
	m.draftErr = draft.ValidateAgainst(input)
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

func serveQuadrantQuestion(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, teams []dhoTeam, requirements []contextfabric.FactRequirement, enabled bool) contextfabric.InvestigationResult {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model := &quadrantModel{teams: teams, requirements: requirements}
	at := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:        &quadrantGraph{t: t, teams: teams},
		Facts:        registry,
		Requirements: registry,
		Synthesizer:  contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "landscape-quadrant", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}},
		Results:      memoryinvestigation.NewStore(),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "landscape-quadrant", LandscapeQuadrantEnabled: enabled, FactCapabilities: registry.Capabilities(), Now: func() time.Time { return at }, NewResultID: func() string { return "result_landscape_quadrant" }})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_landscape_quadrant", Question: "Where do these teams stand on churn and delivery?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{servedLandscapeRepo}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "landscape-quadrant", Version: "1.0.0", Surface: "workbench"}}
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{servedLandscapeRepo}}, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v", err)
	}
	t.Logf("synthesis facts: %v", model.seen)
	if model.draftErr != nil {
		t.Fatalf("the draft does not validate: %v", model.draftErr)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served result does not validate: %v", err)
	}
	return result
}

func servedQuadrant(result contextfabric.InvestigationResult) *contractsv1.ContextFabricRenderShape {
	return servedQuadrantOf(result.RenderShapes)
}

func landscapeAsked() []contextfabric.FactRequirement {
	return []contextfabric.FactRequirement{{Kind: contextfabric.FactLandscape}}
}

// One team of each provider on one day, at 2 or more identities, and a fifth
// team of one identity: the quadrant places the four, in operand order, and
// leaves the fifth out; the answer serves the quadrant's line.
func TestAComparisonOfTeamsOnOneDayDrawsTheirQuadrant(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-landscape-quadrant"
	byProvider := dhoMixedTeams(t)
	teams := []dhoTeam{byProvider["github"], byProvider["gitlab"], byProvider["jira"], byProvider["linear"]}
	solo := dhoTeam{ID: "linear:MOB", Name: "Mobile", Provider: "linear"}
	seeds := []quadrantSeed{
		{teams[0], 2, 2, 1200, 9}, {teams[1], 2, 3, 300, 14}, {teams[2], 2, 4, 0, 6}, {teams[3], 2, 2, 2400, 31},
		{solo, 2, 1, 900, 40},
	}
	query := quadrantStore(t, ctx, orgID, seeds)
	result := serveQuadrantQuestion(t, ctx, query, orgID, append(slices.Clone(teams), solo), landscapeAsked(), true)
	t.Logf("plan family=%s render_kinds=%v claims=%d committed=%d shapes=%d status=%s", result.AnswerPlan.Family, result.AnswerPlan.RenderKinds, len(result.ClaimedFacts), len(result.SubjectResolution.Committed), len(result.RenderShapes), result.Status)
	for _, candidate := range result.SubjectResolution.Candidates {
		t.Logf("candidate %s %q state=%s", candidate.Subject.CanonicalID, candidate.Subject.Label, candidate.State)
	}
	t.Logf("limitations=%v", result.Limitations)
	t.Logf("clarification=%q warnings=%v", result.SubjectResolution.ClarificationPrompt, result.Warnings)
	quadrant := servedQuadrant(result)
	if quadrant == nil {
		t.Fatalf("no quadrant: shapes %+v, claims %+v", result.RenderShapes, result.ClaimedFacts)
	}
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2)
	if want := "Churn and delivery units, 30 days to " + day.Format("Jan 2, 2006"); quadrant.Title != want {
		t.Fatalf("title %q, want %q", quadrant.Title, want)
	}
	// Each team's row sums its identities' one copy: identities × the seeded value.
	if len(quadrant.Series[0].Points) != 4 {
		t.Fatalf("points %+v, want the four teams of 2 or more identities", quadrant.Series[0].Points)
	}
	for index, seed := range seeds[:4] {
		x, y := quadrant.Series[0].Points[index], quadrant.Series[1].Points[index]
		wantX, wantY := float64(seed.churn)*float64(seed.identities), float64(seed.units)*float64(seed.identities)
		if x.Label != seed.team.Name || y.Label != seed.team.Name || x.Value != wantX || y.Value != wantY {
			t.Fatalf("point %d = %s (%v, %v), want %s (%v, %v)", index, x.Label, x.Value, y.Value, seed.team.Name, wantX, wantY)
		}
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
		t.Fatalf("the quadrant's line is missing: %v", result.Limitations)
	}
	projection := answerprojection.Project(result, answerprojection.Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("the projection does not validate: %v", err)
	}
	if servedQuadrantOf(projection.RenderShapes) == nil {
		t.Fatalf("the projection dropped the quadrant (omitted %d)", projection.ProjectionBudget.RenderShapesOmitted)
	}
	// The real-encoder fixture for the ask-dev verifier and the web panel
	// (CHAOS-12624, CHAOS-12625): set ACR_QUADRANT_FIXTURE_DIR to write the
	// served result and its projection, as the production encoder writes them.
	if dir := os.Getenv("ACR_QUADRANT_FIXTURE_DIR"); dir != "" {
		for name, document := range map[string]any{"landscape_quadrant_result.json": result, "landscape_quadrant_projection.json": projection} {
			encoded, err := json.MarshalIndent(document, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), append(encoded, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func servedQuadrantOf(shapes []contractsv1.ContextFabricRenderShape) *contractsv1.ContextFabricRenderShape {
	for index := range shapes {
		if shapes[index].SelectedBy == contractsv1.ContextFabricRenderRuleLandscapePosition {
			return &shapes[index]
		}
	}
	return nil
}

// Check 10 and the one-day rule: no quadrant for a team on another day, for a
// status comparison (which reads landscape facts by composition, and is no
// question about where teams stand), or with the flag off; and no line. Each
// answer still claims both measures of every team, so only the rule's own
// clause can refuse it.
func TestAComparisonOfTeamsDrawsNoQuadrant(t *testing.T) {
	ctx := context.Background()
	byProvider := dhoMixedTeams(t)
	teams := []dhoTeam{byProvider["github"], byProvider["gitlab"], byProvider["jira"], byProvider["linear"]}
	sameDay := []quadrantSeed{{teams[0], 2, 2, 1200, 9}, {teams[1], 2, 3, 300, 14}, {teams[2], 2, 4, 0, 6}, {teams[3], 2, 2, 2400, 31}}
	for name, tc := range map[string]struct {
		seeds        []quadrantSeed
		requirements []contextfabric.FactRequirement
		enabled      bool
	}{
		"a team on another day": {append(slices.Clone(sameDay[:3]), quadrantSeed{teams[3], 9, 2, 2400, 31}), landscapeAsked(), true},
		"a status comparison":   {sameDay, []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}}, true},
		"the flag off":          {sameDay, landscapeAsked(), false},
	} {
		t.Run(name, func(t *testing.T) {
			orgID := "org-landscape-quadrant-none-" + fmt.Sprint(len(name))
			query := quadrantStore(t, ctx, orgID, tc.seeds)
			result := serveQuadrantQuestion(t, ctx, query, orgID, teams, tc.requirements, tc.enabled)
			if quadrant := servedQuadrant(result); quadrant != nil {
				t.Fatalf("a quadrant was drawn: %+v", quadrant)
			}
			claimed := 0
			for _, claim := range result.ClaimedFacts {
				if claim.Kind == contextfabric.FactLandscape {
					claimed++
				}
			}
			if claimed != 2*len(teams) {
				t.Fatalf("%d landscape claims, want both measures of all %d teams, so the rule alone decides", claimed, len(teams))
			}
			if slices.Contains(result.Limitations, contractsv1.ContextFabricLandscapePositionLimitation) {
				t.Fatalf("the quadrant's line was served with no quadrant: %v", result.Limitations)
			}
		})
	}
}
