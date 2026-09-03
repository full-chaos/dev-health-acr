package contextfabric

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupedCohortItemRatioFixture mirrors testdata/grouped_cohort_item_ratio.json.
//
// The rows are rig MEASUREMENTS with a named authority. They are here as the
// EVIDENCE for why the plan's item expectation is worth logging: on real data a
// grouped answer misses that expectation in both directions and by a lot, and
// the measurement is the only thing that says so.
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
	if len(fixture.Rows) == 0 {
		t.Fatal("fixture carries no rows: every assertion below would be vacuous")
	}
	for _, row := range fixture.Rows {
		if row.Members <= 0 || row.Items <= 0 {
			t.Fatalf("fixture row %s has members=%d items=%d; both must be positive",
				row.RequestID, row.Members, row.Items)
		}
	}
	return fixture
}

// TestGroupedAnswerItemsAreNotAFunctionOfMemberCount is the fixture's whole
// point, asserted rather than left as prose in a JSON field.
//
// It records a REFUTATION. An earlier revision of this package predicted item
// totals from a measured items-per-member rate and clamped the cohort with it;
// the rig then produced, at SEVEN members, totals that overlap the ten-member
// range completely. So no clamp on member count can bound the item total, and
// any future attempt to reintroduce one has to answer this data first.
//
// The assertion is deliberately the weakest thing that still forbids the wrong
// model: the two member-count populations must OVERLAP. A rate model requires
// them to separate.
func TestGroupedAnswerItemsAreNotAFunctionOfMemberCount(t *testing.T) {
	t.Parallel()
	fixture := loadGroupedCohortItemRatios(t)

	byMembers := map[int][]int{}
	for _, row := range fixture.Rows {
		byMembers[row.Members] = append(byMembers[row.Members], row.Items)
	}
	if len(byMembers) < 2 {
		t.Fatalf("fixture covers %d distinct member counts; the claim needs at least 2 to be testable", len(byMembers))
	}

	type span struct{ lo, hi int }
	spans := map[int]span{}
	for members, items := range byMembers {
		s := span{items[0], items[0]}
		for _, n := range items {
			if n < s.lo {
				s.lo = n
			}
			if n > s.hi {
				s.hi = n
			}
		}
		spans[members] = s
	}
	overlapping := false
	for a, sa := range spans {
		for b, sb := range spans {
			if a >= b {
				continue
			}
			if sa.lo <= sb.hi && sb.lo <= sa.hi {
				overlapping = true
			}
		}
	}
	if !overlapping {
		t.Fatalf("item totals separate cleanly by member count (%v) -- if that is now true on real data, "+
			"a member-count clamp may be derivable again and this test should be re-derived rather than deleted. Authority: %s",
			spans, fixture.Authority)
	}
}

// TestPredictedItemsIsThePlansOwnArithmetic pins WHAT the prediction is, so it
// cannot quietly become a second model of the system. Every term is a field the
// plan already publishes: one item per member, plus the reserved headroom.
func TestPredictedItemsIsThePlansOwnArithmetic(t *testing.T) {
	t.Parallel()
	const members, headroom = 10, 20
	plan := AnswerPlan{
		Budget: contractsv1.ContextFabricAnswerPlanBudget{
			MaxItems: 30, SynthesisHeadroom: headroom,
		},
	}
	if got, want := PredictedItemsForPlan(plan, members), members+headroom; got != want {
		t.Fatalf("PredictedItemsForPlan = %d, want members(%d)+headroom(%d) = %d", got, members, headroom, want)
	}
	// A prediction for no cohort is absent, not zero dressed as a number.
	if got := PredictedItemsForPlan(plan, 0); got != 0 {
		t.Fatalf("PredictedItemsForPlan(0 members) = %d, want 0", got)
	}
}

