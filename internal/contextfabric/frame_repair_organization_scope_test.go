package contextfabric

import (
	"reflect"
	"testing"
)

type organizationSurveyCell struct {
	cell     string
	receipt  func(*ModelExecutionReceipt)
	frame    QuestionFrame
	repaired bool
	member   SubjectKind
	goals    []InvestigationGoal
	// qualifier is the member qualifier the accepted frame must carry.
	qualifier MemberQualifier
}

func organizationSurveyCells() []organizationSurveyCell {
	organization := SubjectOrganization
	project := SubjectProject
	workItem := SubjectWorkItem
	pullRequest := SubjectPullRequest
	keep := func(*ModelExecutionReceipt) {}
	qualified := organizationProposal(&pullRequest, GoalAssessState)
	qualified.SubjectExpression.Org.MemberQualifier = MemberQualifierStatus
	qualified.SubjectExpression.Org.MemberQualifierValue = "open"
	return []organizationSurveyCell{
		{
			cell: "no member kind: the teams", receipt: keep,
			frame: organizationProposal(nil, GoalAssessState), repaired: true, member: SubjectTeam,
			goals: []InvestigationGoal{GoalAssessState, GoalRankOrSurvey},
		},
		{
			cell: "the organization as member kind is not servable: the teams", receipt: keep,
			frame: organizationProposal(&organization, GoalExplainDrivers), repaired: true, member: SubjectTeam,
			goals: []InvestigationGoal{GoalExplainDrivers, GoalRankOrSurvey},
		},
		{
			cell: "a servable member kind is kept", receipt: keep,
			frame: organizationProposal(&project, GoalAssessState, GoalExplainDrivers), repaired: true, member: SubjectProject,
			goals: []InvestigationGoal{GoalAssessState, GoalExplainDrivers, GoalRankOrSurvey},
		},
		{
			cell: "a member qualifier rides with the frame's own kind", receipt: keep,
			frame: qualified, repaired: true, member: SubjectPullRequest, qualifier: MemberQualifierStatus,
			goals: []InvestigationGoal{GoalAssessState, GoalRankOrSurvey},
		},
		{
			cell:    "an unservable member kind falls to the requested kind",
			receipt: func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = SubjectRepository },
			frame:   organizationProposal(&workItem, GoalAssessState), repaired: true, member: SubjectRepository,
			goals: []InvestigationGoal{GoalAssessState, GoalRankOrSurvey},
		},
		{
			cell: "an unrecognized requested kind is not read",
			receipt: func(r *ModelExecutionReceipt) {
				r.RequestedSubjectKind = SubjectRepository
				r.RequestedSubjectKindUnrecognized = true
			},
			frame: organizationProposal(nil, GoalAssessState), repaired: true, member: SubjectTeam,
			goals: []InvestigationGoal{GoalAssessState, GoalRankOrSurvey},
		},
		{
			cell: "a survey goal is not added twice", receipt: keep,
			frame: organizationProposal(nil, GoalRankOrSurvey), repaired: true, member: SubjectTeam,
			goals: []InvestigationGoal{GoalRankOrSurvey},
		},
		{
			cell: "a count beside an assessment surveys the counted kind", receipt: keep,
			frame: organizationProposal(&project, GoalAssessState, GoalCountOrAggregate), repaired: true, member: SubjectProject,
			goals: []InvestigationGoal{GoalAssessState, GoalRankOrSurvey, GoalCountOrAggregate},
		},
		{
			cell: "a count alone keeps the member census", receipt: keep,
			frame: organizationProposal(&project, GoalCountOrAggregate),
		},
		{
			cell: "an investment question keeps the organization subject", receipt: keep,
			frame: organizationProposal(nil, GoalAllocateInvestment, GoalExplainDrivers),
		},
		{
			cell: "a discovered cohort is not this repair's", receipt: keep,
			frame: QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: discoveredExpression(SubjectTeam), Temporal: TemporalIntentCurrent},
		},
	}
}

// TestTheOrganizationScopeSurveyRepairOverItsDomain drives every cell through
// the production interpreter and reads the accepted frame and the line.
func TestTheOrganizationScopeSurveyRepairOverItsDomain(t *testing.T) {
	for _, testCase := range organizationSurveyCells() {
		t.Run(testCase.cell, func(t *testing.T) {
			receipt := validModelReceiptFixture(ModelOperationInterpret)
			testCase.receipt(&receipt)
			run := interpretForRepair(t, receipt, testCase.frame, nil)
			accepted := run.receipt.QuestionFrame
			if accepted == nil {
				t.Fatal("the receipt carries no frame")
			}
			if !testCase.repaired {
				if name := run.line["repair"]; name == "organization_scope_survey" {
					t.Fatalf("repair = %v on a frame the repair does not own", name)
				}
				if accepted.SubjectExpression.Kind != testCase.frame.SubjectExpression.Kind {
					t.Fatalf("accepted expression = %q, want the proposal's %q", accepted.SubjectExpression.Kind, testCase.frame.SubjectExpression.Kind)
				}
				return
			}
			assertRepairLine(t, run.line, map[string]any{
				"outcome": "repaired", "repair_decision": "applied", "repair": "organization_scope_survey",
				"repair_kind_before": "organization_scope", "repair_kind_after": "discovered_kind",
				"repair_member_kind": string(testCase.member), "frame_gate": "passed", "repair_attempts": float64(1),
			})
			discovered := accepted.SubjectExpression.Discovered
			if accepted.SubjectExpression.Kind != SubjectExpressionDiscoveredKind || discovered == nil || discovered.MemberKind != testCase.member {
				t.Fatalf("accepted expression = %+v, want discovered_kind %s", accepted.SubjectExpression, testCase.member)
			}
			if discovered.MemberQualifier != testCase.qualifier {
				t.Fatalf("member qualifier = %q, want %q", discovered.MemberQualifier, testCase.qualifier)
			}
			if !reflect.DeepEqual(accepted.Goals, testCase.goals) {
				t.Fatalf("accepted goals = %v, want %v", accepted.Goals, testCase.goals)
			}
			if want := requestedJudgmentForGoals(testCase.goals); run.receipt.RequestedJudgmentRepair != want {
				t.Fatalf("requested judgment = %q, want %q", run.receipt.RequestedJudgmentRepair, want)
			}
		})
	}
}

// TestTheOrganizationScopeSurveyRepairRunsAtMostOnce holds the bound.
func TestTheOrganizationScopeSurveyRepairRunsAtMostOnce(t *testing.T) {
	t.Parallel()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	proposal := organizationProposal(nil, GoalAssessState)
	base := validateAgainstInterpretation(receipt, proposal, ShapeOpen)
	base.Repair.Attempts = frameRepairBound
	result := repairOrganizationScopeSurvey(receipt, proposal, ShapeOpen, nil, base)
	if result.Repair.Decision != FrameRepairDeclinedBoundReached || result.Frame.SubjectExpression.Kind != SubjectExpressionOrganizationScope {
		t.Fatalf("result = %+v, want declined_bound_reached with the proposal's frame", result)
	}
}
