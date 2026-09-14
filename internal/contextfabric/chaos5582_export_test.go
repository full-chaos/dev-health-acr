package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
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

// CompositionInvariantsForTest is a test-only export of compositionInvariants
// (unexported), so an external test package can enumerate the invariants
// composeAcceptedContext can produce rather than hand-listing them again.
func CompositionInvariantsForTest() []string { return compositionInvariants() }

// RunCompositionFailureForTest is a test-only export, same purpose as
// RunCHAOS5582ScenarioForTest above: it emits ONE real production
// window-continuation-decision line through SlogEngineTelemetry -- the exact
// production sink, never a hand-typed fixture line -- with
// composition_failed_invariant set to the given invariant, so the external
// certification pin (package contextfabric_test) judges the real emitted
// bytes for every value composeAcceptedContext can produce.
func RunCompositionFailureForTest(t *testing.T, invariant string) (log []byte, requestID string) {
	t.Helper()
	requestID = "req_" + "00000000000000000000000000000001"
	ctx := observability.WithRequestID(context.Background(), requestID)
	decision := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
	decision.CompositionOutcome = CompositionInvalid
	decision.CompositionFailedInvariant = invariant
	var buf bytes.Buffer
	SlogEngineTelemetry{logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}.
		RecordWindowContinuationDecision(ctx, acceptancePrincipal(), decision)
	return buf.Bytes(), requestID
}
