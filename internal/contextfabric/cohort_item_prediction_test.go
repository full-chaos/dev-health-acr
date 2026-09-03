package contextfabric

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// groupedCohortItemRatioFixture mirrors testdata/grouped_cohort_item_ratio.json.
//
// The rows are rig MEASUREMENTS with a named authority, not values chosen here,
// which is the point: the expectation this file asserts is DERIVED from them at
// run time. A constant a reviewer could edit alongside the data would not be an
// oracle (it would agree with whatever the data said), so nothing below hard
// codes a ratio, a member count or an item count.
type groupedCohortItemRatioFixture struct {
	Authority string `json:"authority"`
	MaxItems  int    `json:"max_items_in_force"`
	Rows      []struct {
		RequestID string `json:"request_id"`
		Members   int    `json:"members"`
		Items     int    `json:"items"`
		Overrun   string `json:"overrun"`
	} `json:"rows"`
}

func loadGroupedCohortItemRatios(t *testing.T) groupedCohortItemRatioFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/grouped_cohort_item_ratio.json")
	if err != nil {
		t.Fatalf("read grouped cohort item ratio fixture: %v", err)
	}
	var fixture groupedCohortItemRatioFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode grouped cohort item ratio fixture: %v", err)
	}
	// Non-vacuity. A fixture that decoded to zero rows, or whose rows carry a
	// zero member count, would make every ratio below either absent or a
	// division by zero -- and an empty maximum reads as "nothing overran",
	// which is exactly the false green this test exists to prevent.
	if len(fixture.Rows) == 0 {
		t.Fatal("fixture carries no rows: the derived expectation would be vacuous")
	}
	if fixture.MaxItems <= 0 {
		t.Fatalf("fixture max_items_in_force = %d, want a positive ceiling", fixture.MaxItems)
	}
	for _, row := range fixture.Rows {
		if row.Members <= 0 || row.Items <= 0 {
			t.Fatalf("fixture row %s has members=%d items=%d; both must be positive",
				row.RequestID, row.Members, row.Items)
		}
	}
	return fixture
}

// worstMeasuredItemsPerMember is the largest items-per-member ratio the rig
// produced. The WORST case is the right input to a clamp: a clamp sized on the
// mean refuses about half the time, which is the observed 1-of-7 fit rate.
func worstMeasuredItemsPerMember(fixture groupedCohortItemRatioFixture) (float64, string) {
	worst, worstID := 0.0, ""
	for _, row := range fixture.Rows {
		if ratio := float64(row.Items) / float64(row.Members); ratio > worst {
			worst, worstID = ratio, row.RequestID
		}
	}
	return worst, worstID
}

// TestGroupedCohortMemberClampSurvivesMeasuredItemsPerMember pins the property
// the rig says is broken: the stage-1 member clamp must leave room for the
// items synthesis ADDS PER MEMBER, so that a full cohort's predicted item count
// still fits the budget without needing the stage-3 re-synthesis retry.
//
// It is red on the parent because planSynthesisHeadroom reserves a CONSTANT 20
// items regardless of member count, so the clamp admits 30-20=10 members while
// the rig measures up to 3.90 items per member -- a predicted 39 against a
// ceiling of 30. The retry that would correct it is declined
// (retry_declined=insufficient_deadline) on every observed refusal, so the
// prediction is the only thing standing between this family and a 413.
func TestGroupedCohortMemberClampSurvivesMeasuredItemsPerMember(t *testing.T) {
	t.Parallel()
	fixture := loadGroupedCohortItemRatios(t)
	worstRatio, worstID := worstMeasuredItemsPerMember(fixture)

	plan := PlanAnswer(PlanAnswerInput{
		Family: QuestionFamilyOutcome{
			Family: QuestionFamilyGroupedCohortStatus,
			Source: QuestionFamilySourceModel,
		},
		Budget:           ResponseBudget{MaxItems: fixture.MaxItems},
		MaxCohortMembers: 50,
	})
	if plan.Budget.MaxMembers <= 0 {
		t.Fatalf("MaxMembers = %d, want a positive clamp to predict against", plan.Budget.MaxMembers)
	}

	predicted := int(math.Ceil(float64(plan.Budget.MaxMembers) * worstRatio))
	if predicted > fixture.MaxItems {
		t.Fatalf("stage-1 clamp admits %d members; at the worst measured %.2f items/member (%s) that predicts %d items against a %d-item budget. "+
			"The clamp must be derived from items PER MEMBER, not from a constant headroom: %s",
			plan.Budget.MaxMembers, worstRatio, worstID, predicted, fixture.MaxItems, fixture.Authority)
	}
}

// TestGroupedCohortClampStillAnswersTheQuestion is the attribution control for
// the test above, and it runs in the same file on purpose. "Predict fewer
// items" is trivially satisfiable by clamping to one member, which fits every
// budget and answers no grouped question at all -- decision D2's "for each
// team is the question's own words". So the fix is only correct if it is
// BOTH under the ceiling and still a cohort.
//
// The floor is derived from the fixture (every measured row ran a cohort of
// the same size, and half of that is the smallest thing still recognisable as
// "each team"), never picked here.
func TestGroupedCohortClampStillAnswersTheQuestion(t *testing.T) {
	t.Parallel()
	fixture := loadGroupedCohortItemRatios(t)
	smallestMeasuredCohort := 0
	for _, row := range fixture.Rows {
		if smallestMeasuredCohort == 0 || row.Members < smallestMeasuredCohort {
			smallestMeasuredCohort = row.Members
		}
	}
	floor := smallestMeasuredCohort / 2

	plan := PlanAnswer(PlanAnswerInput{
		Family: QuestionFamilyOutcome{
			Family: QuestionFamilyGroupedCohortStatus,
			Source: QuestionFamilySourceModel,
		},
		Budget:           ResponseBudget{MaxItems: fixture.MaxItems},
		MaxCohortMembers: 50,
	})
	if plan.Budget.MaxMembers < floor {
		t.Fatalf("MaxMembers = %d, below the %d-member floor derived from the measured cohorts: "+
			"a clamp that buys its fit by shrinking the cohort to nothing has answered a different question",
			plan.Budget.MaxMembers, floor)
	}
	if plan.Budget.NarrowingBasis != contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical {
		t.Fatalf("NarrowingBasis = %q, want the declared stage-1 order to survive the clamp change",
			plan.Budget.NarrowingBasis)
	}
}
