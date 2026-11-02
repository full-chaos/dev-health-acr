package devhealthfacts_test

// CHAOS-12585: reach t9 on askdev-1 asked "Where is our organization investing
// over the last 90 days?" with the frame {allocate_investment} over
// organization_scope (temporal bounded_window), and was served
// discovered_cohort_ranking, an investment read refused as repository-bound,
// no claim and no chart. This runs that question through the real
// Engine.Investigate over a real ClickHouse with the production registry, for a
// repository-bound caller (as t9's web user was) and a full caller. Only the
// graph and the model are controlled: the graph commits nothing, as on askdev-1,
// and the model claims what the synthesis input offers.

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

// organizationQuestionGraph commits no subject, as t9's resolution did.
type organizationQuestionGraph struct{ servedLandscapeGraph }

func (*organizationQuestionGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}}, contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

// organizationQuestionModel interprets t9's frame and claims every theme share
// of the organization's investment fact, as a model reads it.
type organizationQuestionModel struct {
	goals    []contextfabric.InvestigationGoal
	offered  int
	draftErr error
}

func (m *organizationQuestionModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := freshLiveReceipt(contextfabric.ModelOperationInterpret)
	receipt.QuestionFrame = &contextfabric.QuestionFrame{
		Goals:             m.goals,
		SubjectExpression: contextfabric.SubjectExpression{Kind: contextfabric.SubjectExpressionOrganizationScope, Org: &contextfabric.OrganizationScopeExpression{}},
		Temporal:          contextfabric.TemporalIntentBoundedWindow,
	}
	return contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeOpen, RequestedJudgment: "Where the organization is investing", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactInvestment}}}, receipt, nil
}

func (m *organizationQuestionModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	text := "Where the organization's effort went."
	draft := contextfabric.SynthesisDraft{Status: contextfabric.InvestigationComplete, DirectJudgment: text, CurrentState: text, DeterministicAnswer: text, StrongestPressures: []string{}, RemainingWork: []contextfabric.Finding{}, ReadinessGaps: []contextfabric.Finding{}, Conflicts: []contextfabric.Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, Warnings: []string{}, Drivers: []contextfabric.DriverJudgment{}, ClaimedFacts: []contextfabric.ClaimedFact{}}
	for _, fact := range input.Facts.Facts {
		if fact.Kind != contextfabric.FactInvestment || fact.Subject.Kind != contextfabric.SubjectOrganization {
			continue
		}
		m.offered++
		var ids []string
		for _, field := range []string{"theme_feature_delivery", "theme_maintenance", "theme_quality", "theme_operational", "theme_risk"} {
			value, scalar := scalarOfFactValue(fact.Fields[field])
			if !scalar {
				continue
			}
			id := "claim_organization_" + field
			ids = append(ids, id)
			draft.ClaimedFacts = append(draft.ClaimedFacts, contextfabric.ClaimedFact{ClaimID: id, Kind: fact.Kind, Subject: fact.Subject, Field: field, Value: value, FactID: fact.ID})
		}
		draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, fact.EvidenceRefIDs...)
		draft.Drivers = append(draft.Drivers, contextfabric.DriverJudgment{
			DriverID: "driver_organization_1", Standing: contextfabric.DriverPrincipal, Category: "narrative",
			Title: "Investment", Summary: text,
			AffectedSubjects: []contextfabric.SubjectRef{fact.Subject}, EvidenceRefIDs: append([]string(nil), fact.EvidenceRefIDs...),
			ClaimedFactIDs: ids,
			Derivation:     contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved, Confidence: 0.9, Current: true,
		})
		break
	}
	if len(draft.ClaimedFacts) == 0 {
		draft.Status = contextfabric.InvestigationPartial
		draft.Limitations = []string{"No organization investment fact was found."}
	}
	m.draftErr = draft.ValidateAgainst(input)
	return draft, freshLiveReceipt(contextfabric.ModelOperationSynthesize), nil
}

func serveOrganizationQuestion(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, principal storage.Principal, goals ...contextfabric.InvestigationGoal) (contextfabric.InvestigationResult, *organizationQuestionModel) {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	model := &organizationQuestionModel{goals: goals}
	now := time.Now().UTC()
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: model, Requirements: registry},
		Graph:       &organizationQuestionGraph{servedLandscapeGraph{t: t}}, Facts: registry, Requirements: registry,
		Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: model, Options: contextfabric.RuntimeAnswerSynthesizerOptions{ServiceVersion: "organization-question", Backend: "graph", ProjectionVersion: "v1", QueryVersion: "v1"}},
		Results:     memoryinvestigation.NewStore(),
		CandidateVerifier: func(context.Context, storage.Principal, contextfabric.RequestedScope, contextfabric.ResolvedGraphBinding, contextfabric.SubjectKind, string) (bool, contextfabric.CandidateVerificationReason) {
			return true, contextfabric.CandidateVerificationValid
		},
	}, contextfabric.EngineOptions{ServiceVersion: "organization-question", Now: func() time.Time { return now }, NewResultID: func() string { return "result_organization_question" }, InvestmentTreemapEnabled: true, FactCapabilities: registry.Capabilities()})
	if err != nil {
		t.Fatal(err)
	}
	request := contextfabric.InvestigationRequest{SchemaVersion: contractsv1.ContextFabricInvestigationRequestSchema, RequestID: "request_organization_question", Question: "Where is our organization investing over the last 90 days?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}}, Options: contractsv1.ContextFabricInvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1048576, AllowClarification: true}, Consumer: contractsv1.ContextFabricConsumerInfo{Name: "organization-question", Version: "1.0.0", Surface: "workbench"}}
	result, err := engine.Investigate(ctx, principal, request)
	if err != nil {
		t.Fatalf("Engine.Investigate: %v", err)
	}
	if model.draftErr != nil {
		t.Fatalf("the draft does not validate: %v", model.draftErr)
	}
	return result, model
}

