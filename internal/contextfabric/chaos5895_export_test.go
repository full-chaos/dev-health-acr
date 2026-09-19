package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
