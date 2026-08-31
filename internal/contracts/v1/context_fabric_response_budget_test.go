package v1

import (
	"encoding/json"
	"testing"
)

// budgetFixtureResult builds a result with a KNOWN count in every charged
// collection, so a miscounted or newly-uncounted collection changes a number
// this test names rather than passing silently.
func budgetFixtureResult() ContextFabricInvestigationResult {
	result := ContextFabricInvestigationResult{}
	for i := 0; i < 4; i++ {
		result.SubjectResolution.Candidates = append(result.SubjectResolution.Candidates, ContextFabricSubjectCandidate{})
	}
	for i := 0; i < 3; i++ {
		result.Drivers = append(result.Drivers, ContextFabricDriverJudgment{})
	}
	for i := 0; i < 7; i++ {
		result.Paths = append(result.Paths, ContextFabricRelationshipPath{})
	}
	for i := 0; i < 2; i++ {
		result.RemainingWork = append(result.RemainingWork, ContextFabricFinding{})
	}
	result.ReadinessGaps = append(result.ReadinessGaps, ContextFabricFinding{})
	result.Conflicts = append(result.Conflicts, ContextFabricFinding{}, ContextFabricFinding{}, ContextFabricFinding{}, ContextFabricFinding{}, ContextFabricFinding{})
	for i := 0; i < 6; i++ {
		result.ClaimedFacts = append(result.ClaimedFacts, ContextFabricClaimedFact{})
	}
	result.Cohort = &ContextFabricCohort{Members: make([]ContextFabricCohortMember, 9)}
	return result
}

func TestCountContextFabricResultItems_ChargesEveryCollection(t *testing.T) {
	counts := CountContextFabricResultItems(budgetFixtureResult())
	want := ContextFabricResultItemCounts{
		Candidates: 4, Drivers: 3, Paths: 7, RemainingWork: 2,
		ReadinessGaps: 1, Conflicts: 5, ClaimedFacts: 6, CohortMembers: 9,
	}
	if counts != want {
		t.Fatalf("CountContextFabricResultItems() = %+v, want %+v", counts, want)
	}
	if got := counts.Total(); got != 37 {
		t.Fatalf("Total() = %d, want 37", got)
	}
	// Budgeted EXCLUDES Paths (CHAOS-4523). That exclusion living beside the
	// count is the whole point of moving the definition here.
	if got := counts.Budgeted(); got != 30 {
		t.Fatalf("Budgeted() = %d, want 30 (Total 37 minus 7 Paths)", got)
	}
}

func TestCountContextFabricResultItems_NilCohortChargesZeroMembers(t *testing.T) {
	result := budgetFixtureResult()
	result.Cohort = nil
	counts := CountContextFabricResultItems(result)
	if counts.CohortMembers != 0 {
		t.Fatalf("CohortMembers = %d for a nil cohort, want 0", counts.CohortMembers)
	}
	if got := counts.Budgeted(); got != 21 {
		t.Fatalf("Budgeted() = %d, want 21", got)
	}
}

// TestMeasureContextFabricResponse_UsesTheServedEncoder pins the property the
// whole three-stage budget rests on: the size the engine measures is the size
// the route serves. A second, independently-drifting json.Marshal call is
// exactly what revision 3 of the budget design failed on.
func TestMeasureContextFabricResponse_UsesTheServedEncoder(t *testing.T) {
	result := budgetFixtureResult()
	measurement, err := MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if measurement.Bytes != int64(len(encoded)) {
		t.Fatalf("measured %d bytes, json.Marshal produced %d", measurement.Bytes, len(encoded))
	}
	if measurement.Items != CountContextFabricResultItems(result) {
		t.Fatalf("measurement.Items = %+v, want the same counts CountContextFabricResultItems reports", measurement.Items)
	}
}

func TestContextFabricResponseMeasurement_Overrun(t *testing.T) {
	measurement := ContextFabricResponseMeasurement{
		Items: ContextFabricResultItemCounts{ClaimedFacts: 30, Paths: 20},
		Bytes: 1000,
	}
	cases := []struct {
		name   string
		budget ContextFabricResponseBudget
		want   ContextFabricBudgetOverrun
	}{
		{"fits on both axes", ContextFabricResponseBudget{MaxItems: 30, MaxSerializedBytes: 1000}, ContextFabricBudgetFits},
		{"items exceeded", ContextFabricResponseBudget{MaxItems: 29, MaxSerializedBytes: 1000}, ContextFabricBudgetOverrunItems},
		{"bytes exceeded", ContextFabricResponseBudget{MaxItems: 30, MaxSerializedBytes: 999}, ContextFabricBudgetOverrunBytes},
		// Items are reported first when BOTH are exceeded: one closed value
		// must be reported, and the item budget is the one a caller can act
		// on by asking a narrower question.
		{"both exceeded reports items", ContextFabricResponseBudget{MaxItems: 1, MaxSerializedBytes: 1}, ContextFabricBudgetOverrunItems},
		// A zero budget axis is unbounded, so a caller that knows only one
		// ceiling is not forced to invent the other.
		{"zero max items disables the item axis", ContextFabricResponseBudget{MaxSerializedBytes: 1000}, ContextFabricBudgetFits},
		{"zero max bytes disables the byte axis", ContextFabricResponseBudget{MaxItems: 30}, ContextFabricBudgetFits},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := measurement.Overrun(testCase.budget); got != testCase.want {
				t.Fatalf("Overrun() = %q, want %q", got, testCase.want)
			}
			if got, want := measurement.Fits(testCase.budget), testCase.want == ContextFabricBudgetFits; got != want {
				t.Fatalf("Fits() = %v, want %v", got, want)
			}
		})
	}
}

// TestPathsAreNeverChargedAgainstTheItemBudget is CHAOS-4523's ruling stated
// as a property of the shared definition rather than of one call site: a
// result made entirely of graph-evidence provenance must fit any item budget.
func TestPathsAreNeverChargedAgainstTheItemBudget(t *testing.T) {
	result := ContextFabricInvestigationResult{Paths: make([]ContextFabricRelationshipPath, 500)}
	measurement, err := MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	if got := measurement.Overrun(ContextFabricResponseBudget{MaxItems: 1}); got != ContextFabricBudgetFits {
		t.Fatalf("500 relationship paths reported %q against MaxItems=1, want %q", got, ContextFabricBudgetFits)
	}
}

func TestContextFabricBudgetOverrunVocabularyIsClosed(t *testing.T) {
	vocabulary := ContextFabricBudgetOverrunVocabulary()
	if len(vocabulary) != ContextFabricBudgetOverrunCount {
		t.Fatalf("vocabulary length %d, want %d", len(vocabulary), ContextFabricBudgetOverrunCount)
	}
	seen := make(map[ContextFabricBudgetOverrun]struct{}, len(vocabulary))
	for _, member := range vocabulary {
		if _, duplicate := seen[member]; duplicate {
			t.Fatalf("duplicate overrun vocabulary member %q", member)
		}
		seen[member] = struct{}{}
		if !ValidContextFabricBudgetOverrun(member) {
			t.Fatalf("ValidContextFabricBudgetOverrun(%q) = false for a published member", member)
		}
	}
	if ValidContextFabricBudgetOverrun("truncated") {
		t.Fatal("ValidContextFabricBudgetOverrun accepted a non-member")
	}
}