func organizationQuestionStore(t *testing.T, ctx context.Context, orgID string) contextpacket.ClickHouseQueryClient {
	t.Helper()
	rawQuery, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	at := time.Now().UTC().AddDate(0, 0, -5).Truncate(24 * time.Hour)
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("repo-org"), orgID, servedLandscapeRepo, "github", at); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []struct {
		id     string
		themes map[string]float64
	}{
		{"wu-org-feature", map[string]float64{"feature_delivery": 1}},
		{"wu-org-mixed", map[string]float64{"maintenance": 0.5, "quality": 0.5}},
	} {
		if err := direct.Exec(ctx, `INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			unit.id, at, at, repoUUID("repo-org"), 1.0, unit.themes, map[string]float64{}, `{"issues":[],"prs":[]}`, at, orgID); err != nil {
			t.Fatal(err)
		}
	}
	return statementthreads.API(rawQuery)
}

func TestAnOrganizationAllocationQuestionDrawsTheOrganizationTreemapForEveryCaller(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-organization-question"
	query := organizationQuestionStore(t, ctx, orgID)
	for name, principal := range map[string]storage.Principal{
		"repository-bound": {OrgID: orgID, RepositoryScopes: []string{servedLandscapeRepo}},
		"full":             {OrgID: orgID},
	} {
		result, model := serveOrganizationQuestion(t, ctx, query, orgID, principal, contextfabric.GoalAllocateInvestment)
		if result.AnswerPlan == nil || result.AnswerPlan.Family != contextfabric.QuestionFamilyInvestmentAllocation {
			t.Fatalf("%s: plan %+v, want investment_allocation", name, result.AnswerPlan)
		}
		if model.offered != 1 {
			t.Fatalf("%s: the model was offered %d organization investment facts, want 1; sources %+v", name, model.offered, result.Coverage.Sources)
		}
		claims := 0
		for _, claim := range result.ClaimedFacts {
			if claim.Kind == contextfabric.FactInvestment && claim.Subject.Kind == contextfabric.SubjectOrganization {
				claims++
			}
		}
		if claims != 5 {
			t.Fatalf("%s: %d organization investment claims, want 5: %+v", name, claims, result.ClaimedFacts)
		}
		var treemaps []contractsv1.ContextFabricRenderShape
		for _, shape := range result.RenderShapes {
			if shape.Kind == contractsv1.ContextFabricRenderKindTreemap {
				treemaps = append(treemaps, shape)
			}
		}
		if len(treemaps) != 1 || len(treemaps[0].Series[0].Points) != 5 {
			t.Fatalf("%s: treemaps %+v, want one of the 5 themes", name, treemaps)
		}
		if got := treemaps[0].ValueLabel; got != "Share of effort linked to a repository (ratio)" {
			t.Fatalf("%s: value label %q", name, got)
		}
		want := map[string]float64{"Feature Delivery": 0.5, "Maintenance / Tech Debt": 0.25, "Quality / Reliability": 0.25, "Operational / Support": 0, "Risk / Security": 0}
		for _, point := range treemaps[0].Series[0].Points {
			if share, named := want[point.Label]; !named || point.Value != share {
				t.Fatalf("%s: point %s = %v, want %v", name, point.Label, point.Value, share)
			}
		}
		for _, source := range result.Coverage.Sources {
			if source.Source == "canonical_fact:investment" && source.State == contextfabric.SourceNotApplicable {
				t.Fatalf("%s: investment source %+v: no caller is refused the organization fact", name, source)
			}
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("%s: the served result does not validate: %v", name, err)
		}
	}
}

// Check 10: a second goal keeps the precedence table, as before this change.
func TestAnOrganizationFrameWithASecondGoalKeepsItsFamilyInTheServedAnswer(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-organization-question-two-goals"
	query := organizationQuestionStore(t, ctx, orgID)
	result, _ := serveOrganizationQuestion(t, ctx, query, orgID, storage.Principal{OrgID: orgID}, contextfabric.GoalAllocateInvestment, contextfabric.GoalAssessState)
	if result.AnswerPlan != nil && result.AnswerPlan.Family == contextfabric.QuestionFamilyInvestmentAllocation {
		t.Fatalf("plan %+v: a second goal must keep the table's family", result.AnswerPlan)
	}
	for _, shape := range result.RenderShapes {
		if shape.Kind == contractsv1.ContextFabricRenderKindTreemap {
			t.Fatalf("a second goal drew %+v", shape)
		}
	}
}
