package falkorgraph

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12373: a routed cohort question, through the engine and the real
// adapter on a real FalkorDB, with no model (client synthesis, a scripted
// interpreter). A project narrowing that leaves the cohort's members out never
// reaches graph_cohort_denied_by_authorization; a real grant refusal on the
// same route still does.
func TestLiveACohortQuestionNeverDisclosesAProjectNarrowingAsAuthorization(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	assertCohortQuestionEndToEnd(t, ctx, adapter)
}

// cohortQuestionInterpreter reads "the <member kind> of team Platform".
type cohortQuestionInterpreter struct {
	member contextfabric.SubjectKind
}

func (i cohortQuestionInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:   contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"Platform"}, MemberKind: i.member},
		},
		Temporal: contextfabric.TemporalIntentCurrent, Version: contextfabric.QuestionFrameVersion,
	}, nil)
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "members",
		SubjectTerms: []string{"Platform"}, TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		FactRequirements: []contextfabric.FactRequirement{},
	}, contextfabric.QuestionFamilyOutcome{
		Frame: &frame, FrameObligations: frame.Obligations,
		Family: contextfabric.QuestionFamilyScopedCohortStatus, Source: contextfabric.QuestionFamilySourceModel,
		Gate:          contextfabric.DecideFrameGate(contextfabric.ValidateFrame(frame, nil, ""), true),
		WinningSample: contextfabric.FamilySample{ScopeAnchorKind: contextfabric.SubjectTeam, ScopeAnchorTerm: "Platform"},
	}, nil
}

func cohortDeniedDetail(result contextfabric.InvestigationResult) bool {
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization {
			return true
		}
	}
	return false
}

func assertCohortQuestionEndToEnd(t *testing.T, ctx context.Context, adapter *Adapter) {
	t.Helper()
	orgID := "live-12373-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	// The production team node carries the repositories it owns
	// (devhealthsource teams_projects.go).
	teamScope := contextfabric.AuthorizationScope{TeamIDs: []string{"platform"}, RepositorySlugs: []string{"acme/api", "acme/secret"}}
	entities, relationships := newLiveMemberFixture(orgID, time.Now().UTC(), func(add func(contextfabric.SubjectRef, contextfabric.AuthorizationScope, string), relate func(string, contractsv1.ContextFabricRelationshipType, contextfabric.SubjectRef, contextfabric.SubjectRef, contextfabric.AuthorizationScope, string)) {
		add(team, teamScope, "")
		for _, id := range []string{"proj-a", "proj-b"} {
			p := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:" + id, Label: id}
			add(p, contextfabric.AuthorizationScope{ProjectIDs: []string{id}}, "")
			relate(id+"_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, p, team, contextfabric.AuthorizationScope{ProjectIDs: []string{id}, TeamIDs: []string{"platform"}}, "")
		}
		// A project of the organization the team does not own: the narrowing
		// resolves completely, and leaves every member of the cohort out.
		add(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:proj-other", Label: "proj-other"}, contextfabric.AuthorizationScope{ProjectIDs: []string{"proj-other"}}, "")
		for _, slug := range []string{"acme/api", "acme/secret"} {
			r := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:github:" + slug, Label: slug}
			add(r, contextfabric.AuthorizationScope{RepositorySlugs: []string{slug}}, "")
			relate(slug+"_owner", contractsv1.ContextFabricRelationshipOwnedByTeam, r, team, contextfabric.AuthorizationScope{RepositorySlugs: []string{slug}, TeamIDs: []string{"platform"}}, "")
		}
	}, nil)
	for _, batch := range []contextfabric.ProjectionBatch{entities, relationships} {
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batch.BatchID, err)
		}
	}
	telemetry := &recordingTelemetry{}
	adapter.config.Telemetry = telemetry
	ask := func(t *testing.T, principal storage.Principal, member contextfabric.SubjectKind, scope contextfabric.RequestedScope) contextfabric.InvestigationResult {
		t.Helper()
		telemetry.teamAnchorWalks = nil
		engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
			Interpreter: cohortQuestionInterpreter{member: member}, Graph: adapter, Facts: emptyFactReader{},
			Synthesizer: contextfabric.RuntimeAnswerSynthesizer{Runtime: routeNoModelRuntime{t: t}, Options: contextfabric.RuntimeAnswerSynthesizerOptions{Backend: "test", ProjectionVersion: "p", QueryVersion: "q"}, ClientSynthesis: synthesisprompt.ClientAssembly()},
			Results:     discardingResultStore{}, Requirements: productionRequirementDeriver{},
		}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_12373001" }})
		if err != nil {
			t.Fatal(err)
		}
		runCtx, _ := contextfabric.WithSynthesisInputCollector(ctx)
		result, err := engine.Investigate(runCtx, principal, contextfabric.InvestigationRequest{
			SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12373001",
			Question: "what does team Platform own", SynthesisMode: contextfabric.SynthesisModeClient, RequestedScope: scope,
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}},
			Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true},
			Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		})
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		// The route is the point: the question reached the cohort walk.
		if len(telemetry.teamAnchorWalks) == 0 {
			t.Fatalf("no team anchor walk ran: status=%s coverage=%+v", result.Status, result.Coverage)
		}
		return result
	}
	t.Run("a project narrowing that leaves every member out", func(t *testing.T) {
		result := ask(t, storage.Principal{OrgID: orgID}, contextfabric.SubjectProject, contextfabric.RequestedScope{ProjectIDs: []string{"proj-other"}})
		walk := telemetry.teamAnchorWalks[len(telemetry.teamAnchorWalks)-1]
		t.Logf("status=%s walk=%+v details=%+v", result.Status, walk, result.Coverage.Details)
		if cohortDeniedDetail(result) {
			t.Fatalf("a scope exclusion is disclosed as authorization: %+v", result.Coverage.Details)
		}
		if walk.Denied != 0 || walk.ProjectExcluded != 2 || walk.Outcome != TeamAnchorWalkNoMembers {
			t.Fatalf("walk decision = %+v, want no_members, denied 0, project_excluded 2", walk)
		}
	})
	t.Run("the control: a real grant refusal on the same route", func(t *testing.T) {
		result := ask(t, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/api"}}, contextfabric.SubjectRepository, contextfabric.RequestedScope{})
		walk := telemetry.teamAnchorWalks[len(telemetry.teamAnchorWalks)-1]
		t.Logf("status=%s walk=%+v details=%+v", result.Status, walk, result.Coverage.Details)
		if walk.Denied == 0 {
			t.Fatalf("premise: the grants hid nothing: walk %+v", walk)
		}
		if !cohortDeniedDetail(result) {
			t.Fatalf("a real grant refusal lost its disclosure: %+v", result.Coverage.Details)
		}
	})
}
