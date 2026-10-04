package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// An ambiguous anchor whose candidates carry their own evidence references
// ends in a clarification that names those references. The terminal commits no
// subject and reads no member, so it is saved as a prospective reading rather
// than validated as a retained-member payload.
func TestWorkItemAmbiguousAnchorTerminalWithCandidateEvidenceSaves(t *testing.T) {
	for _, name := range []string{"unqualified", "status"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			payload := workItemTuplePayloadFixture(t)
			project := payload.SubjectResolution.Candidates[0]
			project.State = ResolutionAmbiguous
			project.Confidence = 0.9
			project.EvidenceRefIDs = []string{"evidence:project:p1"}
			repo := SubjectCandidate{ReceiptID: "receipt-2", Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: "repo-1", Label: "Project"}, State: ResolutionAmbiguous, MatchedTerms: []string{"project"}, MatchReasons: []string{"exact"}, Confidence: 0.9, EvidenceRefIDs: []string{"evidence:repo:r1"}}
			resolution := SubjectResolution{Candidates: []SubjectCandidate{project, repo}, Committed: []SubjectRef{}}
			frame := prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate)
			if name == "status" {
				frame = statusQualifiedTupleFrame(MemberQualifierStatus, "in_progress")
			}
			frame = ValidateFrame(frame, nil, "").Frame
			store := &staticResultStore{results: map[string]InvestigationResult{}}
			outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
			engine, err := NewEngine(EngineDependencies{
				Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: outcome},
				Graph:       &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution}},
				CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
					return true, ""
				},
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return CanonicalFactBundle{}, nil
				}),
				Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
					return InvestigationResult{}, nil
				}),
				Results: store, Requirements: registryDeriver{},
			}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_ambiguous_anchor" }})
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
			if err != nil {
				t.Fatalf("an ambiguous-anchor clarification must be served, got %v", err)
			}
			if result.Status != InvestigationClarificationRequired || cohortMemberCount(result.Cohort) != 0 || len(result.SubjectResolution.Committed) != 0 {
				t.Fatalf("result = status %s members %d committed %d", result.Status, cohortMemberCount(result.Cohort), len(result.SubjectResolution.Committed))
			}
			if store.savedSemantic == nil || store.savedSemantic.State == nil || store.savedSemantic.State.Frame == nil {
				t.Fatal("the clarification was not saved with its reading, so a follow-up could not redeem its offers")
			}
		})
	}
}

func TestWorkItemPreMembershipTerminalExemptsOnlyTheCandidatesOwnEvidence(t *testing.T) {
	state := validWorkItemTupleSemanticState(t)
	build := func(mutate func(*InvestigationResult)) InvestigationResult {
		result := InvestigationResult{
			Status: InvestigationClarificationRequired,
			SubjectResolution: SubjectResolution{Candidates: []SubjectCandidate{
				{ReceiptID: "r1", Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "p1"}, State: ResolutionAmbiguous, EvidenceRefIDs: []string{"evidence:project:p1"}},
				{ReceiptID: "r2", Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: "r1"}, State: ResolutionAmbiguous, EvidenceRefIDs: []string{"evidence:repo:r1"}},
			}},
			EvidenceRefIDs:    []string{"evidence:project:p1", "evidence:repo:r1"},
			EvidenceRefLabels: map[string]string{"evidence:project:p1": "p"},
		}
		if mutate != nil {
			mutate(&result)
		}
		return result
	}
	for _, tc := range []struct {
		name   string
		mutate func(*InvestigationResult)
		want   bool
	}{
		{"candidates' own refs and labels", nil, true},
		{"no refs at all", func(r *InvestigationResult) { r.EvidenceRefIDs, r.EvidenceRefLabels = nil, nil }, true},
		{"a ref no candidate carries", func(r *InvestigationResult) { r.EvidenceRefIDs = append(r.EvidenceRefIDs, "evidence:work_item:w1") }, false},
		{"a label for a ref no candidate carries", func(r *InvestigationResult) { r.EvidenceRefLabels = map[string]string{"evidence:work_item:w1": "w"} }, false},
		{"a committed candidate", func(r *InvestigationResult) { r.SubjectResolution.Candidates[0].State = ResolutionCommitted }, false},
		{"a committed subject", func(r *InvestigationResult) {
			r.SubjectResolution.Committed = []SubjectRef{{Kind: SubjectProject, CanonicalID: "p1"}}
		}, false},
		{"a cohort", func(r *InvestigationResult) { r.Cohort = &Cohort{Kind: SubjectWorkItem} }, false},
		{"a claimed fact", func(r *InvestigationResult) { r.ClaimedFacts = []ClaimedFact{{ClaimID: "c"}} }, false},
		{"a direct judgment", func(r *InvestigationResult) { r.DirectJudgment = "answer" }, false},
		{"a completed status", func(r *InvestigationResult) { r.Status = InvestigationComplete }, false},
	} {
		if got := workItemTuplePreMembershipTerminal(BudgetAssertSubjectlessTerminal, build(tc.mutate), state); got != tc.want {
			t.Errorf("%s: exempt=%v, want %v", tc.name, got, tc.want)
		}
	}
	withCensus := *state
	withCensus.WorkItemCensus = &WorkItemTupleCensus{}
	if workItemTuplePreMembershipTerminal(BudgetAssertSubjectlessTerminal, build(nil), &withCensus) {
		t.Error("a result with a measured census was exempted")
	}
	if workItemTuplePreMembershipTerminal(BudgetAssertDecisive, build(nil), state) {
		t.Error("a decisive save was exempted")
	}
}
