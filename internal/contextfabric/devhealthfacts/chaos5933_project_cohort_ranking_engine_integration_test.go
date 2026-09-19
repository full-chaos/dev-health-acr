package devhealthfacts_test

// A project cohort is ranked through the real Engine.Investigate path over a
// real ClickHouse. Every fact the ranking consumes is written by the real
// producers' own read path (health, workload and investment providers over
// seeded domain tables); only graph discovery and the model are controlled.
//
// Floor arithmetic is derived here from the design weights, never read back
// from production constants: deficiency 20, readiness 15, health 25,
// workload 10, investment_mix 30, floor 50. A project member is never read
// for operational_deficiencies (team-only capability) and carries no
// readiness rows, so its available weight is the sum of the roll-up
// families it actually has.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	designWeightHealth     = 25
	designWeightWorkload   = 10
	designWeightInvestment = 30
	designFloor            = 50
)

// projectCohortCell is one cohort member: which of the three project-scope
// families its seeded domain rows give it.
type projectCohortCell struct {
	key                          string
	health, workload, investment bool
	// healthAgeDays is how old the seeded known health band is.
	healthAgeDays int
}

// healthWindowDays is the design's freshness window for a known health band.
const healthWindowDays = 14

// hasHealth: a seeded known band counts only inside the freshness window.
func (c projectCohortCell) hasHealth() bool {
	return c.health && c.healthAgeDays <= healthWindowDays
}

func (c projectCohortCell) weight() int {
	total := 0
	if c.hasHealth() {
		total += designWeightHealth
	}
	if c.workload {
		total += designWeightWorkload
	}
	if c.investment {
		total += designWeightInvestment
	}
	return total
}

func (c projectCohortCell) families() int {
	count := 0
	for _, present := range []bool{c.hasHealth(), c.workload, c.investment} {
		if present {
			count++
		}
	}
	return count
}

// wantScored is the design rule: enough weight AND at least two families.
func (c projectCohortCell) wantScored() bool {
	return c.weight() >= designFloor && c.families() >= 2
}

func (c projectCohortCell) missing(signal string) bool {
	switch signal {
	case contextfabric.RankingSignalHealthRisk:
		return !c.hasHealth()
	case contextfabric.RankingSignalWorkloadPressure:
		return !c.workload
	case contextfabric.RankingSignalInvestmentMix:
		return !c.investment
	}
	return true
}

var projectCohortCells = []projectCohortCell{
	{key: "ALL3", health: true, workload: true, investment: true, healthAgeDays: 1},
	{key: "HLTHINV", health: true, investment: true, healthAgeDays: 2},
	{key: "HLTHWORK", health: true, workload: true, healthAgeDays: 1},
	{key: "INVONLY", investment: true},
	{key: "NOTHING"},
	// A known band exactly at the window edge is fresh; one past it is not.
	{key: "EDGE14", health: true, investment: true, healthAgeDays: 14},
	{key: "STALE30", health: true, investment: true, healthAgeDays: 30},
}

func seedProjectCohort(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string, cells []projectCohortCell) {
	t.Helper()
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	workAt := ts(2026, 9, 18, 0, 0, 0)
	exec := func(what, sql string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	for _, cell := range cells {
		projectID := "proj-" + strings.ToLower(cell.key)
		teamID := "team-" + strings.ToLower(cell.key)
		repoLabel := "repo-" + strings.ToLower(cell.key)
		exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			projectID, orgID, "linear", cell.key, "Project "+cell.key, uint8(1), "active", "", epoch)
		exec("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			teamID, teamID, epoch, orgID, "linear", uint8(1))
		exec("team_project_ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, projectID, cell.key, "native", epoch, nil, epoch)
		exec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
			repoUUID(repoLabel), orgID, "acme/"+repoLabel, "github", workAt)
		exec("team_repo_ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", teamID, repoUUID(repoLabel), "acme/"+repoLabel, "exact", "native", uint8(1), uint16(100), int32(0), epoch, nil, epoch)
		if cell.health {
			day := recentHealthDay(cell.healthAgeDays)
			exec("compounding_risk_daily", `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at) VALUES (?,?,?,?,?,?,?)`,
				orgID, day, "team", teamID, 0.55, "elevated", day.Add(6*time.Hour))
		}
		if cell.workload {
			p50 := uint16(21)
			exec("capacity_forecasts", `INSERT INTO capacity_forecasts (forecast_id, computed_at, team_id, work_scope_id, backlog_size, p50_days, throughput_mean, throughput_stddev, insufficient_history, high_variance, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				"fc-"+cell.key, recentHealthDay(1).Add(6*time.Hour), teamID, projectID, uint32(12), &p50, 3.2, 0.8, uint8(0), uint8(0), orgID)
		}
		if cell.investment {
			exec("work_unit_investments", `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				"wu-"+cell.key, workAt, workAt, repoUUID(repoLabel), 10.0, map[string]float64{"feature_delivery": 0.7, "maintenance": 0.3}, map[string]float64{}, "{}", workAt, orgID)
		}
	}
}

type projectCohortGraph struct {
	t       *testing.T
	members []contextfabric.SubjectRef
}

func (*projectCohortGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "cohort-proof", Epoch: 1}, nil
}

