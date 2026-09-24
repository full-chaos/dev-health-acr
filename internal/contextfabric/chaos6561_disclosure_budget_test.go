package contextfabric

import (
	"fmt"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-6561 review round 1, P1 (executed repro): the cohort-narrowing
// sentence must be part of the document stage 3 MEASURES, so an answer that
// does not fit with it takes the normal narrowing path -- never a late
// refusal at the final budget assertion for a document stage 3 called a fit.
//
// The repro: 11 projects, the member limit clamped 20 -> 10, an 8,192-byte
// ceiling. Without the sentence the served document fits; with it, it does
// not. Before the fix, stage 3 measured the document BEFORE the coverage
// display labels were applied (engine.go's decisive path), logged "fits", and
// the final assertion then measured the labelled document and refused it.

// chaos6561WithCeiling returns a copy of result whose published plan names the
// given byte ceiling, so a measurement of it is the measurement of the same
// document served under that ceiling.
func chaos6561WithCeiling(result InvestigationResult, ceiling int64) InvestigationResult {
	if result.AnswerPlan != nil {
		plan := *result.AnswerPlan
		plan.Budget.MaxSerializedBytes = ceiling
		result.AnswerPlan = &plan
	}
	return result
}

func TestCHAOS6561DisclosureNeverRefusesAnAnswerThatCanFit(t *testing.T) {
	t.Parallel()
	maxItems := chaos6561HeadroomItems(t, 10)
	const ceiling = int64(contractsv1.ContextFabricSerializedBytesMin)

	// Control: the same investigation with no byte ceiling serves 10 members
	// on one synthesis call, with the one-step disclosure.
	probe, calls, err := chaos6561InvestigateWithin(t, 11, 0, maxItems, 0, nil)
	if err != nil {
		t.Fatalf("control Investigate() error = %v", err)
	}
	if calls != 1 || probe.Cohort == nil || len(probe.Cohort.Members) != 10 || len(chaos6561NarrowingLimitations(probe)) != 1 {
		t.Fatalf("control: calls=%d cohort=%v sentences=%q, want 1 call, 10 members, one sentence", calls, probe.Cohort, chaos6561NarrowingLimitations(probe))
	}
	// The fixture must straddle the ceiling: fits without the sentence,
	// does not fit with it. Otherwise this test proves nothing.
	without := chaos6561WithCeiling(probe, ceiling)
	without.Limitations = withoutCohortNarrowingDisclosure(probe.Limitations)
	with := chaos6561WithCeiling(probe, ceiling)
	noSentence, err := contractsv1.MeasureContextFabricResponse(without)
	if err != nil {
		t.Fatal(err)
	}
	withSentence, err := contractsv1.MeasureContextFabricResponse(with)
	if err != nil {
		t.Fatal(err)
	}
	if noSentence.Bytes > ceiling || withSentence.Bytes <= ceiling {
		t.Fatalf("fixture defect: must straddle the %d-byte ceiling, got no_sentence=%d with_sentence=%d", ceiling, noSentence.Bytes, withSentence.Bytes)
	}

	telemetry := &recordingTelemetry{}
	served, calls, err := chaos6561InvestigateWithin(t, 11, 0, maxItems, ceiling, telemetry)
	if err != nil {
		t.Fatalf("Investigate() under a %d-byte ceiling refused an answer that has a fitting narrower form (no_sentence=%d, with_sentence=%d, calls=%d): %v; stage-3 events=%s",
			ceiling, noSentence.Bytes, withSentence.Bytes, calls, err, chaos6561StageMeasurements(telemetry))
	}
	measurement, err := contractsv1.MeasureContextFabricResponse(served)
	if err != nil {
		t.Fatal(err)
	}
	if measurement.Bytes > ceiling {
		t.Fatalf("served document measures %d bytes over the %d-byte ceiling", measurement.Bytes, ceiling)
	}
	if served.Cohort == nil {
		t.Fatal("served answer carries no cohort")
	}
	// The sentence is INSIDE the measured document and describes the member
	// set actually served -- the narrowing path ran, so it names every step.
	sentences := chaos6561NarrowingLimitations(served)
	if len(sentences) != 1 {
		t.Fatalf("served cohort-narrowing sentences = %q, want exactly one", sentences)
	}
	members := len(served.Cohort.Members)
	if !strings.HasPrefix(sentences[0], fmt.Sprintf("This answer lists %d of the 11 project subjects found", members)) {
		t.Fatalf("sentence %q does not describe the %d served members", sentences[0], members)
	}
	// No served number changed: the stage-1 clamp is still 20 -> 10, and any
	// further cut is a recorded stage-3 step.
	steps := chaos6561MemberSteps(served.AnswerPlan)
	if len(steps) == 0 || steps[0].Stage != contractsv1.ContextFabricPlanNarrowingCardinality || steps[0].Before != 20 || steps[0].After != 10 {
		t.Fatalf("plan member steps = %+v, want the cardinality clamp 20 -> 10 first", steps)
	}
	// Every stage-3 measurement that called its document a fit measured the
	// SERVED bytes: the fit decision and the served document are one document.
	for _, event := range telemetry.planNarrowings {
		if event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult && event.Overrun == contractsv1.ContextFabricBudgetFits && event.MeasuredBytes != measurement.Bytes {
			t.Fatalf("stage-3 fit measured %d bytes, served document is %d bytes -- they must be one document", event.MeasuredBytes, measurement.Bytes)
		}
	}
	// The Info event carries the final disposition and the served measurement.
	if len(telemetry.cohortNarrowingDisclosures) != 1 {
		t.Fatalf("disclosure events = %+v, want exactly one", telemetry.cohortNarrowingDisclosures)
	}
	event := telemetry.cohortNarrowingDisclosures[0]
	if event.Outcome != CohortNarrowingDisclosed || event.Served != members || event.Declared != 11 ||
		event.Disposition != CohortNarrowingDispositionServed ||
		event.AssertedBytes != measurement.Bytes || event.AssertedItems != measurement.Items.Budgeted() {
		t.Fatalf("disclosure event = %+v, want disclosed 11 -> %d, disposition served, measured %d items / %d bytes",
			event, members, measurement.Items.Budgeted(), measurement.Bytes)
	}
}

// chaos6561StageMeasurements renders the stage-3 events for a failure message.
func chaos6561StageMeasurements(telemetry *recordingTelemetry) string {
	var parts []string
	for _, event := range telemetry.planNarrowings {
		parts = append(parts, fmt.Sprintf("%s/%s items=%d bytes=%d", event.Stage, event.Overrun, event.MeasuredItems, event.MeasuredBytes))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}
