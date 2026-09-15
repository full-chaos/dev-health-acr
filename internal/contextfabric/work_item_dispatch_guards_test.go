package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A mutant can remove a nil guard. Report that violation on its named test,
// while allowing the rest of the package's cases to complete.
func TestWorkItemGuardInitialGatePrecedence(t *testing.T) {
	for _, gate := range []FrameGate{
		{Outcome: FrameGateNotProposed},
		{Outcome: FrameGateRejectedInvalid, FailedInvariant: FrameInvariantI6},
		{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectDocument},
		{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortDiscoverability("future_basis"), DeclaredMemberKind: SubjectWorkItem},
	} {
		t.Run(gate.Observable()+string(gate.DeclaredMemberKind), func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			for _, goals := range [][]InvestigationGoal{{GoalAssessState}, {GoalExplainDrivers}} {
				frame := prospectiveTupleFrame(goals...)
				if got := workItemTupleFrameGate(gate, &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}); got != gate {
					t.Errorf("precedence changed: got=%+v want=%+v", got, gate)
				}
			}
		})
	}
}

func TestWorkItemGuardMembershipAdmissionOutcomes(t *testing.T) {
	for _, name := range []string{"measured", "no_port", "read_error", "no_lease", "not_measured", "unmeasured_state", "cancelled", "no_owner", "completed_owner", "invalid_identity", "invalid_census", "fallback_label"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			payload := workItemTuplePayloadFixture(t)
			gate, _ := NewWorkItemMembershipGate(1, 0)
			ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
			defer owner.Complete()
			cancelCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			ctx = cancelCtx
			if name == "no_owner" {
				ctx = context.Background()
			}
			if name == "completed_owner" {
				owner.Complete()
			}
			var lease *WorkItemMembershipLease
			engine := &Engine{workItemMembership: tupleMembershipFunc(func(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
				var err error
				lease, err = gate.Acquire(context.Background())
				if err != nil {
					return nil, WorkItemMembershipResult{}, err
				}
				m := WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, Members: []WorkItemMembershipMember{{CanonicalID: payload.Cohort.Members[0].Subject.CanonicalID, WorkItemID: "work-1"}}}
				switch name {
				case "read_error":
					return lease, m, errors.New("controlled S1 error")
				case "no_lease":
					lease.Release()
					return nil, m, nil
				case "not_measured":
					m.Census.PopulationMeasured = false
				case "unmeasured_state":
					m.Census.State = WorkItemMembershipCensusUnmeasured
				case "cancelled":
					cancel()
				case "invalid_identity":
					m.Members[0].CanonicalID = "invalid"
				case "invalid_census":
					m.Census.AuthorizedPopulation = -1
				case "fallback_label":
					m.Members[0].WorkItemID = ""
				}
				return lease, m, nil
			})}
			if name == "no_port" {
				engine.workItemMembership = nil
			}
			graph, census, err := engine.discoverWorkItemTuple(ctx, storage.Principal{OrgID: "org-1"}, InvestigationRequest{RequestedScope: RequestedScope{RepositorySlugs: []string{"org/b", "org/a", "org/a"}}}, payload.SubjectResolution, &AnswerPlan{})
			wantError := name == "cancelled" || name == "no_owner" || name == "completed_owner" || name == "invalid_identity" || name == "invalid_census"
			if (err != nil) != wantError {
				t.Fatalf("error=%v wantError=%v", err, wantError)
			}
			if !wantError {
				measured := name == "measured" || name == "fallback_label"
				if (graph.Cohort != nil) != measured {
					t.Fatalf("cohort=%+v measured=%v", graph.Cohort, measured)
				}
				if !reflect.DeepEqual(census.RequestedRepositoryScope, []string{"org/b", "org/a", "org/a"}) {
					t.Errorf("raw scope changed: %v", census.RequestedRepositoryScope)
				}
				if !measured && (census.State != WorkItemMembershipCensusUnmeasured || census.Value != 0 || census.Retained != 0) {
					t.Errorf("unmeasured census=%+v", census)
				}
				if name == "fallback_label" && graph.Cohort.Members[0].Subject.Label != graph.Cohort.Members[0].Subject.CanonicalID {
					t.Error("missing member label lost ID fallback")
				}
			}
			owner.Complete()
			if lease != nil {
				lease.Release()
			}
			if gate.Stats().InFlight != 0 {
				t.Error("completed response leaked permit")
			}
		})
	}
}

func TestWorkItemGuardFloorFormattingPreservesOtherKinds(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tc := range []struct {
		kind       SubjectKind
		incomplete bool
		want       string
	}{
		{SubjectWorkItem, true, "Counted at least 2000 work items."},
		{SubjectWorkItem, false, "Counted 2000 work items."},
		{SubjectTeam, true, "Counted 2000 teams."},
	} {
		got := cardinalityAnswerSentence(MembershipCardinality{Resolved: true, Kind: tc.kind, Served: 2000, Declared: 2000, PopulationIncomplete: tc.incomplete})
		if got != tc.want {
			t.Errorf("sentence=%q want=%q", got, tc.want)
		}
	}
}

