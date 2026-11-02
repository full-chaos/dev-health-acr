//go:build fixturegraph

package fixturegraph

import (
	"fmt"
	"testing"
)

// A receipt the server offered on a clarification must resolve on the follow-up turn.
// Every option is redeemed, not the first: a dead option behind a live one hid
// CHAOS-12467's conflict. Each question names the options it must offer and the ones
// it must not, so a subtest cannot vanish because an offer did.
func TestOfferedReceiptsResolveOnTheFollowUpTurn(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("investigate_with_interpretation")
	one, _ := slugs(t)
	number := chScalar(t, fmt.Sprintf(`SELECT toString(number) FROM git_pull_requests FINAL WHERE org_id = %s GROUP BY number ORDER BY uniqExact(repo_id) DESC, number LIMIT 1`, sqlStr(orgID(t))))
	fields := map[string]string{
		"candidate_options": "prior_candidate_receipts",
		"kind_options":      "prior_kind_receipts",
		"handle_options":    "prior_handle_receipts",
		"anchor_options":    "prior_anchor_receipts",
	}
	for name, tc := range map[string]struct {
		handles   []doc
		must      []string
		mustNever []string
	}{
		// Both worlds hold this number, so the caller's own handle cannot decide, and
		// a near match of another number would conflict with it.
		"the request sends the handle": {
			handles:   []doc{{"kind": "pull_request", "pattern_id": "pull_request_number", "value": number}},
			must:      []string{"candidate_options", "kind_options"},
			mustNever: []string{"handle_options"},
		},
		// With no handle in the request, a near match's handle is a real offer.
		"only the question names the handle": {
			must: []string{"candidate_options", "kind_options", "handle_options"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			args := doc{
				"question":        fmt.Sprintf("What is the state of pull request %s in %s?", number, one),
				"expected_kinds":  []string{"pull_request"},
				"evidence_window": doc{"relative_id": "trailing_365d"},
				"interpretation": doc{
					"shape": "single_subject", "requested_judgment": "state of the pull request",
					"subject_terms": []string{number}, "requested_subject_kind": "pull_request",
					"time_context": doc{"axis": "current"}, "fact_requirements": []doc{{"kind": "status"}},
					"clarification_needed": false,
				},
				"contract":  c.interpretContract(),
				"synthesis": "client",
			}
			if tc.handles != nil {
				args["subject_handles"] = tc.handles
			}
			d1, raw1 := c.call("investigate_with_interpretation", args)
			s1 := structured(d1)
			status := str(s1, "status")
			t.Logf("turn 1 status=%s", status)
			if status != "clarification_required" {
				t.Fatalf("fixture defect: turn 1 must offer a clarification (status=%s): %.3000s", status, raw1)
			}
			for _, option := range tc.must {
				if len(list(s1, "structure_needs", option)) == 0 {
					t.Fatalf("turn 1 offers no %s, so its receipt is not tested: %.3000s", option, raw1)
				}
			}
			for _, option := range tc.mustNever {
				if offers := list(s1, "structure_needs", option); len(offers) != 0 {
					t.Fatalf("turn 1 offers %s, which cannot resolve here: %v", option, offers)
				}
			}
			resultID := str(s1, "result_id")
			for option, field := range fields {
				for i, offer := range list(s1, "structure_needs", option) {
					t.Run(fmt.Sprintf("%s/%d", option, i), func(t *testing.T) {
						rid := str(offer, "receipt_id")
						args2 := doc{}
						for k, v := range args {
							args2[k] = v
						}
						args2["parent_result_id"] = resultID
						args2[field] = []doc{{"result_id": resultID, "receipt_id": rid}}
						d2, raw2 := c.call("investigate_with_interpretation", args2)
						s2 := structured(d2)
						if str(s2, "status") == "no_match" {
							t.Fatalf("offered %s receipt did not resolve: limitations=%v\n%.3000s", option, get(s2, "limitations"), raw2)
						}
					})
				}
			}
		})
	}
}
