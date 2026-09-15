package contextfabric

import (
	"fmt"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func prospectiveTupleFrame(goals ...InvestigationGoal) QuestionFrame {
	return frameWith(goals, scopedExpression(SubjectWorkItem), TemporalIntentCurrent, nil)
}

// Every combination of the nine A clauses is exercised. The oracle is the
// finite set of admitted bit patterns, independent of the implementation's
// branching order. Malformed discriminator/payload combinations are deliberate
// defensive inputs; ordinary frame validation must still precede integration.
func TestProspectiveWorkItemTupleAdmissionClauseDomain(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	const clauses = 9
	const all = 1<<clauses - 1
	counts := map[workItemTupleAdmission]int{}
	for mask := 0; mask <= all; mask++ {
		frame := prospectiveTupleFrame(GoalAssessState)
		family := QuestionFamilyScopedCohortStatus
		axis := TimeContext{Axis: TemporalCurrent}
		if mask&(1<<1) == 0 {
			frame.SubjectExpression.Kind = SubjectExpressionDiscoveredKind
		}
		if mask&(1<<3) == 0 {
			frame.SubjectExpression.Scoped.MemberKind = SubjectIncident
		}
		if mask&(1<<4) == 0 {
			family = QuestionFamilyDiscoveredCohortRanking
		}
		if mask&(1<<5) == 0 {
			frame.Goals = []InvestigationGoal{GoalRankOrSurvey}
		}
		if mask&(1<<6) == 0 {
			frame.Temporal = TemporalIntentBoundedWindow
		}
		if mask&(1<<7) == 0 {
			axis.Axis = TemporalValidTime
		}
		if mask&(1<<8) == 0 {
			frame.SubjectExpression.Scoped.MemberQualifier = MemberQualifierStatus
		}
		if mask&(1<<2) == 0 {
			frame.SubjectExpression.Scoped = nil
		}
		input := &frame
		if mask&1 == 0 {
			input = nil
		}
		want := workItemTupleRefused
		if mask&15 != 15 {
			want = workItemTupleNotApplicable
		} else if mask == all {
			want = workItemTupleProspective
		}
		got := prospectiveWorkItemTupleAdmission(input, workItemTupleFamilyPolicyForTest(family), axis)
		if got != want {
			t.Fatalf("clause mask %09b: admission=%d want=%d frame=%+v family=%s axis=%s", mask, got, want, input, family, axis.Axis)
		}
		counts[got]++
	}
	t.Logf("clause domain: 512 cells, not_applicable=%d refused=%d prospective=%d", counts[workItemTupleNotApplicable], counts[workItemTupleRefused], counts[workItemTupleProspective])
}

func TestProspectiveWorkItemTupleAdmissionGoalSets(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	vocabulary := InvestigationGoalVocabulary()
	allowed := map[int]bool{}
	assessMask, countMask := 0, 0
	for i, goal := range vocabulary {
		switch goal {
		case GoalAssessState:
			assessMask = 1 << i
		case GoalCountOrAggregate:
			countMask = 1 << i
		}
	}
	if assessMask == 0 || countMask == 0 {
		t.Fatal("the accepted goal vocabulary is absent")
	}
	allowed[assessMask], allowed[countMask], allowed[assessMask|countMask] = true, true, true
	for mask := 0; mask < 1<<len(vocabulary); mask++ {
		goals := []InvestigationGoal{}
		for i, goal := range vocabulary {
			if mask&(1<<i) != 0 {
				goals = append(goals, goal)
			}
		}
		frame := prospectiveTupleFrame(goals...)
		got := prospectiveWorkItemTupleAdmission(&frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent})
		if (got == workItemTupleProspective) != allowed[mask] {
			t.Fatalf("goals=%v admission=%d want admitted=%v", goals, got, allowed[mask])
		}
	}
	for _, tc := range []struct {
		name  string
		goals []InvestigationGoal
		want  workItemTupleAdmission
	}{
		{"nil", nil, workItemTupleRefused},
		{"empty", []InvestigationGoal{}, workItemTupleRefused},
		{"unknown", []InvestigationGoal{"future_goal"}, workItemTupleRefused},
		{"known_and_unknown", []InvestigationGoal{GoalAssessState, "future_goal"}, workItemTupleRefused},
		{"duplicate", []InvestigationGoal{GoalAssessState, GoalAssessState}, workItemTupleProspective},
		{"reordered", []InvestigationGoal{GoalCountOrAggregate, GoalAssessState}, workItemTupleProspective},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			frame := prospectiveTupleFrame(tc.goals...)
			if got := prospectiveWorkItemTupleAdmission(&frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}); got != tc.want {
				t.Fatalf("goals=%v admission=%d want=%d", tc.goals, got, tc.want)
			}
		})
	}
	t.Logf("goal domain: all %d subsets of %d goals plus six presence/unknown/order cells", 1<<len(vocabulary), len(vocabulary))
}

