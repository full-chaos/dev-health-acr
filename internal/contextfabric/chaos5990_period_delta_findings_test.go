package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"
)

// One test per executed finding of the review of this comparison. Each is
// written against findingsRun/findingsAsOf so the same file runs unchanged
// against the tree the finding was raised on (red) and the fixed tree
// (green).

func findingsTeam() SubjectRef {
	return SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1", Label: "Team One"}
}

func findingsFrame() QuestionFrame {
	return QuestionFrame{Obligations: []AnswerObligation{ObligationPeriodDelta}}
}

// Finding 1: the prior read must request the composition's fact kinds, and
// the event must report what was requested.
func TestFinding1_PriorReadRequestsTheCompositionsKinds(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}
	findingsRun(engine, findingsFrame(), []SubjectRef{findingsTeam()}, nil, time.Now())
	requested := map[FactKind]bool{}
	for _, requirement := range reader.calls[0].Requirements {
		requested[requirement.Kind] = true
	}
	want := statusCategoryFactKindComposition[SubjectTeam]
	if len(requested) != len(want) {
		t.Fatalf("requested kinds %v, want the composition's %v", requested, want)
	}
	for _, kind := range want {
		if !requested[kind] {
			t.Errorf("composed kind %q was not requested", kind)
		}
	}
	if got := telemetry.periodDeltaCompositions[0].ComposedKinds; len(got) != len(requested) {
		t.Errorf("event reports %v but the read requested %v", got, requested)
	}
}

// Finding 2: an observed-time request anchors to its own instant, not a clock.
func TestFinding2_ObservedTimeAnchorsToItsInstant(t *testing.T) {
	t.Parallel()
	wall := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	instant := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	got := findingsAsOf(TimeContext{Axis: TemporalObservedTime, AsOf: &instant}, wall)
	if !got.Equal(instant) {
		t.Fatalf("observed_time as-of = %v, want the request's %v", got, instant)
	}
}

// Finding 3: a subject with no current fact still gets a served typed
// outcome and an event.
func TestFinding3_NoCurrentFactIsDisclosed(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{bundle: CanonicalFactBundle{Facts: []CanonicalFact{
		periodDeltaHealthFact(findingsTeam(), map[string]FactValue{"severity": StringFactValue("high")}),
	}}}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}
	served := findingsRun(engine, findingsFrame(), []SubjectRef{findingsTeam()}, nil, time.Now())
	if len(telemetry.periodDeltaCompositions) != 1 {
		t.Fatalf("events = %d, want 1", len(telemetry.periodDeltaCompositions))
	}
	if telemetry.periodDeltaCompositions[0].TransitionCounts[PeriodDeltaTransitionUnknownCurrent] != 1 {
		t.Errorf("event counts = %v, want unknown_current=1", telemetry.periodDeltaCompositions[0].TransitionCounts)
	}
	if len(served) != 1 || factValueString(served[0].Fields["period_delta_transition"]) != string(PeriodDeltaTransitionUnknownCurrent) {
		t.Errorf("served = %+v, want one fact carrying unknown_current", served)
	}
}

// Finding 4: a failed prior read is a typed served outcome and an event.
func TestFinding4_FailedPriorReadIsDisclosed(t *testing.T) {
	t.Parallel()
	reader := &recordingFactReader{err: errors.Join(context.DeadlineExceeded)}
	telemetry := &recordingTelemetry{}
	engine := &Engine{facts: reader, telemetry: telemetry}
	facts := []CanonicalFact{periodDeltaHealthFact(findingsTeam(), map[string]FactValue{"severity": StringFactValue("low")})}
	served := findingsRun(engine, findingsFrame(), []SubjectRef{findingsTeam()}, facts, time.Now())
	if len(telemetry.periodDeltaCompositions) != 1 {
		t.Fatalf("events = %d, want 1", len(telemetry.periodDeltaCompositions))
	}
	if got := factValueString(served[0].Fields["period_delta_transition"]); got != "prior_read_failed" {
		t.Errorf("served transition = %q, want prior_read_failed", got)
	}
}