func (g *projectCohortGraph) ResolveSubjects(_ context.Context, p storage.Principal, r contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion, _ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection, _ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	candidate, ok := graphrank.NodeCandidate(p, r.RequestedScope, "Team Alpha", graphrank.CandidateNode{UUID: "team-node", Name: "Team Alpha", Attributes: map[string]interface{}{"subject_kind": "team", "canonical_id": "team:ALPHA", "label": "Team Alpha", "authorization_repositories": []string{"acme/allowed"}, "authorization_projects": "*", "authorization_teams": "*"}}, func(contextfabric.SubjectRef) bool { return false }, true, nil, r.RequestID)
	if !ok {
		g.t.Fatal("NodeCandidate rejected the scope anchor")
	}
	candidate.State = contextfabric.ResolutionCommitted
	candidate.ReceiptID = "receipt_cohort_proof"
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{candidate}, Committed: []contextfabric.SubjectRef{candidate.Subject}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

func (g *projectCohortGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	members := make([]contextfabric.CohortMember, 0, len(g.members))
	for i, subject := range g.members {
		members = append(members, contextfabric.CohortMember{Subject: subject, Rank: i + 1, InclusionReasons: []string{"matched"}})
	}
	return contextfabric.GraphContext{
		Cohort:           &contextfabric.Cohort{Kind: contextfabric.SubjectProject, Rationale: "projects owned by the anchor team", Members: members, Complete: true},
		Paths:            []contextfabric.RelationshipPath{},
		DriverCandidates: []contextfabric.DriverJudgment{},
		FactRequirements: []contextfabric.FactRequirement{},
		EvidenceRefIDs:   []string{},
		Coverage:         contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}},
	}, nil
}

type projectCohortModel struct {
	facts contextfabric.CanonicalFactBundle
}

func (m *projectCohortModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFrame = &contextfabric.QuestionFrame{Goals: []contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey}, SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionChildrenOfScope, Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Team Alpha"}, MemberKind: contextfabric.SubjectProject}}, Temporal: contextfabric.TemporalIntentCurrent}
	receipt.QuestionFamily = contextfabric.QuestionFamilyScopedCohortStatus
	receipt.ScopeAnchorKind = contextfabric.SubjectTeam
	receipt.ScopeAnchorTerm = "Team Alpha"
	receipt.RequestedSubjectKind = contextfabric.SubjectProject
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "projects_under_pressure", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Team Alpha"}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactHealth}}}, receipt, nil
}

