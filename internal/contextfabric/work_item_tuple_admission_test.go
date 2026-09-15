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

// Every combination of the ten A clauses is exercised. The oracle is the
// finite set of admitted bit patterns, independent of the implementation's
// branching order. Malformed discriminator/payload combinations are deliberate
// defensive inputs; ordinary frame validation must still precede integration.
//
// Clause 9 (ordering) only has an effect when clause 5 selects
// GoalRankOrSurvey: GoalAssessState never sets Emphasis here, so both its
// values produce the identical frame for that branch, and admission depends
// on it only in the rank_or_survey branch.
func TestProspectiveWorkItemTupleAdmissionClauseDomain(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	const clauses = 10
	const all = 1<<clauses - 1
	const required = 1<<4 | 1<<6 | 1<<7 | 1<<8
	counts := map[workItemTupleAdmission]int{}
	for mask := 0; mask <= all; mask++ {
		frame := prospectiveTupleFrame(GoalAssessState)
		family := QuestionFamilyScopedCohortStatus
		axis := TimeContext{Axis: TemporalCurrent}
		rankGoal := false
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
			rankGoal = true
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
		if rankGoal && mask&(1<<9) == 0 {
			frame.Emphasis = []AnswerEmphasis{EmphasisPositiveOutliers}
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
		} else if mask&required == required && (mask&(1<<5) != 0 || mask&(1<<9) != 0) {
			want = workItemTupleProspective
		}
		got := prospectiveWorkItemTupleAdmission(input, workItemTupleFamilyPolicyForTest(family), axis)
		if got != want {
			t.Fatalf("clause mask %010b: admission=%d want=%d frame=%+v family=%s axis=%s", mask, got, want, input, family, axis.Axis)
		}
		counts[got]++
	}
	t.Logf("clause domain: 1024 cells, not_applicable=%d refused=%d prospective=%d", counts[workItemTupleNotApplicable], counts[workItemTupleRefused], counts[workItemTupleProspective])
}

// TestProspectiveWorkItemTupleAdmissionGoalSets covers every subset of the
// goal vocabulary, with Emphasis empty (a plain survey/no-ordering frame in
// every cell): admitted exactly when the subset is non-empty and drawn only
// from {assess_state, count_or_aggregate, rank_or_survey}. The rank_or_survey
// member is admitted here on the SAME terms as the other two -- ordering is
// what refuses it, covered separately below and in the clause domain test.
func TestProspectiveWorkItemTupleAdmissionGoalSets(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	vocabulary := InvestigationGoalVocabulary()
	assessMask, countMask, rankMask := 0, 0, 0
	for i, goal := range vocabulary {
		switch goal {
		case GoalAssessState:
			assessMask = 1 << i
		case GoalCountOrAggregate:
			countMask = 1 << i
		case GoalRankOrSurvey:
			rankMask = 1 << i
		}
	}
	if assessMask == 0 || countMask == 0 || rankMask == 0 {
		t.Fatal("the accepted goal vocabulary is absent")
	}
	admittedBits := assessMask | countMask | rankMask
	for mask := 0; mask < 1<<len(vocabulary); mask++ {
		goals := []InvestigationGoal{}
		for i, goal := range vocabulary {
			if mask&(1<<i) != 0 {
				goals = append(goals, goal)
			}
		}
		wantAdmitted := mask != 0 && mask&^admittedBits == 0
		frame := prospectiveTupleFrame(goals...)
		got := prospectiveWorkItemTupleAdmission(&frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent})
		if (got == workItemTupleProspective) != wantAdmitted {
			t.Fatalf("goals=%v emphasis=none admission=%d want admitted=%v", goals, got, wantAdmitted)
		}
		if mask&rankMask == 0 {
			continue
		}
		// The identical goal set with an ordering requested must refuse,
		// whatever else rank_or_survey combines with here.
		ordered := prospectiveTupleFrame(goals...)
		ordered.Emphasis = []AnswerEmphasis{EmphasisPositiveOutliers}
		if got := prospectiveWorkItemTupleAdmission(&ordered, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}); got != workItemTupleRefused {
			t.Fatalf("goals=%v emphasis=positive_outliers admission=%d want=refused", goals, got)
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
		{"survey_only", []InvestigationGoal{GoalRankOrSurvey}, workItemTupleProspective},
		{"survey_with_assess", []InvestigationGoal{GoalRankOrSurvey, GoalAssessState}, workItemTupleProspective},
		{"survey_and_unknown", []InvestigationGoal{GoalRankOrSurvey, "future_goal"}, workItemTupleRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			frame := prospectiveTupleFrame(tc.goals...)
			if got := prospectiveWorkItemTupleAdmission(&frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}); got != tc.want {
				t.Fatalf("goals=%v admission=%d want=%d", tc.goals, got, tc.want)
			}
		})
	}
	// The ordering predicate itself: both Emphasis members refuse a
	// rank_or_survey frame, an empty (non-nil) slice does not, and a
	// non-rank goal set is unaffected by Emphasis entirely -- it is never
	// consulted outside the rank_or_survey arm.
	for _, tc := range []struct {
		name     string
		goals    []InvestigationGoal
		emphasis []AnswerEmphasis
		want     workItemTupleAdmission
	}{
		{"survey_positive_outliers", []InvestigationGoal{GoalRankOrSurvey}, []AnswerEmphasis{EmphasisPositiveOutliers}, workItemTupleRefused},
		{"survey_negative_outliers", []InvestigationGoal{GoalRankOrSurvey}, []AnswerEmphasis{EmphasisNegativeOutliers}, workItemTupleRefused},
		{"survey_empty_slice", []InvestigationGoal{GoalRankOrSurvey}, []AnswerEmphasis{}, workItemTupleProspective},
		{"assess_with_emphasis_ignored", []InvestigationGoal{GoalAssessState}, []AnswerEmphasis{EmphasisPositiveOutliers}, workItemTupleProspective},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			frame := prospectiveTupleFrame(tc.goals...)
			frame.Emphasis = tc.emphasis
			if got := prospectiveWorkItemTupleAdmission(&frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}); got != tc.want {
				t.Fatalf("goals=%v emphasis=%v admission=%d want=%d", tc.goals, tc.emphasis, got, tc.want)
			}
		})
	}
	t.Logf("goal domain: all %d subsets of %d goals, per-subset ordering control for rank_or_survey, plus nine named presence/unknown/order/emphasis cells", 1<<len(vocabulary), len(vocabulary))
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

