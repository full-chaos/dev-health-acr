package devhealthfacts_test

// CHAOS-12591: reach run 2 on askdev-1 asked where one repository's investment
// went. The model claimed nine fields of the one investment fact, every claim
// carried that fact's theme_breakdown, and the treemap rule counted nine
// breakdowns. This runs the same answer through the real Engine.Investigate over
// a real ClickHouse: the fact comes from the production provider, the rows reach
// each claim through the production attachment, and only the graph and the
// model are controlled.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

// investmentTreemapFields are the fields the model claims, in the order run 2
// claimed them. Each is a scalar of the one fact.
var investmentTreemapFields = []string{"theme_quality", "theme_feature_delivery", "theme_maintenance", "theme_risk", "theme_operational", "work_unit_count"}

// servedInvestmentModel asks where one repository's effort went and claims
// every field in investmentTreemapFields of the one investment fact.
type servedInvestmentModel struct {
	claimed  []string
	draftErr error
}

func (m *servedInvestmentModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFamily = contextfabric.QuestionFamilyInvestmentAllocation
	receipt.QuestionFrame = &contextfabric.QuestionFrame{
		Goals:             []contextfabric.InvestigationGoal{contextfabric.GoalAllocateInvestment},
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{"Platform"}}},
		Temporal:          contextfabric.TemporalIntentCurrent,
	}
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "investment", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, SubjectTerms: []string{"Platform"}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactInvestment}}}, receipt, nil
}

func (m *servedInvestmentModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	text := "Investment of the subject."
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: text, CurrentState: text, DeterministicAnswer: text, StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	for _, fact := range input.Facts.Facts {
		if fact.Kind != contextfabric.FactInvestment {
			continue
		}
		var ids []string
		for _, field := range investmentTreemapFields {
			value, scalar := scalarOfFactValue(fact.Fields[field])
			if !scalar {
				continue
			}
			id := "claim_served_investment_" + field
			ids = append(ids, id)
			m.claimed = append(m.claimed, field)
			draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: id, Kind: fact.Kind, Subject: fact.Subject, Field: field, Value: value, FactID: fact.ID})
		}
		draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, fact.EvidenceRefIDs...)
		draft.Drivers = append(draft.Drivers, contextfabric.DriverJudgment{
			DriverID: "driver_served_investment_1", Standing: contextfabric.DriverPrincipal, Category: "narrative",
			Title: "Investment", Summary: text,
			AffectedSubjects: []contextfabric.SubjectRef{fact.Subject}, EvidenceRefIDs: append([]string(nil), fact.EvidenceRefIDs...),
			ClaimedFactIDs: ids,
			Derivation:     contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.9, Current: true,
		})
		break
	}
	m.draftErr = draft.ValidateAgainst(input)
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

func TestSeveralClaimsOnOneServedInvestmentFactDrawOneTreemap(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-investment-treemap"
	rawQuery, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	at := time.Now().UTC().AddDate(0, 0, -5).Truncate(24 * time.Hour)
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`,
		repoUUID("repo-treemap"), orgID, servedLandscapeRepo, "github", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	for _, unit := range []struct {
		id     string
		effort float64
		themes map[string]float64
	}{
		{"wu-feature", 1, map[string]float64{"feature_delivery": 1}},
		{"wu-mixed", 1, map[string]float64{"maintenance": 0.5, "quality": 0.5}},
	} {
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			unit.id, at, at, repoUUID("repo-treemap"), unit.effort, unit.themes, map[string]float64{}, `{"issues":[],"prs":[]}`, at, orgID); err != nil {
			t.Fatalf("seed %s: %v", unit.id, err)
		}
	}
	query := statementthreads.API(rawQuery)
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoUUID("repo-treemap"), Label: servedLandscapeRepo}
	model := &servedInvestmentModel{}
	now := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter:  contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:        &servedLandscapeGraph{t: t, subject: subject},
		Facts:        registry,
		Requirements: registry,
		Synthesizer:  contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "served-investment", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}},
		Results:      memoryinvestigation.NewStore(),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{
		ServiceVersion: "served-investment", Now: func() time.Time { return now }, NewResultID: func() string { return "result_served_investment" },
		InvestmentTreemapEnabled: true, FactCapabilities: registry.Capabilities(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_served_investment", Question: "Where did investment go in acme/allowed over the last 90 days?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, RequestedScope: contextfabric.RequestedScope{RepositorySlugs: []string{servedLandscapeRepo}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "served-investment", Version: "1.0.0", Surface: "workbench"}}
	result, err := engine.Investigate(ctx, storage.Principal{OrgID: orgID, RepositoryScopes: []string{servedLandscapeRepo}}, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v", err)
	}
	if model.draftErr != nil {
		t.Fatalf("the draft does not validate: %v", model.draftErr)
	}
	if result.AnswerPlan == nil || result.AnswerPlan.Family != contextfabric.QuestionFamilyInvestmentAllocation {
		t.Fatalf("plan %+v, want investment_allocation", result.AnswerPlan)
	}
	// The state run 2 was in: several claims, each carrying the one breakdown.
	carriers := map[string]struct{}{}
	for _, claim := range result.ClaimedFacts {
		if claim.Kind == contextfabric.FactInvestment && claim.Table != nil && claim.Table.Field == "theme_breakdown" && len(claim.Rows) == 5 {
			carriers[claim.ClaimID] = struct{}{}
		}
	}
	if len(carriers) != len(investmentTreemapFields) {
		t.Fatalf("%d claims carry the breakdown, want %d (claimed %v; claims %+v; limitations %v)", len(carriers), len(investmentTreemapFields), model.claimed, result.ClaimedFacts, result.Limitations)
	}
	var treemaps []contractsv1.ContextFabricRenderShape
	for _, shape := range result.RenderShapes {
		if shape.Kind == contractsv1.ContextFabricRenderKindTreemap {
			treemaps = append(treemaps, shape)
		}
	}
	if len(treemaps) != 1 {
		t.Fatalf("treemaps %d, want 1; shapes %+v", len(treemaps), result.RenderShapes)
	}
	points := treemaps[0].Series[0].Points
	if len(points) != 5 {
		t.Fatalf("points %d, want the 5 themes", len(points))
	}
	first := result.ClaimedFacts[0].ClaimID
	// Each unit weighs 1 in the mix: 1 feature, 0.5 maintenance, 0.5 quality.
	want := map[string]float64{"Feature Delivery": 0.5, "Maintenance / Tech Debt": 0.25, "Quality / Reliability": 0.25}
	for _, point := range points {
		if point.Source.ClaimID != first {
			t.Fatalf("point %+v cites %q, want the first claim %q", point, point.Source.ClaimID, first)
		}
		if share, named := want[point.Label]; named && point.Value != share {
			t.Fatalf("point %s = %v, want %v", point.Label, point.Value, share)
		}
		if _, named := want[point.Label]; !named && point.Value != 0 {
			t.Fatalf("point %s = %v, want a measured 0", point.Label, point.Value)
		}
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served result does not validate: %v", err)
	}
}
