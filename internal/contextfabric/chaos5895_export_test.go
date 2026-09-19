package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// RunQuestionWindowScenarioForTest runs one question-window cell through
// Engine.Investigate and returns the bytes the production slog JSON handler
// wrote, so the external certification pin judges the real emitted line.
// Scenarios: "threaded_window_only", "threaded_kind_receipt",
// "other_parent_vetoed".
func RunQuestionWindowScenarioForTest(t *testing.T, scenario string) (log []byte, requestID string) {
	t.Helper()
	cell := questionWindowCell{Question: questionIdentical, Carrier: carrierCurrent, Fresh: contractsv1.ContextFabricTemporalRange}
	switch scenario {
	case "threaded_window_only":
		cell.Parent, cell.Selection = ContinuationParentWindowReceiptResult, selectionEmptyHints
	case "threaded_kind_receipt":
		cell.Parent, cell.Selection = ContinuationParentWindowReceiptResult, selectionKindReceipt
	case "other_parent_vetoed":
		cell.Parent, cell.Selection = ContinuationParentOtherResult, selectionNone
	default:
		t.Fatalf("unknown question-window scenario %q", scenario)
	}
	_, run := questionWindowRun(t, cell)
	if run.err != nil {
		t.Fatalf("Investigate() error = %v", run.err)
	}
	return run.log.Bytes(), run.requestID
}

// RunRememberedWindowAxisScenarioForTest drives a three-turn chain through the
// engine -- window offered, window redeemed, the identical question continued
// from the answered turn with the model reading it on another axis -- and
// returns the third turn's production slog output.
func RunRememberedWindowAxisScenarioForTest(t *testing.T) (log []byte, orgID, requestID string) {
	t.Helper()
	h := newNeedTurnHarness(t, nil)
	_, two, _ := windowLedgerChain(t, h, "remembered_axis_certify")
	h.historical = true
	buf := swapToJSONLedgerTelemetry(h)
	request := continuingNeedTurn(needTurnRequest("request_need_remembered_axis_three", false), two.result.ResultID)
	requestID = "req_5895" + "000000000000000000000000000a"
	if _, err := h.engine.Investigate(observability.WithRequestID(context.Background(), requestID), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return buf.Bytes(), acceptancePrincipal().OrgID, requestID
}
