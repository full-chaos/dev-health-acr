package contextfabric

import (
	"context"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"testing"
	"time"
)

func TestWorkItemFreshRetryEmitsNoRankDecision(t *testing.T) {
	for _, maxItems := range []int{50} {
		t.Run(fmt.Sprint(maxItems), func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			payload := workItemTuplePayloadFixture(t)
			members := []WorkItemMembershipMember{}
			for i := 0; i < 6; i++ {
				raw := fmt.Sprintf("work-%d", i)
				id, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", raw}, nil)
				if err != nil {
					t.Fatal(err)
				}
				members = append(members, WorkItemMembershipMember{CanonicalID: id, WorkItemID: raw})
			}
			calls := 0
			telemetry := &recordingTelemetry{}
			engine := budgetStageEngine(t, nil, 10, budgetStageOptions(maxItems, time.Second), &calls, telemetry)
			frame := ValidateFrame(prospectiveTupleFrame(GoalCountOrAggregate), nil, "").Frame
			question := InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}}
			outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), question.TimeContext), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
			engine.interpreter = familyInterpreter{interpreted: question, outcome: outcome}
			graph := &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
			engine.graph = graph
			engine.candidateVerifier = func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
				return true, ""
			}
			gate, _ := NewWorkItemMembershipGate(1, 0)
			engine.workItemMembership = tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, _ WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
				lease, err := gate.Acquire(ctx)
				return lease, WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: len(members)}, Members: members}, err
			})
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
			t.Logf("maxItems=%d calls=%d status=%s error=%v candidates=%d cohort=%d narrowing=%+v", maxItems, calls, result.Status, err, len(result.SubjectResolution.Candidates), cohortMemberCount(result.Cohort), telemetry.planNarrowings)
			if err != nil || result.Status != InvestigationComplete || cohortMemberCount(result.Cohort) != 3 {
				t.Fatalf("fitting retry result=%+v err=%v", result, err)
			}
			if calls != 2 {
				t.Fatalf("whole retry did not execute: synthesis calls=%d", calls)
			}
			if len(telemetry.cohortRanked) != 0 {
				t.Errorf("tuple published %d rank decisions", len(telemetry.cohortRanked))
			}
			if graph.discoverCalls != 0 {
				t.Error("tuple retry discovered graph")
			}
			if gate.Stats().InFlight != 0 {
				t.Error("tuple retry leaked lease")
			}
		})
	}
}