func (m *projectCohortModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	m.facts = input.Facts
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: "Ranked projects.", CurrentState: "Ranked projects.", DeterministicAnswer: "Ranked projects.", StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

type projectCohortRun struct {
	result contextfabric.InvestigationResult
	logs   string
	model  *projectCohortModel
}

func runProjectCohortInvestigation(t *testing.T, ctx context.Context, cells []projectCohortCell) projectCohortRun {
	t.Helper()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"projects", "team_project_ownership", "team_repo_ownership", "teams",
		"investment_metrics_daily", "capacity_forecasts", "estimate_coverage_metrics_daily",
		"compounding_risk_daily", "work_unit_investments", "repos", "work_item_team_attributions",
		"recommendations_daily",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	orgID := sharedTestOrgID(t)
	seedProjectCohort(t, ctx, direct, orgID, cells)
	// The anchor team's own deficiency read is clean and available: evidence
	// about the team only, never about the projects it anchors.
	if err := direct.Exec(ctx, `INSERT INTO recommendations_daily (team_id, org_id, rule_id, window_start, window_end, fired, severity, title, rationale, success_criterion, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"ALPHA", orgID, "saturation", date(2026, 7, 29), date(2026, 8, 12), true, "warning", "Saturation", "elevated", "below threshold", ts(2026, 8, 12, 2, 0, 0)); err != nil {
		t.Fatalf("seed anchor deficiency: %v", err)
	}

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	members := make([]contextfabric.SubjectRef, 0, len(cells))
	for _, cell := range cells {
		members = append(members, projectSubject("linear", "proj-"+strings.ToLower(cell.key)))
	}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	model := &projectCohortModel{}
	at := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:        &projectCohortGraph{t: t, members: members},
		Facts:        registry,
		Requirements: registry,
		Telemetry:    contextfabric.NewSlogEngineTelemetry(logger),
		Synthesizer:  contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "cohort-proof", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}},
		Results:      memoryinvestigation.NewStore(),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "cohort-proof", Now: func() time.Time { return at }, NewResultID: func() string { return "result_cohort_proof" }})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_cohort_proof", Question: "Which projects owned by Team Alpha need the most attention?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/allowed"}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "cohort-proof", Version: "1.0.0", Surface: "workbench"}}
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/allowed"}}, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v\nlogs:\n%s", err, logBuffer.String())
	}
	return projectCohortRun{result: result, logs: logBuffer.String(), model: model}
}

func rankingOutcomeRow(t *testing.T, result contextfabric.InvestigationResult) contractsv1.ContextFabricPlanRequirementOutcomeRow {
	t.Helper()
	for _, row := range result.Completeness.Outcomes {
		if row.Obligation == string(contextfabric.ObligationRanking) && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			return row
		}
	}
	t.Fatalf("no assembled ranking outcome row in %+v", result.Completeness.Outcomes)
	return contractsv1.ContextFabricPlanRequirementOutcomeRow{}
}

func cohortMemberFor(t *testing.T, cohort *contextfabric.Cohort, cell projectCohortCell) contextfabric.CohortMember {
	t.Helper()
	want := projectSubject("linear", "proj-"+strings.ToLower(cell.key)).CanonicalID
	for _, member := range cohort.Members {
		if member.Subject.CanonicalID == want {
			return member
		}
	}
	t.Fatalf("member %s absent from served cohort", cell.key)
	return contextfabric.CohortMember{}
}

func hasSignal(signals []string, name string) bool {
	for _, s := range signals {
		if s == name {
			return true
		}
	}
	return false
}

func TestCHAOS5933ProjectCohortIsRankedThroughTheEngineAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	run := runProjectCohortInvestigation(t, ctx, projectCohortCells)
	result := run.result
	if result.Cohort == nil {
		t.Fatalf("no served cohort; status=%s", result.Status)
	}
	if got := len(result.Cohort.Members); got != len(projectCohortCells) {
		t.Fatalf("cohort members = %d, want %d (one population partition)", got, len(projectCohortCells))
	}

	t.Run("one_partition_disclosed_untruncated", func(t *testing.T) {
		if !result.Cohort.Complete || result.Cohort.Truncated {
			t.Fatalf("cohort complete=%v truncated=%v, want the whole population served", result.Cohort.Complete, result.Cohort.Truncated)
		}
		seen := map[string]int{}
		for _, member := range result.Cohort.Members {
			seen[member.Subject.CanonicalID]++
		}
		for id, count := range seen {
			if count != 1 {
				t.Errorf("%s served %d times, want once", id, count)
			}
		}
	})

	t.Run("floor_arithmetic_both_sides", func(t *testing.T) {
		scored := 0
		for _, cell := range projectCohortCells {
			member := cohortMemberFor(t, result.Cohort, cell)
			isScored := member.Outcome == contextfabric.CohortOutcomeProvisional || member.Outcome == contextfabric.CohortOutcomeQualified
			if isScored != cell.wantScored() {
				t.Errorf("%s weight=%d families=%d outcome=%s, want scored=%v", cell.key, cell.weight(), cell.families(), member.Outcome, cell.wantScored())
			}
			if isScored != (member.Score != nil) {
				t.Errorf("%s outcome=%s Score=%v disagree", cell.key, member.Outcome, member.Score)
			}
			if isScored {
				scored++
			}
		}
		if scored != 3 {
			t.Errorf("scored members = %d, want 3 (ALL3 at 65, HLTHINV and EDGE14 at 55)", scored)
		}
		if cell := projectCohortCells[0]; cell.weight() != 65 {
			t.Fatalf("test cell arithmetic: %d", cell.weight())
		}
	})

	t.Run("per_member_signal_availability", func(t *testing.T) {
		for _, cell := range projectCohortCells {
			member := cohortMemberFor(t, result.Cohort, cell)
			for _, signal := range []string{contextfabric.RankingSignalHealthRisk, contextfabric.RankingSignalWorkloadPressure, contextfabric.RankingSignalInvestmentMix} {
				if got := hasSignal(member.MissingSignals, signal); got != cell.missing(signal) {
					t.Errorf("%s %s missing=%v, want %v (missing=%v)", cell.key, signal, got, cell.missing(signal), member.MissingSignals)
				}
			}
			for _, signal := range []string{contextfabric.RankingSignalDeficiencySeverity, contextfabric.RankingSignalReadinessGap} {
				if !hasSignal(member.MissingSignals, signal) {
					t.Errorf("%s %s credited without the member's own evidence (missing=%v)", cell.key, signal, member.MissingSignals)
				}
			}
		}
	})

	t.Run("served_cohort_ranked_line", func(t *testing.T) {
		var line string
		for _, candidate := range strings.Split(run.logs, "\n") {
			if strings.Contains(candidate, "context fabric cohort ranked") {
				if line != "" {
					t.Fatalf("more than one cohort ranked line:\n%s", run.logs)
				}
				line = candidate
			}
		}
		if line == "" {
			t.Fatalf("no cohort ranked line in logs:\n%s", run.logs)
		}
		health, workload, investment := 0, 0, 0
		for _, cell := range projectCohortCells {
			if cell.hasHealth() {
				health++
			}
			if cell.workload {
				workload++
			}
			if cell.investment {
				investment++
			}
		}
		for _, want := range []string{
			"cohort_kind=project",
			fmt.Sprintf("member_count=%d", len(projectCohortCells)),
			fmt.Sprintf("%s:%d", contextfabric.RankingSignalHealthRisk, health),
			fmt.Sprintf("%s:%d", contextfabric.RankingSignalWorkloadPressure, workload),
			fmt.Sprintf("%s:%d", contextfabric.RankingSignalInvestmentMix, investment),
			fmt.Sprintf("deficiency_zero_withheld=%d", len(projectCohortCells)),
			"read_attribution_carried=true",
		} {
			if !strings.Contains(line, want) {
				t.Errorf("cohort ranked line lacks %q:\n%s", want, line)
			}
		}
		if strings.Contains(line, contextfabric.RankingSignalDeficiencySeverity+":") {
			t.Errorf("cohort ranked line credits deficiency_severity to project members:\n%s", line)
		}
	})

	t.Run("completeness_authority_reports_ranking_available", func(t *testing.T) {
		row := rankingOutcomeRow(t, result)
		if row.Outcome == contractsv1.ContextFabricRequirementUnavailable {
			t.Fatalf("ranking outcome = %+v, want not unavailable", row)
		}
		if row.Outcome != contractsv1.ContextFabricRequirementNarrowed || row.CauseCoverage != contractsv1.ContextFabricCoverageDetailFactProviderReported {
			t.Fatalf("ranking outcome = %+v, want narrowed by provider-reported partial evidence", row)
		}
	})
}

// A cohort in which no member reaches the floor is reported unavailable by
// the completeness authority: the same arithmetic, the other side.
func TestCHAOS5933ProjectCohortBelowTheFloorIsReportedUnavailable(t *testing.T) {
	ctx := context.Background()
	cells := []projectCohortCell{
		{key: "HLTHWORK", health: true, workload: true, healthAgeDays: 1},
		{key: "INVONLY", investment: true},
		{key: "NOTHING"},
	}
	run := runProjectCohortInvestigation(t, ctx, cells)
	if run.result.Cohort == nil {
		t.Fatalf("no served cohort; status=%s", run.result.Status)
	}
	for _, cell := range cells {
		member := cohortMemberFor(t, run.result.Cohort, cell)
		if member.Outcome == contextfabric.CohortOutcomeProvisional || member.Outcome == contextfabric.CohortOutcomeQualified || member.Score != nil {
			t.Errorf("%s weight=%d outcome=%s score=%v, want below the floor", cell.key, cell.weight(), member.Outcome, member.Score)
		}
	}
	row := rankingOutcomeRow(t, run.result)
	if row.Outcome != contractsv1.ContextFabricRequirementUnavailable || row.Impact != contractsv1.ContextFabricAnswerImpactDimension {
		t.Fatalf("ranking outcome = %+v, want unavailable/dimension when no member clears the floor", row)
	}
}
