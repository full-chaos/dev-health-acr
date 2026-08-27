package contextfabric

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestChaos4347_ComposeStatusCategoryRequirements is the direct unit-level
// pin for composeStatusCategoryRequirements, mirroring twoTurnCommittedWrong/
// twoTurnMutationProbe's own precedent (chaos3742_two_turn_confirmation_test.go)
// of testing the pure decision function independent of the full Investigate()
// plumbing. This is RED on origin/main before CHAOS-4347: main has no
// composeStatusCategoryRequirements at all, so a bare FactStatus requirement
// for a repository or team subject passes straight through unexpanded and
// prunes downstream (CHAOS-4344 case 23's own root cause).
func TestChaos4347_ComposeStatusCategoryRequirements(t *testing.T) {
	t.Parallel()

	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:r1"}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:t1"}
	workItem := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item:w1"}

	sortedKinds := func(kinds []FactKind) []FactKind {
		out := append([]FactKind(nil), kinds...)
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}

	cases := []struct {
		name             string
		requirements     []FactRequirement
		subjects         []SubjectRef
		wantKinds        []FactKind
		wantCompositions []CategoryFactCompositionEvent
	}{
		{
			name:         "repository subject: status composes to metrics+health+identity",
			requirements: []FactRequirement{{Kind: FactStatus}},
			subjects:     []SubjectRef{repo},
			wantKinds:    []FactKind{FactHealth, FactIdentity, FactMetrics},
			wantCompositions: []CategoryFactCompositionEvent{
				{RequirementKind: FactStatus, SubjectKind: SubjectRepository, ComposedKinds: []FactKind{FactMetrics, FactHealth, FactIdentity}},
			},
		},
		{
			name:         "team subject: status composes to health+workload+readiness+investment",
			requirements: []FactRequirement{{Kind: FactStatus}},
			subjects:     []SubjectRef{team},
			wantKinds:    []FactKind{FactHealth, FactInvestment, FactReadiness, FactWorkload},
			wantCompositions: []CategoryFactCompositionEvent{
				{RequirementKind: FactStatus, SubjectKind: SubjectTeam, ComposedKinds: []FactKind{FactHealth, FactWorkload, FactReadiness, FactInvestment}},
			},
		},
		{
			name:             "work_item subject: unchanged, no composition event",
			requirements:     []FactRequirement{{Kind: FactStatus}},
			subjects:         []SubjectRef{workItem},
			wantKinds:        []FactKind{FactStatus},
			wantCompositions: nil,
		},
		{
			name:         "mixed repository + work_item: BOTH the composed set AND the original FactStatus survive",
			requirements: []FactRequirement{{Kind: FactStatus}},
			subjects:     []SubjectRef{repo, workItem},
			wantKinds:    []FactKind{FactHealth, FactIdentity, FactMetrics, FactStatus},
			wantCompositions: []CategoryFactCompositionEvent{
				{RequirementKind: FactStatus, SubjectKind: SubjectRepository, ComposedKinds: []FactKind{FactMetrics, FactHealth, FactIdentity}},
			},
		},
		{
			name:             "non-status requirement: passes through untouched, no composition event",
			requirements:     []FactRequirement{{Kind: FactReadiness}},
			subjects:         []SubjectRef{repo},
			wantKinds:        []FactKind{FactReadiness},
			wantCompositions: nil,
		},
		{
			name:         "requirement's OWN Subjects (non-empty) wins over the investigation-wide set",
			requirements: []FactRequirement{{Kind: FactStatus, Subjects: []SubjectRef{team}}},
			subjects:     []SubjectRef{repo}, // investigation-wide set is repository, but the requirement pins team
			wantKinds:    []FactKind{FactHealth, FactInvestment, FactReadiness, FactWorkload},
			wantCompositions: []CategoryFactCompositionEvent{
				{RequirementKind: FactStatus, SubjectKind: SubjectTeam, ComposedKinds: []FactKind{FactHealth, FactWorkload, FactReadiness, FactInvestment}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingTelemetry{}
			e := &Engine{telemetry: rec}
			got := e.composeStatusCategoryRequirements(context.Background(), storage.Principal{OrgID: "org_1"}, tc.requirements, tc.subjects)

			var gotKinds []FactKind
			for _, requirement := range got {
				gotKinds = append(gotKinds, requirement.Kind)
			}
			if !reflect.DeepEqual(sortedKinds(gotKinds), sortedKinds(tc.wantKinds)) {
				t.Fatalf("composed kinds = %v, want %v (full requirements: %#v)", gotKinds, tc.wantKinds, got)
			}
			if !reflect.DeepEqual(rec.categoryFactCompositions, tc.wantCompositions) {
				t.Fatalf("categoryFactCompositions = %#v, want %#v", rec.categoryFactCompositions, tc.wantCompositions)
			}
		})
	}
}