func TestProspectiveWorkItemTupleAdmissionAxesFamilyAndQualifier(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	intents := []TemporalIntent{"", "future_intent"}
	for _, intent := range TemporalIntentVocabulary() {
		intents = append(intents, intent)
	}
	axes := []TemporalAxis{"", "future_axis"}
	for _, axis := range contractsv1.ContextFabricTemporalAxisVocabulary() {
		axes = append(axes, axis)
	}
	families := []QuestionFamily{"", "future_family"}
	for _, family := range contractsv1.ContextFabricQuestionFamilyVocabulary() {
		families = append(families, family)
	}
	qualifiers := []string{"", "status", "assignee", "unrecognized", "future_filter", "  ", "\u2003"}
	cells, admitted := 0, 0
	for _, intent := range intents {
		for _, axis := range axes {
			for _, family := range families {
				for _, raw := range qualifiers {
					frame := prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate)
					frame.Temporal = intent
					qualifier, _ := SanitizeMemberQualifier(raw)
					frame.SubjectExpression.Scoped.MemberQualifier = qualifier
					got := prospectiveWorkItemTupleAdmission(&frame, workItemTupleFamilyPolicyForTest(family), TimeContext{Axis: axis})
					want := workItemTupleRefused
					if intent == TemporalIntentCurrent && axis == TemporalCurrent && family == QuestionFamilyScopedCohortStatus && raw == "" {
						want = workItemTupleProspective
					}
					if got != want {
						t.Fatalf("intent=%q axis=%q family=%q raw qualifier=%q: admission=%d want=%d", intent, axis, family, raw, got, want)
					}
					if got == workItemTupleRefused && got.refusalBasis() != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
						t.Fatalf("unapproved axis/filter token escaped: %q", got.refusalBasis())
					}
					cells++
					if got == workItemTupleProspective {
						admitted++
					}
				}
			}
		}
	}
	t.Logf("axis/family/qualifier domain: %d cells, admitted=%d; unknown and whitespace qualifier presence preserved", cells, admitted)
}

func TestProspectiveWorkItemTupleAnchorAnswerability(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	kinds := []SubjectKind{"", "future_kind"}
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		kinds = append(kinds, kind)
	}
	for _, admission := range []workItemTupleAdmission{workItemTupleNotApplicable, workItemTupleRefused, workItemTupleProspective, 255} {
		for _, kind := range kinds {
			want := admission == workItemTupleProspective && kind == SubjectProject
			if got := admission.anchorAnswerable(kind); got != want {
				t.Fatalf("admission=%d anchor kind=%q answerable=%v want=%v", admission, kind, got, want)
			}
		}
	}
	for _, admission := range []workItemTupleAdmission{workItemTupleNotApplicable, workItemTupleProspective} {
		if admission.refusalBasis() != contractsv1.ContextFabricRefusalBasisUnspecified {
			t.Fatalf("non-refusal admission=%d received a refusal", admission)
		}
	}
	t.Logf("anchor domain: %d cells; project kind alone is no existence or authorization assertion", 4*len(kinds))
}

func TestProspectiveWorkItemTupleAdmissionLeavesLivePathsDormant(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, expression := range []SubjectExpression{
		scopedExpression(SubjectWorkItem), discoveredExpression(SubjectWorkItem),
		groupedExpression(SubjectWorkItem, SubjectProject), orgExpression(kindPointer(SubjectWorkItem)),
		scopedExpression(SubjectIncident), namedExpression(SubjectProject),
	} {
		memberKind, _ := expression.MemberKind()
		t.Run(fmt.Sprintf("%s/%s", expression.Kind, memberKind), func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			frame := frameWith([]InvestigationGoal{GoalAssessState, GoalCountOrAggregate}, expression, TemporalIntentCurrent, nil)
			validation := ValidateFrame(frame, nil, "")
			before := DecideFrameGate(validation, true)
			got := prospectiveWorkItemTupleAdmission(&frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent})
			after := DecideFrameGate(ValidateFrame(frame, nil, ""), true)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("dormant decision changed live gate: before=%+v after=%+v", before, after)
			}
			if expression.Kind == SubjectExpressionChildrenOfScope && expression.Scoped.MemberKind == SubjectWorkItem {
				if got != workItemTupleProspective || !after.Refuses() || after.RefusalBasis() != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
					t.Fatalf("prospective=%d live gate=%+v; tuple dispatch must remain inactive", got, after)
				}
				if ScopeAnchorRetrievalKind(&frame, SubjectTeam) != SubjectTeam {
					t.Fatal("the prospective helper changed existing anchor retrieval")
				}
			} else if got != workItemTupleNotApplicable {
				t.Fatalf("another expression's admission was intercepted: %d", got)
			}
		})
	}
}

func TestWorkItemTupleFinalGateOnlyTightens(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState), nil, "").Frame
	current := TimeContext{Axis: TemporalCurrent}
	initial := workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), current)
	if initial.Outcome != FrameGatePassed {
		t.Fatalf("initial prospective tuple was not admitted: %+v", initial)
	}
	refusals := []FrameGate{
		{Outcome: FrameGateRejectedInvalid, FailedInvariant: FrameInvariantI6},
		{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectWorkItem},
		{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable, DeclaredMemberKind: SubjectDocument},
	}
	for _, gate := range refusals {
		for _, family := range []QuestionFamily{QuestionFamilyScopedCohortStatus, QuestionFamilyUnclassified} {
			got := tightenWorkItemTupleFrameGate(gate, &frame, workItemTupleFamilyPolicyForTest(family), current)
			if got != gate {
				t.Errorf("final gate changed earlier refusal: before=%+v after=%+v", gate, got)
			}
		}
	}
	for _, family := range []QuestionFamily{QuestionFamilyScopedCohortStatus, QuestionFamilyUnclassified} {
		for _, axis := range []TemporalAxis{TemporalCurrent, TemporalValidTime} {
			got := tightenWorkItemTupleFrameGate(initial, &frame, workItemTupleFamilyPolicyForTest(family), TimeContext{Axis: axis})
			wantPass := family == QuestionFamilyScopedCohortStatus && axis == TemporalCurrent
			if (got.Outcome == FrameGatePassed) != wantPass {
				t.Errorf("family=%s axis=%s final gate=%+v", family, axis, got)
			}
		}
	}
}

func workItemTupleFamilyPolicyForTest(family QuestionFamily) bool {
	definition, known := LookupQuestionFamily(family)
	return known && definition.allowsWorkItemTuple
}
