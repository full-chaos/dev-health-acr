package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// planCarryOutcomeEngine wires a real Engine whose plan carry will be
// attempted: the interpreter classifies nothing, which is the only condition
// under which a carried family may apply (applyCarriedPlan refuses to override
// a family this turn resolved for itself).
func planCarryOutcomeEngine(t *testing.T, store InvestigationResultStore, telemetry EngineTelemetry) *Engine {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	fresh := validInvestigationResult()
	return mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
	})
}

// TestPlanCarryOutcome_ReportedFromTheEngineOnHitAndOnDrift is the plan axis's
// missing denominator, tested at the CONSUMER.
//
// WHAT WAS WRONG. The plan axis published RecordPlanCarry, which fires only on
// an APPLIED carry -- a numerator with no denominator. A refusal was
// indistinguishable from a turn that never attempted a carry, and the entire
// PlanCarryOutcome miss vocabulary had no consumer at all. That is the same
// blind spot that let this axis ship with no same-question containment: an
// axis nothing reports on is an axis nobody enumerates.
//
// WHY IT DRIVES Investigate RATHER THAN THE RECORDER. A test that calls
// recordPlanCarryOutcome directly proves the sink formats a line; it cannot
// fail if the engine never calls it, which is exactly the plumbing defect this
// programme keeps rediscovering. Both arms drive the public entry point, and
// the hit arm asserts EVERY field the production emit writes -- a field
// populated on the call and never asserted is an unguarded field.
func TestPlanCarryOutcome_ReportedFromTheEngineOnHitAndOnDrift(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// priorQuestion decides whether the parent is same-question (hit) or
		// drifted (refused).
		priorQuestion func(requestQuestion string) string
		wantOutcome   PlanCarryOutcome
		// wantSourceResultID is the prior result id on a hit and empty on a
		// refusal -- asserted in BOTH directions, because a recorder that
		// always echoed the id would pass a hit-only check.
		wantSource bool
	}{
		{
			name:          "hit",
			priorQuestion: func(q string) string { return q },
			wantOutcome:   PlanCarryHit,
			wantSource:    true,
		},
		{
			name:          "refused for question drift",
			priorQuestion: func(string) string { return driftQuestion },
			wantOutcome:   PlanCarryMissQuestionDrift,
			wantSource:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := validInvestigationRequest()
			prior := carriablePlanResult("result_plan_carry_src", tc.priorQuestion(request.Question))
			request.ParentResultID = prior.ResultID
			store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}

			telemetry := &recordingTelemetry{}
			if _, err := planCarryOutcomeEngine(t, store, telemetry).Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}

			// EXACTLY ONE, not "at least one": a test that counts only what it
			// expects cannot detect a surplus, and a second emit would
			// double-count every rate computed off this line.
			if len(telemetry.planCarryOutcomes) != 1 {
				t.Fatalf("got %d plan-carry-outcome records, want exactly 1 -- this is the plan axis's only attempt counter, so a surplus corrupts every rate computed from it; records: %#v", len(telemetry.planCarryOutcomes), telemetry.planCarryOutcomes)
			}
			got := telemetry.planCarryOutcomes[0]
			if got.outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.outcome, tc.wantOutcome)
			}
			if tc.wantSource && got.sourceResultID != prior.ResultID {
				t.Errorf("sourceResultID = %q, want %q -- it is the join key tying this line to the applied-carry line for the same turn", got.sourceResultID, prior.ResultID)
			}
			if !tc.wantSource && got.sourceResultID != "" {
				t.Errorf("sourceResultID = %q on a refusal, want empty -- naming an origin the engine refused to carry from would read as provenance for a value that was never used", got.sourceResultID)
			}
			// The seed source is derived from the REQUEST, so it is
			// parent_field on both arms; asserting it here is what stops the
			// refusal arm from being explained away as "the request looked
			// different".
			if got.seedSource != CarrySeedParentField {
				t.Errorf("seedSource = %q, want %q: this request is linked ONLY by parent_result_id", got.seedSource, CarrySeedParentField)
			}
		})
	}
}

