package contextfabric

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4632 wiring tests at the REAL call site.
//
// The tests in chaos4632_question_family_consensus_test.go prove the
// aggregation; these prove it actually RUNS, on every interpretation, with
// the signals the interpretation actually produced. A resolver nothing
// calls is the same defect class as a telemetry field nothing logs.

// familyTelemetrySpy records every event, verbatim.
type familyTelemetrySpy struct {
	events []QuestionFamilyResolutionEvent
}

func (s *familyTelemetrySpy) RecordQuestionFamilyResolution(_ context.Context, _ storage.Principal, event QuestionFamilyResolutionEvent) {
	s.events = append(s.events, event)
}

func groupedInterpretation() InterpretedQuestion {
	return InterpretedQuestion{
		Shape: ShapeDiscoveredCohort, RequestedJudgment: "status_and_drivers",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
}

// TestInterpretResolvesTheFamilyFromTheSAMECallsSignals is the core wiring
// pin: the sample must be built from THIS interpretation and THIS receipt,
// never from a re-interpretation or from two different calls.
func TestInterpretResolvesTheFamilyFromTheSAMECallsSignals(t *testing.T) {
	t.Parallel()
	spy := &familyTelemetrySpy{}
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.GroupKind = contractsv1.ContextFabricSubjectTeam
	receipt.QuestionFamily = QuestionFamilyGroupedCohortStatus

	interpreter := RuntimeQuestionInterpreter{
		Runtime:         fakeModelRuntime{interpreted: groupedInterpretation(), receipt: receipt},
		Sink:            &fakeReceiptSink{},
		FamilyTelemetry: spy,
	}
	if _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if len(spy.events) != 1 {
		t.Fatalf("got %d family events, want exactly 1 per interpretation", len(spy.events))
	}
	event := spy.events[0]
	// Row 1 fired on the receipt's GroupKind -- the signal is reaching
	// the precedence table, not being dropped between the two.
	if event.Family != QuestionFamilyGroupedCohortStatus {
		t.Errorf("family = %q, want grouped_cohort_status", event.Family)
	}
	// N=1 is the shipped default and MUST report source=model, never
	// model_consensus: one sample cannot corroborate itself, and claiming
	// consensus here would overstate the guarantee on every production
	// turn.
	if event.Source != QuestionFamilySourceModel {
		t.Errorf("source = %q, want model (the N=1 degrade path)", event.Source)
	}
	if event.EnsembleSize != 1 {
		t.Errorf("ensemble_size = %d, want 1", event.EnsembleSize)
	}
	if len(event.Samples) != 1 || !event.Samples[0].GroupKindSet {
		t.Errorf("samples = %+v, want one row with group_kind_set", event.Samples)
	}
	// The Shape must come from the INTERPRETATION, not the receipt.
	if event.Samples[0].Shape != ShapeDiscoveredCohort {
		t.Errorf("sample shape = %q, want discovered_cohort from the interpretation", event.Samples[0].Shape)
	}
	if event.FamilyVersion != QuestionFamilyTableVersion {
		t.Errorf("family_version = %q, want %q", event.FamilyVersion, QuestionFamilyTableVersion)
	}
}

// TestInterpretFiresTheFamilyEventEvenWhenUnclassified pins the
// DENOMINATOR at the real call site. See the matching sink test for why
// this matters: without it, "the resolver classifies nothing" and "the
// resolver never runs" are the same observation.
func TestInterpretFiresTheFamilyEventEvenWhenUnclassified(t *testing.T) {
	t.Parallel()
	spy := &familyTelemetrySpy{}
	interpreted := groupedInterpretation()
	// explicit_cohort with no named members is row 7 -> unclassified.
	interpreted.Shape = ShapeExplicitCohort

	interpreter := RuntimeQuestionInterpreter{
		Runtime:         fakeModelRuntime{interpreted: interpreted, receipt: validModelReceiptFixture(ModelOperationInterpret)},
		Sink:            &fakeReceiptSink{},
		FamilyTelemetry: spy,
	}
	if _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if len(spy.events) != 1 {
		t.Fatalf("got %d events, want 1 -- the event must fire on unclassified too", len(spy.events))
	}
	if spy.events[0].Family != QuestionFamilyUnclassified {
		t.Errorf("family = %q, want unclassified", spy.events[0].Family)
	}
}

// TestInterpretDoesNotFireTheFamilyEventOnFailure pins the other side of
// the denominator: a failed interpretation produced no signals, so there
// is nothing to resolve and nothing to report. Firing here would inflate
// the denominator with turns that never reached a model verdict, and the
// unclassified RATE -- the number the gating measurement reads -- would be
// wrong in a way nobody could see.
func TestInterpretDoesNotFireTheFamilyEventOnFailure(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		runtime fakeModelRuntime
	}{
		{
			name:    "transport failure",
			runtime: fakeModelRuntime{interpErr: errors.New("provider down"), receipt: validModelReceiptFixture(ModelOperationInterpret)},
		},
		{
			name: "output that fails Validate",
			runtime: fakeModelRuntime{
				interpreted: InterpretedQuestion{Shape: "not_a_real_shape", RequestedJudgment: "x", TimeContext: TimeContext{Axis: TemporalCurrent}},
				receipt:     validModelReceiptFixture(ModelOperationInterpret),
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			spy := &familyTelemetrySpy{}
			interpreter := RuntimeQuestionInterpreter{Runtime: testCase.runtime, Sink: &fakeReceiptSink{}, FamilyTelemetry: spy}
			if _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err == nil {
				t.Fatal("Interpret() succeeded, want an error")
			}
			if len(spy.events) != 0 {
				t.Fatalf("got %d events on a failed interpretation, want 0", len(spy.events))
			}
		})
	}
}

