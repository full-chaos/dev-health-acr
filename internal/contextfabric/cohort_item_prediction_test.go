package contextfabric

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

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
// totals from a measured items-per-member RATE and clamped the cohort with it.
// If that model held, a 30% smaller cohort would produce a proportionally
// smaller answer. It does not.
//
// The assertion is the one a rate model actually fails. Codex round 1 finding 4
// showed the first version -- "the two populations must OVERLAP" -- was too
// weak: buckets [70,170] at seven members and [100,200] at ten overlap while
// still carrying a large per-member component, so the fixture could drift to
// data that does not support the conclusion while the test stayed green. What a
// proportional model REQUIRES is that the smaller cohort's mean scale down by
// the member ratio; this asserts the observed means are far closer together
// than that, which is the claim the branch actually rests on.
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
	mean := func(xs []int) float64 {
		total := 0
		for _, x := range xs {
			total += x
		}
		return float64(total) / float64(len(xs))
	}
	// Compare the smallest and largest cohort sizes present.
	small, large := 0, 0
	for members := range byMembers {
		if small == 0 || members < small {
			small = members
		}
		if members > large {
			large = members
		}
	}
	if small == large {
		t.Fatal("smallest and largest cohort sizes are equal; the comparison below is vacuous")
	}
	smallMean, largeMean := mean(byMembers[small]), mean(byMembers[large])

	// Bind the BUDGET axis. Codex round 2 finding: the means comparison alone
	// ignores max_items_in_force entirely, so editing the fixture's ceiling
	// from 30 to 45 left this test green while destroying the conclusion --
	// at 45 every recorded measurement fits, and a fixture in which nothing
	// overruns cannot support an argument about what to do when things
	// overrun. The refutation needs a SMALLER cohort that still exceeded the
	// ceiling: that is the observation a member-count clamp promises to
	// prevent and did not.
	if fixture.MaxItems <= 0 {
		t.Fatalf("fixture max_items_in_force = %d, want the positive ceiling these rows were measured against", fixture.MaxItems)
	}
	overBudgetAtSmall := 0
	for _, items := range byMembers[small] {
		if items > fixture.MaxItems {
			overBudgetAtSmall++
		}
	}
	if overBudgetAtSmall == 0 {
		t.Fatalf("no %d-member measurement exceeds the declared %d-item ceiling. The anti-clamp conclusion "+
			"rests on a NARROWED cohort still overrunning; a fixture where the small cohort always fits is "+
			"consistent with a clamp working, not with one being underivable. Authority: %s",
			small, fixture.MaxItems, fixture.Authority)
	}

	// What a per-member rate model predicts for the smaller cohort, if the
	// larger cohort's observed mean were entirely per-member cost.
	proportional := largeMean * float64(small) / float64(large)
	// The midpoint between "totals scale with members" (proportional) and
	// "totals do not move at all" (largeMean). Landing above it means the
	// member-independent term dominates, which is the branch's claim.
	midpoint := (proportional + largeMean) / 2
	if smallMean <= midpoint {
		t.Fatalf("at %d members the mean total is %.1f; a proportional model predicts %.1f and a fully "+
			"member-independent one predicts %.1f. %.1f is on the proportional side of the %.1f midpoint, so "+
			"this data does NOT refute a per-member rate and the branch's anti-clamp conclusion is unsupported. "+
			"Re-derive the design rather than relaxing this test. Authority: %s",
			small, smallMean, proportional, largeMean, smallMean, midpoint, fixture.Authority)
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

// TestSingletonRefusalPredictsForItsOneMember pins codex round 1 finding 1.
//
// narrowSynthesisInput's trivial-cohort return used to carry the ZERO value for
// Before/After, and the declined-retry path forwards those straight into
// planRefusal. A one-member cohort that synthesis had genuinely measured an
// answer against therefore published `before:0, after:0` and, once this branch
// added the prediction, `predicted_items:0` beside a real measured count.
//
// Driven through narrowSynthesisInput itself, because the loss happened there
// and a test that hand-supplies the counts (as the first M4 pin did) cannot see
// a caller-side count loss.
func TestSingletonRefusalPredictsForItsOneMember(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("only_member")
	params := synthesisAssemblyParams{Graph: GraphContext{Cohort: cohort}, Facts: CanonicalFactBundle{}}
	result := narrowSynthesisInput(params, &AnswerPlan{})

	if result.Narrow {
		t.Fatal("a one-member cohort cannot narrow; fixture is wrong")
	}
	if result.Before != 1 || result.After != 1 {
		t.Fatalf("Before/After = %d/%d for a one-member cohort, want 1/1: the count the refusal and its "+
			"prediction are derived from must describe the cohort that actually exists", result.Before, result.After)
	}

	plan := AnswerPlan{
		Family: QuestionFamilyGroupedCohortStatus,
		Budget: contractsv1.ContextFabricAnswerPlanBudget{MaxItems: 30, SynthesisHeadroom: 20},
	}
	if got, want := PredictedItemsForPlan(plan, result.Before), 1+20; got != want {
		t.Fatalf("prediction for the singleton = %d, want %d", got, want)
	}
	// The control: zero members must still predict nothing, so the fix above
	// cannot have been made by making PredictedItemsForPlan return a floor.
	if got := PredictedItemsForPlan(plan, 0); got != 0 {
		t.Fatalf("PredictedItemsForPlan(0) = %d, want 0", got)
	}
}

// TestEveryAssembledResultEventCarriesAPrediction pins codex round 1 findings 2
// and 3 together, by the POPULATION rather than by the sites I happened to
// remember: every construction of an assembled_result PlanNarrowingEvent in the
// stage-3 file must assign PredictedItems.
//
// Findings 2 and 3 were both "a site that sets MeasuredItems and not
// PredictedItems". Enumerating sites by hand is what missed them the first
// time, so this reads the source and fails on any site the enumeration does not
// cover -- including sites added after this test was written.
func TestEveryAssembledResultEventCarriesAPrediction(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("chaos4636_budget_stage3.go")
	if err != nil {
		t.Fatalf("read stage-3 source: %v", err)
	}
	text := string(src)

	constructions := strings.Count(text, "PlanNarrowingEventFrom(")
	predictions := strings.Count(text, ".PredictedItems = PredictedItemsForPlan(")
	// Positive control: if the file stops containing either shape, this test is
	// measuring nothing and must say so rather than pass.
	if constructions == 0 {
		t.Fatal("no PlanNarrowingEventFrom( constructions found: this test is anchored to a shape that no longer exists")
	}
	if predictions == 0 {
		t.Fatal("no PredictedItems assignments found: the prediction is not populated anywhere in stage 3")
	}
	if predictions != constructions {
		t.Fatalf("stage 3 constructs %d assembled_result narrowing events but assigns PredictedItems only %d times. "+
			"Every event that carries measured_items must carry the prediction beside it, or an operator reads a "+
			"measurement against predicted_items:0 and concludes the plan expected nothing.",
			constructions, predictions)
	}
}

// TestRetryEventPredictsForTheRetriedCohort pins the count the RETRY event's
// prediction is derived from. My own mutation battery found this gap: deleting
// any prediction assignment is caught by the population test above, but
// SWAPPING the retry event's count from `after` to `before` survived it, because
// counting assignments says nothing about which value each one passes.
//
// That is the third instance on this branch of the same class (M4 at the
// refusal seam, and the first M4 pin that asserted on the helper instead of the
// call site). The population test and this one are complements: one forbids an
// omission, the other forbids a wrong argument.
//
// Driven through a REAL bounded retry (6 members overrun a 12-item budget,
// halved to 3 and re-synthesized), so the counts come from production.
func TestRetryEventPredictsForTheRetriedCohort(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, budgetStageOptions(12, time.Second), &calls, telemetry)

	result, err := engine.Investigate(context.Background(),
		storage.Principal{OrgID: "org_retry_prediction"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2 (one bounded retry) -- this fixture must actually retry", calls)
	}
	if result.AnswerPlan == nil {
		t.Fatal("served result carries no plan; cannot read the headroom the prediction is built from")
	}
	headroom := result.AnswerPlan.Budget.SynthesisHeadroom

	var retryEvent *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		if telemetry.planNarrowings[index].RetryAttempted {
			retryEvent = &telemetry.planNarrowings[index]
		}
	}
	if retryEvent == nil {
		t.Fatal("no narrowing event with RetryAttempted was recorded; the retry path was not exercised")
	}
	// Non-vacuity: the two candidate counts must differ, or this assertion
	// cannot distinguish the correct argument from the wrong one.
	if retryEvent.Before == retryEvent.After {
		t.Fatalf("Before == After == %d; this fixture cannot see the mutation it exists to catch", retryEvent.Before)
	}

	wantRetried := retryEvent.After + headroom
	wrongOriginal := retryEvent.Before + headroom
	if retryEvent.PredictedItems != wantRetried {
		t.Fatalf("retry event PredictedItems = %d, want %d (After=%d + headroom=%d). %d would be the prediction "+
			"for the PRE-narrowing cohort (Before=%d), but this event's measured_items describes the "+
			"re-synthesized answer, which ran against the narrowed set",
			retryEvent.PredictedItems, wantRetried, retryEvent.After, headroom, wrongOriginal, retryEvent.Before)
	}
}