// TestPlanCarryOutcome_ReachesTheRealSink drives a real SlogEngineTelemetry
// and reads the bytes that came out, because a recording double proves the
// engine called SOMETHING, never that the shipped sink emits anything usable.
//
// The key allow-list is asserted rather than the message alone: a renamed key
// silently breaks every downstream query while the line still appears.
func TestPlanCarryOutcome_ReachesTheRealSink(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	// Info level: the line is logged at Info, so a Warn-only handler would
	// drop it and this test would pass by never seeing anything.
	sink := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	request := validInvestigationRequest()
	prior := carriablePlanResult("result_plan_carry_src", request.Question)
	request.ParentResultID = prior.ResultID
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}

	if _, err := planCarryOutcomeEngine(t, store, sink).Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "context fabric plan carry outcome") {
		t.Fatalf("no plan-carry-outcome line reached the real sink; engine output was:\n%s", logged)
	}
	// A DISTINCT message from "context fabric plan carry" (the applied-carry
	// line). Folding the two would make the attempt counter and the applied
	// counter the same number and destroy the hit rate this line publishes.
	for _, want := range []string{
		`"outcome":"` + string(PlanCarryHit) + `"`,
		`"source_result_id":"` + prior.ResultID + `"`,
		`"seed_source":"` + string(CarrySeedParentField) + `"`,
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("plan-carry-outcome line is missing %s -- a key that never reaches the sink is a field, not telemetry:\n%s", want, logged)
		}
	}
}

// TestPlanCarryOutcome_DriftRefusesTheParentAsAncestry closes the loop the
// other two arms leave open: the plan axis's drift refusal must also stop the
// refused parent being persisted as this turn's ancestry.
//
// The window and kind axes already did this; the plan axis resolves EARLIER in
// Investigate than either of them, so its refusal is written by a separate
// assignment and could have been omitted without any existing arm noticing.
// This is the arm that notices.
func TestPlanCarryOutcome_DriftRefusesTheParentAsAncestry(t *testing.T) {
	t.Parallel()

	request := validInvestigationRequest()
	prior := carriablePlanResult("result_plan_drifted", driftQuestion)
	request.ParentResultID = prior.ResultID

	store := newAncestryRecordingStore(&staticResultStore{
		results: map[string]InvestigationResult{prior.ResultID: prior},
	})
	telemetry := &recordingTelemetry{}
	result, err := planCarryOutcomeEngine(t, store, telemetry).Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	// REACHABILITY GUARD: without a drift refusal there is nothing to refuse
	// to record, and the assertion below would pass for the wrong reason.
	if len(telemetry.planCarryOutcomes) != 1 || telemetry.planCarryOutcomes[0].outcome != PlanCarryMissQuestionDrift {
		t.Fatalf("plan carry outcomes = %#v, want exactly one %q -- this turn no longer refuses for drift, so it cannot prove anything about the refused parent", telemetry.planCarryOutcomes, PlanCarryMissQuestionDrift)
	}
	// PRESENCE FIRST: an absent entry reads as "", which is not the refused
	// id, so the check below would pass on a turn that never saved.
	got, saved := store.saved[result.ResultID]
	if !saved {
		t.Fatalf("no Save recorded for %q: this test asserts what ancestry was PERSISTED, so a turn that never persisted proves nothing about it", result.ResultID)
	}
	if got == prior.ResultID {
		t.Fatalf("recorded parent %q: the plan axis refused this parent for question drift, and persisting it anyway leaves the edge a later turn walks to reach the value the gate rejected", got)
	}
	// And nothing may have been carried onto the wire from it.
	for _, entry := range result.ConfirmedStructure {
		if entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			t.Errorf("ConfirmedStructure carries %#v from a drifted parent", entry)
		}
	}
}