// TestPlanNarrowingLineCarriesPredictedItems pins the telemetry. Mutation M3
// (deleting the "predicted_items" pair from RecordPlanNarrowing's arg list)
// SURVIVED the package before this test existed: the field was populated on the
// event and reached no sink.
//
// The prediction is only useful BESIDE the measurement, so this asserts both
// keys on ONE record, not either alone.
func TestPlanNarrowingLineCarriesPredictedItems(t *testing.T) {
	t.Parallel()
	event := PlanNarrowingEvent{
		Family:        QuestionFamilyGroupedCohortStatus,
		Stage:         contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Before:        7,
		After:         3,
		MeasuredItems: 41,
		MaxItems:      30,
		// Distinct from every other number on the event, so a record carrying
		// it cannot be satisfied by a neighbouring field. 27 against a measured
		// 41 is a real pair off the rig.
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
	if got := int(predicted.(float64)); got != event.PredictedItems {
		t.Fatalf("predicted_items = %d, want %d", got, event.PredictedItems)
	}
	measured, ok := record["measured_items"]
	if !ok {
		t.Fatal("plan narrowing line lost measured_items: predicted is only readable against it")
	}
	if got := int(measured.(float64)); got != event.MeasuredItems {
		t.Fatalf("measured_items = %d, want %d", got, event.MeasuredItems)
	}
}

// TestPlanRefusalPredictsForTheCohortItMeasured drives planRefusal ITSELF --
// the production seam -- rather than the helper it calls.
//
// The first attempt at pinning this asserted on PredictedItemsForPlan directly
// and the mutation SURVIVED: proving the helper can tell two member counts
// apart says nothing about which one the call site hands it. That is the
// "a sweep's key is the population, never the helper you happened to call"
// trap, and this test is keyed on the call site instead.
//
// members(10) and selected(5) are the real refusal shape from the rig
// (before:10 -> after:5, overlap_aware_set_cover). They must predict DIFFERENT
// totals or the assertion cannot see the mutation, so that is asserted first.
func TestPlanRefusalPredictsForTheCohortItMeasured(t *testing.T) {
	t.Parallel()
	const synthesizedMembers, declinedRetryTarget = 10, 5

	plan := &AnswerPlan{
		Family: QuestionFamilyGroupedCohortStatus,
		Budget: contractsv1.ContextFabricAnswerPlanBudget{
			MaxItems: 30, MaxSerializedBytes: 262144, SynthesisHeadroom: 20,
		},
	}
	wantPredicted := PredictedItemsForPlan(*plan, synthesizedMembers)
	forRetryTarget := PredictedItemsForPlan(*plan, declinedRetryTarget)
	if wantPredicted <= 0 {
		t.Fatalf("prediction for the synthesized cohort = %d, want positive", wantPredicted)
	}
	if wantPredicted == forRetryTarget {
		t.Fatalf("both counts predict %d; this fixture cannot see the mutation it exists to catch", wantPredicted)
	}

	telemetry := &recordingTelemetry{}
	engine := &Engine{telemetry: telemetry}
	err := engine.planRefusal(
		context.Background(), storage.Principal{OrgID: "org_predicted_call_site"}, plan,
		ResponseMeasurement{}, contractsv1.ContextFabricBudgetOverrunItems,
		false, true, contractsv1.ContextFabricNarrowingBasisOverlapAwareSetCover,
		synthesizedMembers, declinedRetryTarget, RetryDeclinedInsufficientDeadline,
	)
	if err == nil {
		t.Fatal("planRefusal returned no error; a refusal must terminate the answer")
	}
	if len(telemetry.planNarrowings) != 1 {
		t.Fatalf("recorded %d narrowing events, want exactly 1", len(telemetry.planNarrowings))
	}
	event := telemetry.planNarrowings[0]
	if !event.RefusalPlanned || event.Before != synthesizedMembers || event.After != declinedRetryTarget {
		t.Fatalf("recorded event is not the refusal driven here: RefusalPlanned=%v Before=%d After=%d",
			event.RefusalPlanned, event.Before, event.After)
	}
	if event.PredictedItems != wantPredicted {
		t.Fatalf("PredictedItems = %d, want %d (the %d-member cohort synthesis RAN against). "+
			"%d would be the prediction for the %d-member target the declined retry never synthesized",
			event.PredictedItems, wantPredicted, synthesizedMembers, forRetryTarget, declinedRetryTarget)
	}
}
