package contextfabric_test

// CHAOS-5582: the window continuation decision line, certified against its
// eventspec declaration from the bytes the production slog JSON handler wrote
// during a real Investigate call -- identity, level, exactly-one-per-request
// multiplicity, every declared field's presence, type and closed vocabulary,
// and the VALUES each scenario must carry. Every scenario differs from every
// other in at least one asserted value, so no two can certify the same line.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestCHAOS5582_TheContinuationDecisionLineCertifiesAgainstItsSpecification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario string
		want     map[string]any
	}{
		{"overridden_by_receipt", map[string]any{
			"continuation_disposition": "applied", "decision_reason": "none",
			"family_carried": "grouped_cohort_status", "family_fresh": "discovered_cohort_ranking", "family_accepted": "grouped_cohort_status",
			"family_source": "carried", "comparison_evaluated": true, "agreement": false, "conflict_reason": "non_window_context_conflict",
			"window_receipt_count": 1, "explicit_window_present": false,
			"interpreted_axis": "range", "carried_axis": "current", "executed_axis": "current",
			"interpreted_axis_outcome": "overridden_by_receipt",
		}},
		{"agreed", map[string]any{
			"continuation_disposition": "applied", "decision_reason": "none",
			"window_receipt_count": 1, "explicit_window_present": false,
			"interpreted_axis": "current", "carried_axis": "current", "executed_axis": "current",
			"interpreted_axis_outcome": "agreed",
		}},
		{"plural_receipts_vetoed", map[string]any{
			"continuation_disposition": "not_applicable", "decision_reason": "window_veto",
			"window_receipt_count": 2, "explicit_window_present": false,
			"interpreted_axis": "", "carried_axis": "", "executed_axis": "",
			"interpreted_axis_outcome": "not_evaluated",
		}},
		{"changed_question_vetoed", map[string]any{
			"continuation_disposition": "not_applicable", "decision_reason": "changed_question",
			"window_receipt_count": 1, "explicit_window_present": false,
			"interpreted_axis": "range", "carried_axis": "current", "executed_axis": "range",
			"interpreted_axis_outcome": "vetoed",
		}},
	} {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			raw, requestID := contextfabric.RunCHAOS5582ScenarioForTest(t, tc.scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			want := map[string]any{"request_id": requestID}
			for key, value := range tc.want {
				want[key] = value
			}
			result, err := certify.Certify(log, certify.Assertion{Event: eventspec.WindowContinuationDecision, Want: want})
			if err != nil {
				t.Fatalf("certify %s: %v", tc.scenario, err)
			}
			t.Logf("CERTIFIED %s: %v", tc.scenario, result.Line)
		})
	}
}

// Every key the production emitter writes is declared, and every declared key
// is written: the declaration and the line are compared on a real emitted
// line, in both directions.
func TestCHAOS5582_TheSpecificationDeclaresExactlyTheEmittedKeys(t *testing.T) {
	t.Parallel()
	raw, requestID := contextfabric.RunCHAOS5582ScenarioForTest(t, "overridden_by_receipt")
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	lines := log.LinesWithMsg(eventspec.WindowContinuationDecision.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	line := lines[0]
	if line["request_id"] != requestID {
		t.Fatalf("request_id = %v, want %s", line["request_id"], requestID)
	}
	declared := map[string]bool{}
	for _, field := range eventspec.WindowContinuationDecision.Fields {
		declared[field.Key] = true
		if _, ok := line[field.Key]; !ok {
			t.Errorf("declared key %q is not on the emitted line", field.Key)
		}
	}
	for key := range line {
		switch key {
		case "time", "level", "msg":
			continue
		}
		if !declared[key] {
			t.Errorf("emitted key %q is not declared on %s", key, eventspec.WindowContinuationDecision.ID)
		}
	}
}
