package contextfabric_test

// The count population scope line, certified against its eventspec declaration
// from the bytes the production slog JSON handler wrote during real Investigate
// calls. Every scenario differs from every other in at least one asserted value.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestTheCountPopulationScopeLineCertifiesAgainstItsSpecification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario string
		want     map[string]any
	}{
		{"scoped count, anchor unresolved, no_match", map[string]any{
			"expression_kind": "children_of_scope", "member_kind": "team", "requirement": "count/member/team",
			"committed": 0, "committed_anchors": 0, "candidates": 0, "anchor_candidates": 0, "member_set_resolved": true, "members": 3,
			"decision": "anchor_unresolved", "assembled_outcome": "unavailable", "counted": false, "served": 0, "reused": false,
		}},
		{"scoped count, anchor ambiguous", map[string]any{
			"committed": 0, "committed_anchors": 0, "candidates": 2, "anchor_candidates": 2, "members": 3,
			"decision": "anchor_ambiguous", "assembled_outcome": "unavailable", "counted": false, "served": 0,
		}},
		{"scoped count, anchor committed", map[string]any{
			"expression_kind": "children_of_scope", "committed": 1, "committed_anchors": 1, "committed_unbound": 0, "candidates": 1, "anchor_candidates": 1, "members": 3,
			"anchor_kind": "", "anchor_id": "repository:SCOPE_ANCHOR",
			"decision": "anchor_committed", "assembled_outcome": "satisfied", "counted": true, "served": 3,
		}},
		{"scoped count, unrelated committed subject", map[string]any{
			"committed": 1, "committed_anchors": 0, "committed_unbound": 1, "anchor_id": "",
			"decision": "anchor_unresolved", "assembled_outcome": "unavailable", "counted": false, "served": 0,
		}},
		{"scoped count, anchor term matched under the reading's anchor kind, normalized", map[string]any{
			"committed_anchors": 1, "committed_unbound": 0, "anchor_kind": "repository", "anchor_id": "repository:SCOPE_ANCHOR",
			"decision": "anchor_committed", "counted": true, "served": 3,
		}},
		{"scoped count, only a member-kind subject committed", map[string]any{
			"committed": 1, "committed_anchors": 0, "decision": "anchor_unresolved", "counted": false,
		}},
		{"organization-level discovered count, nothing committed", map[string]any{
			"expression_kind": "discovered_kind", "member_kind": "team", "committed": 0, "committed_anchors": 0, "members": 5,
			"decision": "organization_scope", "assembled_outcome": "satisfied", "counted": true, "served": 5,
		}},
	} {
		tc := tc
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()
			raw, requestID := contextfabric.RunCountPopulationScopeScenarioForTest(t, tc.scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			if lines := log.LinesWithMsg(eventspec.CountPopulationScope.Msg); len(lines) != 1 {
				t.Fatalf("count population scope lines = %d, want exactly 1", len(lines))
			}
			want := map[string]any{"org_id": "org_1", "request_id": requestID}
			for key, value := range tc.want {
				want[key] = value
			}
			result, err := certify.Certify(log, certify.Assertion{Event: eventspec.CountPopulationScope, Want: want})
			if err != nil {
				t.Fatalf("certify: %v", err)
			}
			t.Logf("CERTIFIED: %v", result.Line)
		})
	}
}