// TestWorkItemTuplePromotionStripsRankingObligationOnlyOnPromotion pins the
// invariant: GoalRankOrSurvey unconditionally derives
// ObligationRanking (frame_obligations.go) whether the frame asks to rank
// or only to survey, but this arm never computes one -- RankCohort is
// skipped for every work-item tuple and the registry declares no ranking
// producer for work_item. Left alone, an admitted survey turn would derive
// a REQUIRED requirement no producer can ever serve and degrade every
// survey answer, contradicting the one promise this arm makes for it: the
// same answer contract GoalAssessState already gets. The obligation is
// stripped exactly when, and only when, the tuple PROMOTES the gate --
// never on a frame this arm leaves alone, and never on the frame it still
// refuses (the ordering-present control).
func TestWorkItemTuplePromotionStripsRankingObligationOnlyOnPromotion(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tc := range []struct {
		name         string
		goals        []InvestigationGoal
		emphasis     []AnswerEmphasis
		wantPromoted bool
		wantRanking  bool
	}{
		{"survey_admitted_ranking_stripped", []InvestigationGoal{GoalRankOrSurvey}, nil, true, false},
		{"survey_ordering_refused_obligations_untouched", []InvestigationGoal{GoalRankOrSurvey}, []AnswerEmphasis{EmphasisPositiveOutliers}, false, true},
		{"assess_state_admitted_no_ranking_to_strip", []InvestigationGoal{GoalAssessState}, nil, true, false},
		{"assess_count_admitted_no_ranking_to_strip", []InvestigationGoal{GoalAssessState, GoalCountOrAggregate}, nil, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer reportWorkItemMutationPanic(t)
			frame := prospectiveTupleFrame(tc.goals...)
			frame.Emphasis = tc.emphasis
			before := append([]AnswerObligation(nil), frame.Obligations...)
			beforeHadRanking := frame.HasObligation(ObligationRanking)
			gate := workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent})
			if (gate.Outcome == FrameGatePassed) != tc.wantPromoted {
				t.Fatalf("gate=%+v want promoted=%v", gate, tc.wantPromoted)
			}
			if frame.HasObligation(ObligationRanking) != tc.wantRanking {
				t.Fatalf("obligations=%v HasObligation(ranking)=%v want=%v", frame.Obligations, frame.HasObligation(ObligationRanking), tc.wantRanking)
			}
			if !tc.wantPromoted {
				if !reflect.DeepEqual(frame.Obligations, before) {
					t.Fatalf("a non-promoting call mutated obligations: before=%v after=%v", before, frame.Obligations)
				}
				return
			}
			// Every other obligation the frame started with survives; only
			// ranking (when present) is removed, and only that.
			for _, obligation := range before {
				if obligation == ObligationRanking {
					continue
				}
				if !frame.HasObligation(obligation) {
					t.Fatalf("promotion dropped an unrelated obligation %s: before=%v after=%v", obligation, before, frame.Obligations)
				}
			}
			if beforeHadRanking && len(frame.Obligations) != len(before)-1 {
				t.Fatalf("promotion should remove exactly one obligation (ranking): before=%v after=%v", before, frame.Obligations)
			}
			if !beforeHadRanking && len(frame.Obligations) != len(before) {
				t.Fatalf("promotion changed obligation count with no ranking to remove: before=%v after=%v", before, frame.Obligations)
			}
		})
	}
}
