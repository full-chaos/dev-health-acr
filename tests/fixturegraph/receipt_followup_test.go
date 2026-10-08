//go:build fixturegraph

package fixturegraph

import (
	"fmt"
	"testing"
)

// A receipt the server offered on a clarification must resolve on the follow-up turn.
func TestOfferedReceiptsResolveOnTheFollowUpTurn(t *testing.T) {
	c := connect(t, "FG_ORG_TOKEN_FILE")
	c.requireTools("investigate_with_interpretation")
	one, _ := slugs(t)
	number := chScalar(t, fmt.Sprintf(`SELECT toString(number) FROM git_pull_requests FINAL WHERE org_id = %s GROUP BY number ORDER BY uniqExact(repo_id) DESC, number LIMIT 1`, sqlStr(orgID(t))))
	args := doc{
		"question":        fmt.Sprintf("What is the state of pull request %s in %s?", number, one),
		"expected_kinds":  []string{"pull_request"},
		"subject_handles": []doc{{"kind": "pull_request", "pattern_id": "pull_request_number", "value": number}},
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
	d1, raw1 := c.call("investigate_with_interpretation", args)
	s1 := structured(d1)
	status := str(s1, "status")
	t.Logf("turn 1 status=%s", status)
	if status != "clarification_required" {
		t.Fatalf("fixture defect: turn 1 must offer a clarification (status=%s): %.3000s", status, raw1)
	}
	resultID := str(s1, "result_id")
	fields := map[string]string{
		"candidate_options": "prior_candidate_receipts",
		"kind_options":      "prior_kind_receipts",
		"handle_options":    "prior_handle_receipts",
		"anchor_options":    "prior_anchor_receipts",
	}
	for option, field := range fields {
		offers := list(s1, "structure_needs", option)
		if len(offers) == 0 {
			continue
		}
		t.Run(option, func(t *testing.T) {
			rid := str(offers[0], "receipt_id")
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
