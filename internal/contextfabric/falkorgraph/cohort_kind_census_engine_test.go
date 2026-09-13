package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// subjectlessGraphReader is the real adapter with subject resolution returning
// nothing committed, the resolution a population survey with no named member
// reaches. DiscoverContext, the census gate, the kind-scoped census and cohort
// assembly all run through the embedded *Adapter.
type subjectlessGraphReader struct {
	*Adapter
}

func (subjectlessGraphReader) ResolveSubjects(
	_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest, _ contextfabric.InterpretedQuestion,
	_ contextfabric.ResolvedGraphBinding, _ *contextfabric.ConfirmedExpectedKind, _ *contextfabric.ConfirmedAnchorSelection,
	_ *contextfabric.QuestionFrame, _ contextfabric.SubjectKind,
) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}},
		contextfabric.StructureOfferMaterial{}, nil, nil, nil
}

// TestInvestigateTermFreeSurveyServesTheDeclaredKindsCohort drives a whole
// investigation for every servable kind the exact-name census does not fetch:
// the frame declares that member kind, no member matches a term, and the served
// document carries the kind's population as its cohort.
func TestInvestigateTermFreeSurveyServesTheDeclaredKindsCohort(t *testing.T) {
	t.Parallel()
	for _, kind := range uncensusedServableKinds(t) {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			store := &kindCensusStore{population: map[string]int{string(kind): 3, "team": 2, "repository": 1}}
			adapter := newFakeAdapterWithTelemetry(t, store.conn(t), &recordingTelemetry{})
			derived := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
				Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
				SubjectExpression: contextfabric.SubjectExpression{
					Kind:       contextfabric.SubjectExpressionDiscoveredKind,
					Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: kind},
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
				NewResultID:    func() string { return "result_56540001" },
			})
			if err != nil {
				t.Fatalf("NewEngine() error = %v", err)
			}
			request := contextfabric.InvestigationRequest{
				SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_56540001",
				Question: "which ones need a look",
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
			if result.Cohort == nil {
				t.Fatalf("status %q: the served document carries no cohort for a term-free %s survey", result.Status, kind)
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
