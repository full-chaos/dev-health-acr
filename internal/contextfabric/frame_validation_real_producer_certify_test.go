package contextfabric

// The frame-validation line's declaration is certified elsewhere only
// against events built by struct literal (frame_validation_certify_test.go,
// package contextfabric_test). That proves the declaration and the
// certifier agree with EACH OTHER; it never drives the real repair
// producers (repairCountKindCollapse, repairCompareGroupedCollapse,
// repairCompareRankingCollapse, all three reached only through
// frameRepairTable -> validateProposedFrame) or requestedJudgmentForGoals,
// so a real producer emitting a value outside its own declared vocabulary
// -- a repair added to frameRepairTable with a name nobody added to
// FrameRepairNameVocabulary, say -- ships undetected. This file exports
// every cell frame_repair_test.go already drives through the production
// interpreter (repairCells, compareRepairCells, rankingRepairCells) or the
// production repair functions directly at the attempts bound
// (repairAtTheBound, compareRepairAtTheBound, rankingRepairAtTheBound) as
// a raw production log, test-only, so the external certification pin
// (frame_validation_real_producer_eventspec_certify_test.go, package
// contextfabric_test, which can import eventspec/certify without the cycle
// this package cannot take) certifies the REAL emitted line, not a
// reconstruction of it -- the same split RunCHAOS5582ScenarioForTest
// already established for the window-continuation-decision line.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// FrameValidationRealProducerScenarios names every cell this file can run,
// in a fixed order: one per repairCells() entry, one per
// compareRepairCells() entry, and the four attempts-bound cases neither
// cell table reaches. Read from the SAME tables
// TestEveryFrameRepairDecisionHasAnExecutedDriver already walks, so a cell
// added there reaches this list without a second, independently
// maintained one.
func FrameValidationRealProducerScenarios() []string {
	names := make([]string, 0, len(repairCells())+len(compareRepairCells())+len(rankingRepairCells())+6)
	for _, tc := range repairCells() {
		names = append(names, "count_kind/"+tc.cell)
	}
	for _, tc := range compareRepairCells() {
		names = append(names, "compare_grouped/"+tc.cell)
	}
	for _, tc := range rankingRepairCells() {
		names = append(names, "compare_ranking/"+tc.cell)
	}
	for _, attempts := range []int{0, frameRepairBound} {
		names = append(names, fmt.Sprintf("count_kind_bound/%d", attempts))
		names = append(names, fmt.Sprintf("compare_grouped_bound/%d", attempts))
		names = append(names, fmt.Sprintf("compare_ranking_bound/%d", attempts))
	}
	return names
}

const frameValidationRealProducerOrgID = "org_repair_certify"

// RunFrameValidationRealProducerScenarioForTest drives ONE named scenario
// (a name FrameValidationRealProducerScenarios returned) through the real
// producer and returns the production slog JSON bytes it wrote, so the
// external certification pin can certify the ACTUAL emitted line rather
// than a struct built by hand.
func RunFrameValidationRealProducerScenarioForTest(t *testing.T, scenario string) (log []byte, orgID string) {
	t.Helper()
	orgID = frameValidationRealProducerOrgID

	for _, tc := range repairCells() {
		if "count_kind/"+tc.cell != scenario {
			continue
		}
		receipt := classAReceipt()
		tc.receipt(&receipt)
		flat := tc.flat
		if flat == nil {
			flat = []string{repairAnchorTerm}
		}
		return runFrameValidationInterpretForTest(t, receipt, tc.frame(), flat), orgID
	}
	for _, tc := range compareRepairCells() {
		if "compare_grouped/"+tc.cell != scenario {
			continue
		}
		receipt := compareGroupedReceipt()
		tc.receipt(&receipt)
		return runFrameValidationInterpretForTest(t, receipt, tc.frame(), []string{repairAnchorTerm}), orgID
	}
	for _, tc := range rankingRepairCells() {
		if "compare_ranking/"+tc.cell != scenario {
			continue
		}
		receipt := rankingReceipt()
		tc.receipt(&receipt)
		return runFrameValidationInterpretForTest(t, receipt, tc.frame(), []string{repairAnchorTerm}), orgID
	}
	for _, attempts := range []int{0, frameRepairBound} {
		if scenario == fmt.Sprintf("count_kind_bound/%d", attempts) {
			return runFrameValidationResultForTest(t, classAReceipt(), countOverNamedSubject(), repairAtTheBound(t, attempts)), orgID
		}
		if scenario == fmt.Sprintf("compare_grouped_bound/%d", attempts) {
			return runFrameValidationResultForTest(t, compareGroupedReceipt(), compareOverGroupedCohort(), compareRepairAtTheBound(t, attempts)), orgID
		}
		if scenario == fmt.Sprintf("compare_ranking_bound/%d", attempts) {
			return runFrameValidationResultForTest(t, rankingReceipt(), compareOverDiscoveredCohort(), rankingRepairAtTheBound(t, attempts)), orgID
		}
	}
	t.Fatalf("unknown FrameValidationRealProducer scenario %q", scenario)
	return nil, ""
}

// runFrameValidationInterpretForTest is interpretForRepair's own steps,
// stopping short of that function's own map-based assertions: it returns
// the configured logger's raw bytes, unread, so the caller certifies them
// against the declaration instead.
func runFrameValidationInterpretForTest(t *testing.T, receipt ModelExecutionReceipt, frame QuestionFrame, flat []string) []byte {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt.QuestionFrame = &frame
	sink := &fakeReceiptSink{}
	interpreter := RuntimeQuestionInterpreter{
		Runtime:         fakeModelRuntime{interpreted: repairInterpretation(flat), receipt: receipt},
		Sink:            sink,
		FrameTelemetry:  logs.telemetry,
		FamilyTelemetry: logs.telemetry,
		Requirements:    registryDeriver{},
	}
	if _, _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: frameValidationRealProducerOrgID}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	return []byte(logs.configured.String())
}

// runFrameValidationResultForTest builds and records a FrameValidationEvent
// from a FrameValidationResult the way validateProposedFrame's own caller
// (model_runtime.go's Interpret) does: FrameValidationEventFrom for the
// result itself, then InterpretationBoundaryFrom for the same receipt and
// proposal, read through the SAME Gate FrameValidationEventFrom already
// decided -- never Boundary's own zero value, which no real caller emits.
func runFrameValidationResultForTest(t *testing.T, receipt ModelExecutionReceipt, proposed QuestionFrame, result FrameValidationResult) []byte {
	t.Helper()
	event := FrameValidationEventFrom(proposed, result, ShapeSingleSubject, nil)
	event.Boundary = InterpretationBoundaryFrom(receipt, proposed, event.Gate)
	var buf bytes.Buffer
	NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))).
		RecordFrameValidation(context.Background(), storage.Principal{OrgID: frameValidationRealProducerOrgID}, event)
	return buf.Bytes()
}
