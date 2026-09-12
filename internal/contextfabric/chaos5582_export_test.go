package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-5582 test-only export: drives one named scenario through
// Engine.Investigate with the PRODUCTION slog JSON handler, so the external
// eventspec certification pin (package contextfabric_test, which can import
// eventspec/certify without an import cycle) judges the real emitted bytes.
// It calls the same harness the internal pins use; it re-implements nothing.
func RunCHAOS5582ScenarioForTest(t *testing.T, scenario string) (log []byte, requestID string) {
	t.Helper()
	question := validInvestigationRequest().Question
	drift := axis5582DriftedAxes()[0].time
	prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	older := continuationPrior(t, continuationOlderID, question, QuestionFamilyDiscoveredCohortRanking, "")
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior, older.ResultID: older}}
	request := continuationRequest(question)
	interpreter := freshAxisInterpreter{family: QuestionFamilyDiscoveredCohortRanking, timeContext: drift}
	switch scenario {
	case "overridden_by_receipt":
	case "agreed":
		interpreter.timeContext = TimeContext{Axis: TemporalCurrent}
	case "plural_receipts_vetoed":
		request.PriorWindowReceipts = append(request.PriorWindowReceipts, BoundSubjectReceipt{ResultID: continuationOlderID, ReceiptID: continuationReceiptID})
	case "changed_question_vetoed":
		changed := prior
		changed.Question = "What was the status of Ask Dev last quarter and what drove it?"
		store.results[prior.ResultID] = changed
	default:
		t.Fatalf("unknown CHAOS-5582 scenario %q", scenario)
	}
	run := axis5582Investigate(t, store, interpreter, request)
	if run.err != nil {
		t.Fatalf("Investigate() error = %v", run.err)
	}
	return run.log.Bytes(), run.requestID
}
