package contextfabric_test

// The anchor binding transition line, certified against its eventspec
// declaration from the bytes the production slog JSON handler wrote during
// real Investigate calls. Every scenario differs from every other in at
// least one asserted value.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestTheAnchorBindingTransitionLineCertifiesAgainstItsSpecification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario string
		want     map[string]any
	}{
		{"decisive identity proven", map[string]any{
			"result_id": "result_5788_0001", "parent_result_id": "", "site": "decisive", "evaluation": "resolved",
			"parent_binding": "no_reference", "from_state": "unbound", "from_kind": "", "from_id": "",
			"model_anchor_kind": "", "named_expected_kind": "", "receipt_anchor_kind": "", "receipt_anchor_id": "",
			"caller_hint_ids": []any{}, "proven_anchor_ids": []any{"repository:repository:probe-alpha"}, "effective_kind": "",
			"to_state": "bound", "to_kind": "repository", "to_id": "repository:probe-alpha", "proof": "identity_proven", "reason": "identity_proven",
			"origin_result_id": "result_5788_0001", "contender_kind": "", "contender_id": "",
			"persisted": "persisted", "shadow_agreement": "agree", "disagreement_field": "none",
			"served_anchor_kind": "repository", "served_anchor_id": "repository:probe-alpha",
			"served_count_decision": "anchor_committed", "served_count_kind": "repository", "served_count_id": "repository:probe-alpha",
		}},
		{"window gated pending", map[string]any{
			"site": "window_confirmation_required", "evaluation": "window_gated", "parent_binding": "no_reference",
			"to_state": "pending_window_confirmation", "to_id": "repository:probe-alpha", "reason": "pending_window_confirmation",
			"persisted": "persisted", "shadow_agreement": "disagree", "disagreement_field": "pending_proof",
			"served_anchor_id": "", "served_count_decision": "not_evaluated",
		}},
		{"natural follow-up carried silent", map[string]any{
			"result_id": "result_5788_0002", "parent_result_id": "result_5788_0001", "site": "subjectless_terminal", "evaluation": "resolved",
			"parent_binding": "present", "from_state": "bound", "from_kind": "repository", "from_id": "repository:probe-alpha",
			"proven_anchor_ids": []any{}, "effective_kind": "repository",
			"to_state": "bound", "to_id": "repository:probe-alpha", "proof": "identity_proven", "reason": "carried_silent", "origin_result_id": "result_5788_0001",
			"shadow_agreement": "disagree", "disagreement_field": "carried_anchor", "served_anchor_id": "",
			"served_count_decision": "not_evaluated", "served_count_id": "",
		}},
		{"follow-up alias contested", map[string]any{
			"parent_binding": "present", "proven_anchor_ids": []any{"repository:repository:probe-beta"},
			"to_state": "contested", "to_id": "repository:probe-alpha", "reason": "contested_by_resolution",
			"contender_kind": "repository", "contender_id": "repository:probe-beta",
			"shadow_agreement": "disagree", "disagreement_field": "carried_anchor",
			"served_anchor_id": "repository:probe-beta", "served_count_id": "repository:probe-beta",
		}},
		{"model kind conflict", map[string]any{
			"parent_binding": "present", "model_anchor_kind": "project", "effective_kind": "repository",
			"to_state": "bound", "to_kind": "repository", "reason": "carried_reconfirmed",
			"shadow_agreement": "disagree", "disagreement_field": "count_anchor",
			"served_anchor_id": "repository:probe-alpha", "served_count_decision": "anchor_unresolved", "served_count_id": "",
		}},
	} {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			raw, requestID := contextfabric.RunAnchorBindingScenarioForTest(t, tc.scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			if lines := log.LinesWithMsg(eventspec.AnchorBindingTransition.Msg); len(lines) != 1 {
				t.Fatalf("anchor binding transition lines = %d, want exactly 1", len(lines))
			}
			want := map[string]any{"org_id": "org_acceptance", "request_id": requestID}
			for key, value := range tc.want {
				want[key] = value
			}
			result, err := certify.Certify(log, certify.Assertion{Event: eventspec.AnchorBindingTransition, Want: want})
			if err != nil {
				t.Fatalf("certify: %v", err)
			}
			t.Logf("CERTIFIED: %v", result.Line)
		})
	}
}