func TestWorkItemGuardCapNarrowingSeparateFromPopulation(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tc := range []struct{ plan, request, population, retained, after int }{{234, 250, 201, 200, 200}, {234, 250, 2000, 200, 200}, {234, 7, 201, 7, 7}, {234, 250, 1, 1, 200}} {
		plan := &AnswerPlan{Budget: AnswerPlanBudget{MaxMembers: tc.plan}}
		telemetry := &recordingTelemetry{}
		engine := &Engine{telemetry: telemetry}
		engine.workItemTupleNarrowing(context.Background(), storage.Principal{OrgID: "org-1"}, plan, tc.request, &WorkItemTupleCensus{Value: tc.population, Retained: tc.retained})
		found := false
		for _, step := range plan.Narrowing {
			if step.Before == tc.plan && step.After == tc.after && step.Basis == contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
				found = true
			}
		}
		if !found {
			t.Errorf("cap %d to %d absent: %+v", tc.plan, tc.after, plan.Narrowing)
		}
		if len(telemetry.planNarrowings) != len(plan.Narrowing) {
			t.Errorf("cap telemetry=%d steps=%d", len(telemetry.planNarrowings), len(plan.Narrowing))
		}
	}
}

func TestWorkItemGuardRealProjectAnchorRole(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	frame := prospectiveTupleFrame(GoalAssessState)
	for _, sampled := range []SubjectKind{"", SubjectProject, SubjectTeam, SubjectOrganization} {
		slots := answerabilityReadingOf(&frame, sampled).slots()
		if len(slots) != 2 || slots[0].Kind != SubjectProject || slots[1].Kind != SubjectWorkItem {
			t.Errorf("sampled=%s slots=%+v", sampled, slots)
		}
	}
}

func TestWorkItemGuardPreMembershipSaveDomain(t *testing.T) {
	for _, name := range []string{"empty", "offer", "window", "decisive_site", "decisive_status", "unmeasured_census", "zero_census", "committed", "committed_candidate", "cohort", "claims", "paths", "drivers", "remaining", "readiness", "conflicts", "refs", "labels", "judgment", "state", "pressures"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			state := workItemTupleSemanticStateFixture()
			state.WorkItemCensus = nil
			result := InvestigationResult{ResultID: "result_prospective", Status: InvestigationNoMatch}
			site := BudgetAssertSubjectlessTerminal
			sample := workItemTuplePayloadFixture(t)
			switch name {
			case "offer":
				result.Status = InvestigationClarificationRequired
				result.SubjectResolution.Candidates = sample.SubjectResolution.Candidates
				result.SubjectResolution.Candidates[0].State = ResolutionAmbiguous
			case "window":
				site = BudgetAssertWindowConfirmationRequired
				result.Status = InvestigationClarificationRequired
			case "decisive_site":
				site = BudgetAssertDecisive
			case "decisive_status":
				result.Status = InvestigationComplete
			case "unmeasured_census":
				state.WorkItemCensus = &WorkItemTupleCensus{State: WorkItemMembershipCensusUnmeasured}
			case "zero_census":
				state.WorkItemCensus = &WorkItemTupleCensus{State: WorkItemMembershipCensusExact}
			case "committed":
				result.SubjectResolution.Committed = sample.SubjectResolution.Committed
			case "committed_candidate":
				result.SubjectResolution.Candidates = sample.SubjectResolution.Candidates
			case "cohort":
				result.Cohort = &Cohort{Members: []CohortMember{}}
			case "claims":
				result.ClaimedFacts = []ClaimedFact{{}}
			case "paths":
				result.Paths = []RelationshipPath{{}}
			case "drivers":
				result.Drivers = []DriverJudgment{{}}
			case "remaining":
				result.RemainingWork = []Finding{{}}
			case "readiness":
				result.ReadinessGaps = []Finding{{}}
			case "conflicts":
				result.Conflicts = []Finding{{}}
			case "refs":
				result.EvidenceRefIDs = []string{"foreign"}
			case "labels":
				result.EvidenceRefLabels = map[string]string{"foreign": "label"}
			case "judgment":
				result.DirectJudgment = "answer"
			case "state":
				result.CurrentState = "answer"
			case "pressures":
				result.StrongestPressures = []string{"answer"}
			}
			store := &resultStoreStub{}
			engine := mustReuseTestEngine(t, EngineDependencies{Results: store})
			err := engine.saveResult(context.Background(), storage.Principal{OrgID: "org-1"}, site, result, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateOf(state)})
			want := name == "empty" || name == "offer" || name == "window"
			if (err == nil) != want || (store.saved.ResultID != "") != want {
				t.Errorf("save err=%v persisted=%s want allowed=%v", err, store.saved.ResultID, want)
			}
		})
	}
}

func TestWorkItemGuardPreMembershipNilReading(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	if workItemTuplePreMembershipTerminal(BudgetAssertSubjectlessTerminal, InvestigationResult{Status: InvestigationNoMatch}, nil) {
		t.Error("nil reading qualified for tuple terminal exemption")
	}
}
