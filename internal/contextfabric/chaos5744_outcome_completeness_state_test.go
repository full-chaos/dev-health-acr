package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The "context fabric plan narrowing" line's outcome_completeness_state
// field is non-empty on every assembled_result-stage line that describes a
// served document: recordCandidateNarrowing's own emission, and
// fitAssembledResult's three other exits (the immediate fit, the retry that
// fits without needing the candidate reduction, and the retry that errors)
// all read the already-computed InvestigationResult.Completeness.State off
// the document each one describes.
//
// These two tests drive the two exits that SERVE an answer -- the immediate
// fit (the majority path) and the retry that fits on its own -- and pin that
// the served line's own derived state reaches the trace, never empty.

func assembledResultNarrowingEvents(telemetry *recordingTelemetry) []PlanNarrowingEvent {
	var out []PlanNarrowingEvent
	for _, event := range telemetry.planNarrowings {
		if event.Stage == contractsv1.ContextFabricPlanNarrowingAssembledResult {
			out = append(out, event)
		}
	}
	return out
}

// TestImmediateFitNamesTheServedCompletenessState drives fitAssembledResult's
// "measured FIT" exit: a budget generous enough that the first synthesis
// pass already fits, so no retry and no candidate reduction ever run. This
// is the majority path this file's own comments describe, and it is the one
// this field was silently empty on for every served investigation before
// this change.
func TestImmediateFitNamesTheServedCompletenessState(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// 6 members + 12 claims (2 per member) + 6 candidates = 24 items,
	// comfortably inside a 30-item ceiling: the first pass fits.
	engine := outcomeCohortEngineWithCandidates(t, budgetStageCohort(6), 2, 6, budgetStageOptions(30, time.Second), &calls, telemetry)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err != nil {
		t.Fatalf("Investigate() error = %v -- the answer fits inside the ceiling", err)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times, want 1 -- a fitting first pass never retries", calls)
	}

	events := assembledResultNarrowingEvents(telemetry)
	if len(events) != 1 {
		t.Fatalf("%d assembled_result narrowing events, want exactly 1", len(events))
	}
	event := events[0]
	if event.RetryAttempted || event.OutcomeReductionApplied {
		t.Fatalf("event = %+v, want the plain fit arm (no retry, no reduction)", event)
	}
	if event.OutcomeCompletenessState == "" {
		t.Fatal("outcome_completeness_state is empty on a SERVED assembled_result line -- the served document's own already-computed state was never copied onto the event")
	}
}

// TestRetryThatFitsOnItsOwnNamesTheServedCompletenessState drives the retry
// arm where the halved cohort fits WITHOUT the candidate reduction ever
// running (fitAssembledResult's `retryOverrun == Fits` fallthrough at the
// end of the function) -- the sibling exit that built its event from
// `before`/`after` counts and a measurement, but never read the retried
// document's own completeness state either.
func TestRetryThatFitsOnItsOwnNamesTheServedCompletenessState(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// 6 members x 2 claims + 6 members + 6 candidates = 24 items against 15:
	// over. The retry halves the cohort to 3: 6 claims + 3 members + 6
	// candidates = 15, which fits the same ceiling exactly -- so the
	// candidate reduction never needs to run.
	engine := outcomeCohortEngineWithCandidates(t, budgetStageCohort(6), 2, 6, budgetStageOptions(15, time.Second), &calls, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v -- the retry alone fits inside the ceiling", err)
	}
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	if measurement.Items.Budgeted() > 15 {
		t.Fatalf("served %d budgeted items against a 15-item ceiling", measurement.Items.Budgeted())
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2 -- one bounded retry, no candidate reduction", calls)
	}

	events := assembledResultNarrowingEvents(telemetry)
	if len(events) != 1 {
		t.Fatalf("%d assembled_result narrowing events, want exactly 1", len(events))
	}
	event := events[0]
	if !event.RetryAttempted || !event.RetryFit || event.OutcomeReductionApplied {
		t.Fatalf("event = %+v, want the retry-fits-on-its-own arm (retried, fit, no reduction)", event)
	}
	if event.RefusalPlanned {
		t.Fatalf("event = %+v, want no refusal planned -- the retry served", event)
	}
	if event.OutcomeCompletenessState == "" {
		t.Fatal("outcome_completeness_state is empty on a SERVED assembled_result line -- the retried document's own already-computed state was never copied onto the event")
	}
}
