package answerprojection

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func capFixture(canonical int, countRows int) contractsv1.ContextFabricAnswerProjection {
	rows := make([]contractsv1.ContextFabricPlanRequirementOutcomeRow, 0, canonical)
	for i := 0; i < canonical; i++ {
		row := contractsv1.ContextFabricPlanRequirementOutcomeRow{
			Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
			Requirement: "state/member/team",
			Obligation:  "state",
			Outcome:     contractsv1.ContextFabricRequirementSatisfied,
			Impact:      contractsv1.ContextFabricAnswerImpactNone,
		}
		if i < countRows {
			row.Stage = contractsv1.ContextFabricOutcomeStageAssembledResult
			row.Requirement = "count/member/" + string(rune('a'+i))
			row.Obligation = string(contractsv1.ContextFabricAnswerObligationCount)
			row.Served, row.Declared = 9, 9
		}
		rows = append(rows, row)
	}
	return contractsv1.ContextFabricAnswerProjection{
		Cohort: &contractsv1.ContextFabricProjectedCohort{Members: make([]contractsv1.ContextFabricProjectedCohortMember, 1)},
		Completeness: contractsv1.ContextFabricAnswerCompleteness{
			TerminalStatus: contractsv1.ContextFabricInvestigationComplete,
			State:          contractsv1.ContextFabricAnswerCompletenessComplete,
			Outcomes:       rows,
		},
		ProjectionBudget: contractsv1.ContextFabricProjectionBudget{
			CohortMembersOmitted: 1, CohortGroupsOmitted: 1, CandidatesOmitted: 1,
			DriversOmitted: 1, WithheldDriversOmitted: 1, FactsOmitted: 1,
			EvidenceRefsOmitted: 1, ReasonsOmitted: 1, ValuesClamped: 1,
			LimitationsOmitted: 1, WarningsOmitted: 1, CoverageOmitted: 1,
			RenderShapesOmitted: 1,
		},
	}
}

func TestProjectedOutcomeRowsNeverExceedTheRowCap(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount
	for _, canonical := range []int{max - 20, max - 12, max - 5, max - 1, max} {
		served := appendProjectionOutcomes(capFixture(canonical, 3))
		if got := len(served.Completeness.Outcomes); got > max {
			t.Errorf("canonical=%d: projected %d rows, cap %d", canonical, got, max)
		}
		if served.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
			t.Errorf("canonical=%d: cut projection serves a complete state", canonical)
		}
	}
}

func TestAnOutcomeRowCutIsDisclosedOutsideTheRows(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount
	for _, canonical := range []int{max - 12, max - 1, max} {
		served := appendProjectionOutcomes(capFixture(canonical, 3))
		found := false
		for _, l := range served.Limitations {
			if strings.Contains(l, "outcome rows were cut") {
				found = true
			}
		}
		if !found || !served.ProjectionBudget.Truncated {
			t.Errorf("canonical=%d: cut not disclosed (limitations=%v truncated=%v)", canonical, served.Limitations, served.ProjectionBudget.Truncated)
		}
	}
}

func TestAFullCanonicalSetKeepsItsRowsUntouched(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount
	in := capFixture(max, 3)
	served := appendProjectionOutcomes(in)
	if len(served.Completeness.Outcomes) != max {
		t.Fatalf("rows = %d, want %d", len(served.Completeness.Outcomes), max)
	}
	for i := range in.Completeness.Outcomes {
		if served.Completeness.Outcomes[i].Requirement != in.Completeness.Outcomes[i].Requirement {
			t.Fatalf("canonical row %d rewritten", i)
		}
	}
}

func TestAnOrdinaryFrameAppendsEveryRowAndDisclosesNoCut(t *testing.T) {
	t.Parallel()
	served := appendProjectionOutcomes(capFixture(60, 3))
	if got, want := len(served.Completeness.Outcomes), 60+13+3; got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}
	if len(served.Limitations) != 0 {
		t.Fatalf("ordinary frame gained limitations: %v", served.Limitations)
	}
}

func TestOutcomeRowCapBoundaries(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount
	cases := []struct {
		name      string
		canonical int
		rows      int
		cut       bool
	}{
		{"everything fits exactly", max - 16, max, false},
		{"one row short cuts", max - 15, max, true},
		{"omissions fit exactly, counts dropped", max - 13, max, true},
		{"omissions merged, counts kept", max - 12, max - 12 + 2 + 3, true},
		{"over cap canonical gets nothing appended", max + 3, max + 3, true},
	}
	for _, c := range cases {
		served := appendProjectionOutcomes(capFixture(c.canonical, 3))
		if got := len(served.Completeness.Outcomes); got != c.rows {
			t.Errorf("%s: rows = %d, want %d", c.name, got, c.rows)
		}
		cutSeen := false
		for _, l := range served.Limitations {
			cutSeen = cutSeen || strings.Contains(l, "outcome rows were cut")
		}
		if cutSeen != c.cut {
			t.Errorf("%s: cut disclosed = %v, want %v", c.name, cutSeen, c.cut)
		}
	}
}

func TestACutDisclosureDoesNotOverflowTheLimitationsCap(t *testing.T) {
	t.Parallel()
	in := capFixture(contractsv1.ContextFabricPlanRequirementOutcomeMaxCount, 3)
	for i := 0; i < contractsv1.ContextFabricProjectedNarrativeMaxCount; i++ {
		in.Limitations = append(in.Limitations, "limitation "+strings.Repeat("x", i%7)+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	before := in.ProjectionBudget.LimitationsOmitted
	served := appendProjectionOutcomes(in)
	if len(served.Limitations) != contractsv1.ContextFabricProjectedNarrativeMaxCount {
		t.Fatalf("limitations = %d", len(served.Limitations))
	}
	if served.ProjectionBudget.LimitationsOmitted != before+1 {
		t.Fatalf("displaced limitation not counted: %d -> %d", before, served.ProjectionBudget.LimitationsOmitted)
	}
	if !strings.Contains(served.Limitations[len(served.Limitations)-1], "outcome rows were cut") {
		t.Fatal("disclosure missing")
	}
}
