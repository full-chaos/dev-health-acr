package falkorgraph

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestInvestigateOrganizationScopeCountReadsTheKindScopedCensusOnlyWhenItCounts
// drives a whole investigation for every servable kind the exact-name census
// does not fetch, framed as an organization scope declaring that member kind.
// Counting it, the frame-level admission lets the kind-scoped census choose that
// kind, and the served cohort is that kind's population. With no count goal the
// same frame is not admitted, so no kind-scoped query runs and no cohort is
// served.
func TestInvestigateOrganizationScopeCountReadsTheKindScopedCensusOnlyWhenItCounts(t *testing.T) {
	t.Parallel()
	for _, kind := range uncensusedServableKinds(t) {
		for _, counted := range []bool{true, false} {
			kind, counted := kind, counted
			goals := []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate}
			if !counted {
				goals = []contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey, contextfabric.GoalAllocateInvestment}
			}
			t.Run(fmt.Sprintf("%s/counted=%v", kind, counted), func(t *testing.T) {
				t.Parallel()
				store := &kindCensusStore{population: map[string]int{string(kind): 3, "team": 2, "repository": 1}}
				adapter := newFakeAdapterWithTelemetry(t, store.conn(t), &recordingTelemetry{})
				derived := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
					Goals: goals,
					SubjectExpression: contextfabric.SubjectExpression{
						Kind: contextfabric.SubjectExpressionOrganizationScope,
						Org:  &contextfabric.OrganizationScopeExpression{MemberKind: &kind},
					},
					Temporal: contextfabric.TemporalIntentCurrent,
					Version:  contextfabric.QuestionFrameVersion,
				}, nil)
				engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
					Interpreter: framedInterpreter{
						interpreted: contextfabric.InterpretedQuestion{
							Shape: contextfabric.ShapeDiscoveredCohort, RequestedJudgment: "attention",
							TimeContext:      contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
							FactRequirements: []contextfabric.FactRequirement{},
						},
						frame: &derived,
					},
					Graph:        subjectlessGraphReader{Adapter: adapter},
					Facts:        emptyFactReader{},
					Synthesizer:  countingSynthesizer{},
					Results:      discardingResultStore{},
					Requirements: productionRequirementDeriver{},
				}, contextfabric.EngineOptions{
					ServiceVersion: "acr-test",
					NewResultID:    func() string { return "result_56410001" },
				})
				if err != nil {
					t.Fatalf("NewEngine() error = %v", err)
				}
				request := contextfabric.InvestigationRequest{
					SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_56410001",
					Question: "how many are there across the organization",
					TimeContext: contextfabric.TimeContext{
						Axis:           contextfabric.TemporalCurrent,
						EvidenceWindow: &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D},
					},
					Options: contextfabric.InvestigationOptions{
						MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50,
						MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
					},
					Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
				}

				result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
				if err != nil {
					t.Fatalf("Investigate() error = %v", err)
				}
				if !counted {
					if queries := store.singleKindQueries(kind); len(queries) != 0 {
						t.Errorf("an organization scope with no count goal ran %d kind-scoped census queries for %s; it is not admitted, so it must run none", len(queries), kind)
					}
					if result.Cohort != nil {
						t.Errorf("an organization scope with no count goal served a %s cohort of %d members; it is not admitted", result.Cohort.Kind, len(result.Cohort.Members))
					}
					return
				}
				if result.Cohort == nil {
					t.Fatalf("status %q: the served document carries no cohort for an organization scope counting %s", result.Status, kind)
				}
				if result.Cohort.Kind != kind {
					t.Errorf("served cohort kind = %q, want %q", result.Cohort.Kind, kind)
				}
				want := map[string]bool{}
				for i := 0; i < 3; i++ {
					want[kindCensusMemberID(string(kind), i)] = true
				}
				got := memberIDs(result.Cohort)
				if len(got) != len(want) {
					t.Fatalf("served members = %v, want the %d projected %s members", got, len(want), kind)
				}
				for _, id := range got {
					if !want[id] {
						t.Errorf("served member %q is not one of the projected %s members", id, kind)
					}
				}
				if len(store.singleKindQueries(kind)) == 0 {
					t.Error("the served cohort did not come from the kind-scoped census")
				}
			})
		}
	}
}
