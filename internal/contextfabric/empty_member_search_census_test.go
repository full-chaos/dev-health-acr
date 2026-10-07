package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func scopedMemberFrame(kind SubjectKind) *QuestionFrame {
	return &QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:   SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{AnchorTerms: []string{"the platform team"}, MemberKind: kind},
		},
		Temporal: TemporalIntentCurrent,
	}
}

func noneFoundRows(bundle CanonicalFactBundle) int {
	rows := 0
	for _, detail := range bundle.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailGraphNoMemberFound {
			rows++
		}
	}
	return rows
}

func TestEmptyMemberSearchRowIsFiledOnlyFromAnEmptyServableCensus(t *testing.T) {
	member := Cohort{Members: []CohortMember{{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project:a", Label: "a"}}}}
	for _, tc := range []struct {
		name       string
		frame      *QuestionFrame
		graph      Coverage
		cohort     *Cohort
		population int
		want       int
	}{
		{"empty servable census", scopedMemberFrame(SubjectProject), Coverage{}, nil, 0, 1},
		{"empty cohort value", scopedMemberFrame(SubjectProject), Coverage{}, &Cohort{}, 0, 1},
		{"a served member", scopedMemberFrame(SubjectProject), Coverage{}, &member, 0, 0},
		{"members counted but not carried", scopedMemberFrame(SubjectProject), Coverage{}, nil, 3, 0},
		{"graph search degraded", scopedMemberFrame(SubjectProject), Coverage{Partial: true}, nil, 0, 0},
		{"member kind the graph cannot list", scopedMemberFrame(SubjectWorkItem), Coverage{}, nil, 0, 0},
		{"no frame", nil, Coverage{}, nil, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := CanonicalFactBundle{Coverage: Coverage{}}
			recordEmptyMemberSearch(&bundle, tc.frame, tc.graph, tc.cohort, tc.population)
			if got := noneFoundRows(bundle); got != tc.want {
				t.Fatalf("none-found rows = %d, want %d: %+v", got, tc.want, bundle.Coverage.Details)
			}
			if tc.want == 1 && !bundle.Coverage.Partial {
				t.Fatalf("coverage not partial beside the row")
			}
		})
	}
}

func TestEmptyMemberSearchRowIsNotFiledTwice(t *testing.T) {
	bundle := emptyFactReadBundle(scopedMemberFrame(SubjectProject), Coverage{})
	recordEmptyMemberSearch(&bundle, scopedMemberFrame(SubjectProject), Coverage{}, nil, 0)
	if got := noneFoundRows(bundle); got != 1 {
		t.Fatalf("none-found rows = %d, want 1", got)
	}
}