// TestFitEventPredictsForTheLiveCohort closes the FIT half of the shape sweep.
// Codex round 2 found (and I executed) that swapping the FIT path's
// `cohortMemberCount(params.Graph.Cohort)` for `plan.Budget.MaxMembers` — a
// CONFIGURED CAP rather than the cohort that was actually answered — survived
// the whole package. The existing FIT test reads existence, overrun and
// measurement, never the prediction.
func TestFitEventPredictsForTheLiveCohort(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// 3 members x 1 claim = 6 items, comfortably inside a 30-item budget, so
	// this is the FIT path and no narrowing runs.
	engine := budgetStageEngine(t, budgetStageCohort(3), 1, budgetStageOptions(30, time.Second), &calls, telemetry)

	result, err := engine.Investigate(context.Background(),
		storage.Principal{OrgID: "org_fit_prediction"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.AnswerPlan == nil {
		t.Fatal("served result carries no plan")
	}
	headroom := result.AnswerPlan.Budget.SynthesisHeadroom
	capped := result.AnswerPlan.Budget.MaxMembers

	var fit *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		if e := telemetry.planNarrowings[index]; e.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult &&
			e.Overrun == contractsv1.ContextFabricBudgetFits {
			fit = &telemetry.planNarrowings[index]
		}
	}
	if fit == nil {
		t.Fatal("no recorded FIT event; this fixture did not exercise the fit path")
	}
	// Non-vacuity: the live cohort and the configured cap must DIFFER, or the
	// mutation this test exists to catch is invisible.
	if capped == 3 {
		t.Fatalf("MaxMembers == the cohort size (3); this fixture cannot distinguish the live cohort from the cap")
	}
	if want := 3 + headroom; fit.PredictedItems != want {
		t.Fatalf("FIT PredictedItems = %d, want %d (live cohort 3 + headroom %d). %d would be the prediction "+
			"for the configured cap MaxMembers=%d, which is not what was answered",
			fit.PredictedItems, want, headroom, capped+headroom, capped)
	}
}

// TestRetryFailureEventPredictsForTheFirstSynthesisCohort closes the last site
// in the sweep. Codex round 2 found (and I executed) that swapping this event's
// `before` for `after` survived: its measurement belongs to the FIRST
// synthesis, taken against the pre-narrowing cohort, so predicting for the
// retry cohort pairs a measurement of one cohort with an expectation for
// another. The existing path-3 test asserts Before/After and never the
// prediction.
func TestRetryFailureEventPredictsForTheFirstSynthesisCohort(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := budgetStageEngine(t, chaos4809OverlappingGroupedCohort(), 20, budgetStageOptions(20, time.Second), &calls, telemetry)
	attempts := 0
	engine.synthesizer = chaos4809FailOnSecondCall(engine.synthesizer, &attempts)

	if _, err := engine.Investigate(context.Background(),
		storage.Principal{OrgID: "org_retry_failure_prediction"}, validInvestigationRequestWithConfirmedWindow()); err == nil {
		t.Fatal("Investigate() returned no error; this fixture must fail its SECOND synthesis")
	}
	var failed *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		if telemetry.planNarrowings[index].RetryFailed {
			failed = &telemetry.planNarrowings[index]
		}
	}
	if failed == nil {
		t.Fatal("no RetryFailed narrowing event recorded; the retry-failure path was not exercised")
	}
	if failed.Before == failed.After {
		t.Fatalf("Before == After == %d; this fixture cannot see the mutation it exists to catch", failed.Before)
	}
	// headroom is not on the event, so derive it from the site's own contract
	// and check the OTHER candidate would have produced a different number.
	headroom := failed.PredictedItems - failed.Before
	if wrong := failed.After + headroom; failed.PredictedItems == wrong {
		t.Fatalf("PredictedItems = %d is consistent with BOTH Before=%d and After=%d; the assertion cannot discriminate",
			failed.PredictedItems, failed.Before, failed.After)
	}
	if failed.PredictedItems != failed.Before+headroom {
		t.Fatalf("retry-failure event PredictedItems = %d, want Before(%d)+headroom(%d) = %d: this event's "+
			"measured_items describes the FIRST synthesis, which ran against the pre-narrowing cohort",
			failed.PredictedItems, failed.Before, headroom, failed.Before+headroom)
	}
}
