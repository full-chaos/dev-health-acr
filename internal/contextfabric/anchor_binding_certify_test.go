package contextfabric_test

// The anchor binding transition line, certified against its eventspec
// declaration from the bytes the production slog JSON handler wrote during
// real Investigate calls. Every scenario differs from every other in at
// least one asserted value.

import (
	"fmt"
	"reflect"
	"sort"
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
			"parent_binding": "no_reference", "carry_checks": "not_applicable", "from_state": "unbound", "from_kind": "", "from_id": "",
			"model_anchor_kind": "", "named_expected_kind": "", "receipt_anchor_kind": "", "receipt_anchor_id": "",
			"caller_hint_ids": []any{}, "proven_anchor_ids": []any{"repository:repository:probe-alpha"}, "effective_kind": "",
			"to_state": "bound", "to_kind": "repository", "to_id": "repository:probe-alpha", "proof": "identity_proven", "reason": "identity_proven",
			"origin_result_id": "result_5788_0001", "contender_kind": "", "contender_id": "",
			"persisted": "persisted", "shadow_agreement": "agree", "disagreement_field": "none",
			"served_anchor_kind": "repository", "served_anchor_id": "repository:probe-alpha",
			"served_count_decision": "anchor_committed", "served_count_kind": "repository", "served_count_id": "repository:probe-alpha",
		}},
		{"window gated pending", map[string]any{
			"result_id": "result_5788_0001", "site": "window_confirmation_required", "evaluation": "window_gated", "parent_binding": "no_reference",
			"to_state": "pending_window_confirmation", "to_id": "repository:probe-alpha", "reason": "pending_window_confirmation",
			"persisted": "persisted", "shadow_agreement": "disagree", "disagreement_field": "pending_proof",
			"served_anchor_id": "", "served_count_decision": "not_evaluated",
		}},
		{"natural follow-up carried silent", map[string]any{
			"result_id": "result_5788_0002", "parent_result_id": "result_5788_0001", "site": "subjectless_terminal", "evaluation": "resolved",
			"parent_binding": "present", "carry_checks": "not_evaluated", "from_state": "bound", "from_kind": "repository", "from_id": "repository:probe-alpha",
			"proven_anchor_ids": []any{}, "effective_kind": "repository",
			"to_state": "bound", "to_id": "repository:probe-alpha", "proof": "identity_proven", "reason": "carried_silent", "origin_result_id": "result_5788_0001",
			"shadow_agreement": "disagree", "disagreement_field": "carried_anchor", "served_anchor_id": "",
			"served_count_decision": "not_evaluated", "served_count_id": "",
		}},
		{"follow-up alias contested", map[string]any{
			"result_id": "result_5788_0002", "site": "decisive", "carry_checks": "not_evaluated",
			"parent_binding": "present", "proven_anchor_ids": []any{"repository:repository:probe-beta"},
			"to_state": "contested", "to_id": "repository:probe-alpha", "reason": "contested_by_resolution",
			"contender_kind": "repository", "contender_id": "repository:probe-beta",
			"shadow_agreement": "disagree", "disagreement_field": "carried_anchor",
			"served_anchor_id": "repository:probe-beta", "served_count_id": "repository:probe-beta",
		}},
		{"model kind conflict", map[string]any{
			"result_id": "result_5788_0002", "site": "decisive", "carry_checks": "not_evaluated",
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
			lines := log.LinesWithMsg(eventspec.AnchorBindingTransition.Msg)
			if len(lines) != 1 {
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

// TestEveryClosedAnchorBindingTokenIsEmittedByTheRealProducer drives every
// transition-line driver through the production slog sink, certifies every
// line it wrote against the declaration, and requires every member of every
// closed vocabulary the declaration names -- except the fail-closed
// undeclared token -- to have been emitted at least once.
func TestEveryClosedAnchorBindingTokenIsEmittedByTheRealProducer(t *testing.T) {
	seen := map[string]map[string]bool{}
	lines := 0
	for _, driver := range contextfabric.RunAnchorBindingVocabularyForTest(t) {
		log, err := certify.Parse(driver.Log)
		if err != nil {
			t.Fatalf("%s: certify.Parse: %v", driver.Name, err)
		}
		driverLines := log.LinesWithMsg(eventspec.AnchorBindingTransition.Msg)
		if len(driverLines) == 0 {
			t.Fatalf("%s: the driver emitted no transition line", driver.Name)
		}
		for _, line := range driverLines {
			lines++
			want := map[string]any{"org_id": line["org_id"], "result_id": line["result_id"], "site": line["site"]}
			if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.AnchorBindingTransition, Want: want}); err != nil {
				t.Fatalf("%s: certify %v: %v", driver.Name, want, err)
			}
			for _, field := range eventspec.AnchorBindingTransition.Fields {
				if len(field.ClosedVocabulary) == 0 {
					continue
				}
				if seen[field.Key] == nil {
					seen[field.Key] = map[string]bool{}
				}
				seen[field.Key][fmt.Sprint(line[field.Key])] = true
			}
		}
	}
	t.Logf("certified %d production lines", lines)
	for _, field := range eventspec.AnchorBindingTransition.Fields {
		if len(field.ClosedVocabulary) == 0 {
			continue
		}
		var missing []string
		for _, member := range field.ClosedVocabulary {
			if member == contextfabric.AnchorBindingUndeclaredToken {
				if seen[field.Key][member] {
					t.Errorf("%s: a production driver emitted the fail-closed token", field.Key)
				}
				continue
			}
			if !seen[field.Key][member] {
				missing = append(missing, member)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: no production driver emitted %v", field.Key, missing)
		}
		if last := field.ClosedVocabulary[len(field.ClosedVocabulary)-1]; last != contextfabric.AnchorBindingUndeclaredToken {
			t.Errorf("%s: the declaration does not end with the fail-closed token", field.Key)
		}
	}
	if !reflect.DeepEqual(len(seen) > 0, true) {
		t.Fatal("no closed key was observed")
	}
}
