package contextfabric_test

// The window continuation decision line's axis-authority fields, certified
// against the eventspec declaration from bytes the production slog handler
// wrote during a real Investigate call. Every scenario differs from every
// other in at least one asserted value.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestQuestionWindow_TheDecisionLineCertifiesAgainstItsSpecification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario string
		want     map[string]any
	}{
		{"threaded_window_only", map[string]any{
			"continuation_disposition": "applied", "decision_reason": "none",
			"parent_reference": "window_receipt_result", "question_window_confirmed": true,
			"carrier_read": "read", "interpreted_axis": "range", "carried_axis": "current", "executed_axis": "current",
			"interpreted_axis_outcome": "overridden_by_receipt", "refusal_basis": "none",
		}},
		{"threaded_kind_receipt", map[string]any{
			"continuation_disposition": "not_applicable", "decision_reason": "not_window_only",
			"parent_reference": "window_receipt_result", "question_window_confirmed": true,
			"carrier_read": "read", "interpreted_axis": "range", "carried_axis": "current", "executed_axis": "current",
			"interpreted_axis_outcome": "overridden_by_receipt", "refusal_basis": "none",
		}},
		{"other_parent_vetoed", map[string]any{
			"continuation_disposition": "not_applicable", "decision_reason": "not_window_only",
			"parent_reference": "other_result", "question_window_confirmed": false,
			"carrier_read": "not_read", "interpreted_axis": "range", "carried_axis": "", "executed_axis": "range",
			"interpreted_axis_outcome": "vetoed", "refusal_basis": "none",
		}},
	} {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			raw, requestID := contextfabric.RunQuestionWindowScenarioForTest(t, tc.scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			want := map[string]any{"request_id": requestID, "window_receipt_count": 1}
			for key, value := range tc.want {
				want[key] = value
			}
			if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.WindowContinuationDecision, Want: want}); err != nil {
				t.Fatalf("certify %s: %v", tc.scenario, err)
			}
		})
	}
}
