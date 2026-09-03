package contextfabric

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"

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

// TestPlanNarrowingLineCarriesPredictedItems pins the telemetry half of the
// fix. Mutation M3 (deleting the "predicted_items" pair from
// SlogEngineTelemetry.RecordPlanNarrowing's arg list) SURVIVED the package
// before this test existed: the field was populated on the event and reached
// no sink, which is exactly the "a field populated on the struct and never
// logged is not telemetry" discipline chaos4085 already states for this file's
// neighbours.
//
// The prediction is only useful BESIDE the measurement, so this asserts both
// keys on ONE record, not either alone.
func TestPlanNarrowingLineCarriesPredictedItems(t *testing.T) {
	t.Parallel()
	event := PlanNarrowingEvent{
		Family:        QuestionFamilyGroupedCohortStatus,
		Stage:         contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Before:        10,
		After:         5,
		MeasuredItems: 39,
		MaxItems:      30,
		// The value under test. Distinct from every other number on the event
		// so a record carrying it cannot be satisfied by a neighbouring field.
		PredictedItems: 27,
	}
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordPlanNarrowing(
			context.Background(), storage.Principal{OrgID: "org_predicted_items_test"}, event)
	})
	if len(records) != 1 {
		t.Fatalf("emitted %d records, want exactly 1", len(records))
	}
	record := records[0]
	predicted, ok := record["predicted_items"]
	if !ok {
		t.Fatal("plan narrowing line carries no predicted_items key: the prediction is populated on the event and never reaches an operator")
	}
	if got := predicted.(float64); int(got) != event.PredictedItems {
		t.Fatalf("predicted_items = %v, want %d", got, event.PredictedItems)
	}
	// Beside, not instead of.
	measured, ok := record["measured_items"]
	if !ok {
		t.Fatal("plan narrowing line lost measured_items: predicted is only readable against it")
	}
	if got := measured.(float64); int(got) != event.MeasuredItems {
		t.Fatalf("measured_items = %v, want %d", got, event.MeasuredItems)
	}
}

// TestPredictedItemsIsForTheSynthesizedCohortNotTheDeclinedRetryTarget pins
// which member count the refusal's prediction describes. Mutation M4
// (predicting from `selected`, the count the DECLINED retry would have
// narrowed to, instead of `members`, the cohort synthesis actually ran
// against) SURVIVED the package: nothing compared the two, so a refusal could
// have published a prediction for an answer that was never assembled, sitting
// next to a measurement of one that was.
//
// Driven through PredictedItemsForPlan -- the seam planRefusal calls -- with
// the two counts from a real refusal (before=10, after=5).
func TestPredictedItemsIsForTheSynthesizedCohortNotTheDeclinedRetryTarget(t *testing.T) {
	t.Parallel()
	plan := AnswerPlan{Family: QuestionFamilyGroupedCohortStatus}
	const synthesizedMembers, declinedRetryTarget = 10, 5

	forSynthesized := PredictedItemsForPlan(plan, synthesizedMembers)
	forRetryTarget := PredictedItemsForPlan(plan, declinedRetryTarget)
	if forSynthesized <= 0 {
		t.Fatalf("prediction for the synthesized cohort = %d, want positive (the grouped profile has a measured rate)", forSynthesized)
	}
	// Non-vacuity: if the two counts predicted the SAME total, this test could
	// not tell the mutation from the fix.
	if forSynthesized == forRetryTarget {
		t.Fatalf("both member counts predict %d items; this fixture cannot distinguish them", forSynthesized)
	}

	fixture := loadGroupedCohortItemRatios(t)
	worstRatio, _ := worstMeasuredItemsPerMember(fixture)
	want := int(math.Ceil(float64(synthesizedMembers) * worstRatio))
	if forSynthesized != want {
		t.Fatalf("PredictedItemsForPlan(%d) = %d, want %d (%d members at the worst measured %.2f items/member)",
			synthesizedMembers, forSynthesized, want, synthesizedMembers, worstRatio)
	}
}
