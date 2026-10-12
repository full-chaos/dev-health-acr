package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func investigateProjectCohort(t *testing.T, outside MemberStatusOutsideSet, cut ...MemberStatusPoolCut) InvestigationResult {
	t.Helper()
	var poolCut MemberStatusPoolCut
	if len(cut) > 0 {
		poolCut = cut[0]
	}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:alpha", Label: "Alpha"}
	cohort := &Cohort{
		Kind:      SubjectProject,
		Rationale: "kind census match",
		Members:   []CohortMember{{Subject: project, Rank: 1, InclusionReasons: []string{"matched"}, EvidenceRefIDs: []string{"evidence_project_roster"}}},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "projects_under_pressure",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactHealth}},
	}
	graph := graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage:               Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			MemberStatusOutsideSet: outside,
			MemberStatusPoolCut:    poolCut,
		},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, _ CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{{Kind: FactHealth, Subject: project, Fields: map[string]FactValue{"severity": StringFactValue("high")}}},
				Coverage: Coverage{
					Sources:         []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}},
					DegradedReasons: []string{},
				},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, _ SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status:              InvestigationPartial,
				DirectJudgment:      "This investigation is partial: some canonical or graph coverage was unavailable.",
				CurrentState:        "Current observed values: health.severity=high for Alpha.",
				DeterministicAnswer: "This investigation is partial: some canonical or graph coverage was unavailable.",
				StrongestPressures:  []string{},
				Drivers:             []DriverJudgment{},
				RemainingWork:       []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: &resultStoreStub{},
	}, EngineOptions{ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(300, 0).UTC() }, NewResultID: func() string { return "result_member_status_01" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_member_status_01"
	request.Question = "which projects are completed?"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

// TestServedProjectAnswerStatesTheMembersOutsideTheKnownSet drives a project
// status question through the engine's full finalize: the graph read reports
// 3 projects whose stored state is outside the set, and the served answer
// carries that count in its limitations; a read that reports none serves no
// such sentence.
func TestServedProjectAnswerStatesTheMembersOutsideTheKnownSet(t *testing.T) {
	t.Parallel()
	served := investigateProjectCohort(t, MemberStatusOutsideSet{Kind: SubjectProject, Count: 3})
	want := contractsv1.ContextFabricMemberStatusOutsideSetLimitation("project", 3)
	count := 0
	for _, limitation := range served.Limitations {
		if limitation == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("served limitations = %q, want exactly one %q", served.Limitations, want)
	}
	none := investigateProjectCohort(t, MemberStatusOutsideSet{Kind: SubjectProject, Count: 0})
	for _, limitation := range none.Limitations {
		if contractsv1.IsContextFabricMemberStatusLimitation(limitation) {
			t.Fatalf("a read with no member outside the set served %q", limitation)
		}
	}
}

// TestServedAnswerStatesWithinHowManyMembersACutStatusFilterRan: a status
// decided within a cut read states how many members it checked, once; a read
// that was not cut states nothing of the kind.
func TestServedAnswerStatesWithinHowManyMembersACutStatusFilterRan(t *testing.T) {
	t.Parallel()
	served := investigateProjectCohort(t, MemberStatusOutsideSet{}, MemberStatusPoolCut{Kind: SubjectIncident, Read: 25})
	want := contractsv1.ContextFabricMemberStatusPoolCutLimitation("incident", 25)
	count := 0
	for _, limitation := range served.Limitations {
		if limitation == want {
			count++
		}
	}
	if want == "" || count != 1 || !contractsv1.IsContextFabricMemberStatusLimitation(want) {
		t.Fatalf("served limitations = %q, want exactly one %q", served.Limitations, want)
	}
	none := investigateProjectCohort(t, MemberStatusOutsideSet{}, MemberStatusPoolCut{Kind: SubjectIncident})
	for _, limitation := range none.Limitations {
		if contractsv1.IsContextFabricMemberStatusLimitation(limitation) {
			t.Fatalf("a read that was not cut served %q", limitation)
		}
	}
}
