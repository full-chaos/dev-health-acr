package falkorgraph

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func clientOrganizationCountFrame(t *testing.T, status string) *contextfabric.QuestionFrame {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"shape": "discovered_cohort", "requested_judgment": "how many pull requests are " + status,
		"requested_subject_kind": "pull_request", "subject_terms": []string{},
		"time_context":      map[string]any{"axis": "current"},
		"fact_requirements": []map[string]any{{"kind": "pull_requests"}}, "clarification_needed": false,
		"question_frame": map[string]any{
			"goals": []string{"count_or_aggregate"},
			"subject_expression": map[string]any{
				"kind": "organization_scope", "member_kind": "pull_request",
				"member_qualifier": "status", "member_qualifier_value": status,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, capture, err := genkitruntime.ParseInterpretationOutputSignals(raw, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent})
	if err != nil {
		t.Fatalf("ParseInterpretationOutputSignals: %v", err)
	}
	frame := capture.Frame.Frame
	return &frame
}

// TestAClientOrganizationCountAppliesAndNamesItsStatus: "how many pull requests
// are awaiting review / merged in the organization" counts the pull requests
// holding the status and says which status it counted.
func TestAClientOrganizationCountAppliesAndNamesItsStatus(t *testing.T) {
	pulls := organizationPullRequests(30, 20)
	served := map[string]int{}
	for _, status := range []string{"awaiting_review", "merged"} {
		result, _ := investigatePullRequestsFramed(t, countingSynthesizer{}, pulls, status, storage.Principal{OrgID: "org-1"}, false,
			func(string) *contextfabric.QuestionFrame { return clientOrganizationCountFrame(t, status) })
		served[status] = len(servedCohortIDs(result))
		named := false
		for _, l := range result.Limitations {
			if strings.Contains(l, contextfabric.PullRequestStatusFilterDisclosure(status)) {
				named = true
			}
		}
		if !named {
			t.Errorf("%s: status %q, no limitation names the status filter: %q", status, result.Status, result.Limitations)
		}
	}
	if served["awaiting_review"] == served["merged"] {
		t.Errorf("awaiting_review and merged both counted %d pull requests, want different counts", served["merged"])
	}
}