// TestChaos4347_ComposeStatusCategoryRequirements_EmptyPassesThrough pins the
// nil-input short-circuit separately from the table above (a nil/empty
// requirements slice must never fail on a nil e.telemetry either, unlike
// every case above which sets one).
func TestChaos4347_ComposeStatusCategoryRequirements_EmptyPassesThrough(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	got := e.composeStatusCategoryRequirements(context.Background(), storage.Principal{OrgID: "org_1"}, nil, []SubjectRef{{Kind: SubjectRepository, CanonicalID: "repository:r1"}})
	if got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
}

// TestAcceptanceRepositoryStatusQuestionPullsComposedFacts (CHAOS-4347) is
// the planner-level acceptance pin team-lead's own ruling asked for: a
// repository "status" question -- the SAME FactRequirements: [{Kind:
// FactStatus}] shape bootstrapInterpretation() already uses for a project,
// just with a repository as the committed subject instead -- must reach the
// fact reader with the COMPOSED requirement set (metrics+health+identity),
// never a bare, prune-bound FactStatus. RED on origin/main: before
// CHAOS-4347, interpretation.FactRequirements passes straight into
// mergeFactRequirements unexpanded, so this test's captured request would
// carry a bare FactStatus requirement instead.
func TestAcceptanceRepositoryStatusQuestionPullsComposedFacts(t *testing.T) {
	t.Parallel()
	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:r1", Label: "full-chaos/action-runners-local"}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{repo}},
		context: GraphContext{
			DriverCandidates: []DriverJudgment{}, EvidenceRefIDs: []string{},
			FactRequirements: []FactRequirement{},
			Coverage:         Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	var capturedKinds []FactKind
	facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		for _, requirement := range request.Requirements {
			capturedKinds = append(capturedKinds, requirement.Kind)
		}
		return CanonicalFactBundle{
			Facts:    []CanonicalFact{},
			Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}, {Source: "canonical_fact:health", State: SourceAvailable}, {Source: "canonical_fact:identity", State: SourceAvailable}}, DegradedReasons: []string{}},
			Version:  "acceptance-v1",
		}, nil
	})
	interpretation := InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "status",
		SubjectTerms: []string{"action-runners-local"}, TimeContext: TimeContext{Axis: TemporalCurrent},
		// The model's own 1:1 pick from the closed FactKind vocabulary --
		// exactly what a real Interpret() call returns for a "status of
		// <repository>" question, since it has no way to know FactStatus is
		// work_item-only.
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	draft := SynthesisDraft{
		Status: InvestigationDegraded, DirectJudgment: "The repository shows steady activity with no open incidents.",
		CurrentState:       "Metrics and health facts available; no discrete status field exists for a repository.",
		StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
		Limitations: []string{"placeholder"}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{},
		DeterministicAnswer: "model prose placeholder, discarded by server composition", Warnings: []string{},
	}
	results := newMapResultStore()
	runtime := fakeModelRuntime{interpreted: interpretation, draft: draft, receipt: acceptanceReceipt()}
	rec := &recordingTelemetry{}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
		Results:     results,
		Telemetry:   rec,
	}, EngineOptions{
		ServiceVersion: "acceptance-test",
		Now:            func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_chaos4347_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = "What is the status of the action-runners-local repository?"
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status == InvestigationNoMatch {
		t.Fatalf("Status = no_match, want an answer-bearing status (composed facts were available): %#v", result)
	}

	wantKinds := []FactKind{FactHealth, FactIdentity, FactMetrics}
	gotSorted := append([]FactKind(nil), capturedKinds...)
	sort.Slice(gotSorted, func(i, j int) bool { return gotSorted[i] < gotSorted[j] })
	if !reflect.DeepEqual(gotSorted, wantKinds) {
		t.Fatalf("fact requirements reaching the reader = %v, want the composed set %v (a bare FactStatus here is the CHAOS-4344 case-23 defect)", capturedKinds, wantKinds)
	}
	if len(rec.categoryFactCompositions) != 1 || rec.categoryFactCompositions[0].SubjectKind != SubjectRepository {
		t.Fatalf("categoryFactCompositions = %#v, want exactly one repository composition event", rec.categoryFactCompositions)
	}
}