// TestFamilyResolutionNeverChangesTheReturnedInterpretation is the ZERO
// BEHAVIOUR CHANGE pin, at the seam where a shadow feature would leak.
//
// This slice's whole claim is that nothing is gated on the family. The
// interpretation returned to the engine must be byte-identical whatever
// the family signals say -- otherwise every downstream stage sees a
// different input and "shadow" is a false claim.
func TestFamilyResolutionNeverChangesTheReturnedInterpretation(t *testing.T) {
	t.Parallel()
	base := groupedInterpretation()

	var results []InterpretedQuestion
	for _, receiptMutation := range []func(*ModelExecutionReceipt){
		func(*ModelExecutionReceipt) {},
		func(r *ModelExecutionReceipt) { r.GroupKind = contractsv1.ContextFabricSubjectTeam },
		func(r *ModelExecutionReceipt) {
			r.ScopeAnchorTerm = "fullchaos"
			r.ScopeAnchorKind = contractsv1.ContextFabricSubjectTeam
			r.RequestedSubjectKind = contractsv1.ContextFabricSubjectProject
		},
		func(r *ModelExecutionReceipt) { r.QuestionFamily = QuestionFamilyTrend },
		func(r *ModelExecutionReceipt) { r.QuestionFamilyUnrecognized = true },
	} {
		receipt := validModelReceiptFixture(ModelOperationInterpret)
		receiptMutation(&receipt)
		interpreter := RuntimeQuestionInterpreter{
			Runtime:         fakeModelRuntime{interpreted: base, receipt: receipt},
			Sink:            &fakeReceiptSink{},
			FamilyTelemetry: &familyTelemetrySpy{},
		}
		got, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
		if err != nil {
			t.Fatalf("Interpret() error = %v", err)
		}
		results = append(results, got)
	}
	for i, got := range results {
		if got.Shape != base.Shape || got.RequestedJudgment != base.RequestedJudgment ||
			len(got.SubjectTerms) != len(base.SubjectTerms) || len(got.FactRequirements) != len(base.FactRequirements) {
			t.Fatalf("variant %d returned a DIFFERENT interpretation (%+v) than the input (%+v) -- the family resolution is not shadow", i, got, base)
		}
	}
}

// TestInterpretWithoutFamilyTelemetryStillSucceeds pins that a nil
// telemetry never breaks interpretation -- an observability dependency
// must not become a serving-path failure mode.
//
// This is NOT a licence to leave it nil in production: open.go wires it,
// and the sink test asserts the production line's bytes. The two together
// are what CHAOS-4085 was missing.
func TestInterpretWithoutFamilyTelemetryStillSucceeds(t *testing.T) {
	t.Parallel()
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: groupedInterpretation(), receipt: validModelReceiptFixture(ModelOperationInterpret)},
		Sink:    &fakeReceiptSink{},
	}
	if _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v with nil FamilyTelemetry", err)
	}
}
